package config

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/trigger_cron.vcl
var triggerCronVCL []byte

//go:embed testdata/trigger_start.vcl
var triggerStartVCL []byte

//go:embed testdata/trigger_shutdown.vcl
var triggerShutdownVCL []byte

//go:embed testdata/trigger_signals.vcl
var triggerSignalsVCL []byte

//go:embed testdata/trigger_disabled.vcl
var triggerDisabledVCL []byte

//go:embed testdata/trigger_dup_name.vcl
var triggerDupNameVCL []byte

//go:embed testdata/trigger_dup_signal.vcl
var triggerDupSignalVCL []byte

//go:embed testdata/trigger_invalid_type.vcl
var triggerInvalidTypeVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func TestTriggerCron(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerCronVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Cron trigger adds one Startable (the cron scheduler)
	assert.Len(t, cfg.Startables, 1)
	// No cty value is created for cron triggers
	assert.NotContains(t, cfg.CtyTriggerMap, "ticker")
	// Name is tracked
	assert.Contains(t, cfg.TriggerDefRanges, "ticker")
}

func TestTriggerStart(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerStartVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Start trigger produces a cty value
	require.Contains(t, cfg.CtyTriggerMap, "init")
	val := cfg.CtyTriggerMap["init"]
	assert.Equal(t, cty.StringVal("hello from start"), val)

	// trigger.init is available in the eval context
	triggerVar, ok := cfg.evalCtx.Variables["trigger"]
	require.True(t, ok, "trigger variable should be set in evalCtx")
	assert.Equal(t, cty.StringVal("hello from start"), triggerVar.GetAttr("init"))

	// No Startables added (start triggers run at Process() time)
	assert.Empty(t, cfg.Startables)
}

func TestTriggerShutdown(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerShutdownVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Shutdown trigger adds one Stoppable
	assert.Len(t, cfg.Stoppables, 1)
	// No cty value created
	assert.NotContains(t, cfg.CtyTriggerMap, "bye")
	// Name is tracked
	assert.Contains(t, cfg.TriggerDefRanges, "bye")
}

func TestTriggerSignals(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerSignalsVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Signal action is registered
	assert.NotEmpty(t, cfg.SigActions.SignalActions)
	// No cty value created
	assert.NotContains(t, cfg.CtyTriggerMap, "main")
	// Name is tracked
	assert.Contains(t, cfg.TriggerDefRanges, "main")
}

func TestTriggerDisabled(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerDisabledVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Disabled trigger is not tracked
	assert.NotContains(t, cfg.TriggerDefRanges, "inactive")
	// No Startables added
	assert.Empty(t, cfg.Startables)
}

func TestTriggerDuplicateName(t *testing.T) {
	_, diags := NewConfig().WithSources(triggerDupNameVCL).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "expected error for duplicate trigger name")
	assert.Contains(t, diags.Error(), "Trigger already defined")
}

func TestTriggerDuplicateSignal(t *testing.T) {
	_, diags := NewConfig().WithSources(triggerDupSignalVCL).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "expected error for duplicate signal")
	assert.Contains(t, diags.Error(), "Signal already defined")
}

func TestTriggerInvalidType(t *testing.T) {
	_, diags := NewConfig().WithSources(triggerInvalidTypeVCL).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "expected error for invalid trigger type")
	assert.Contains(t, diags.Error(), "Invalid trigger type")
}
