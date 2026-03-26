package config

import (
	_ "embed"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

//go:embed testdata/trigger_after.vcl
var triggerAfterVCL []byte

func TestTriggerAfter(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerAfterVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	require.Contains(t, cfg.CtyTriggerMap, "warmup")
	val := cfg.CtyTriggerMap["warmup"]
	assert.Equal(t, AfterCapsuleType, val.Type())

	// trigger.warmup is available in the eval context.
	triggerVar, ok := cfg.evalCtx.Variables["trigger"]
	require.True(t, ok)
	assert.Equal(t, AfterCapsuleType, triggerVar.GetAttr("warmup").Type())

	// Adds one Startable and one Stoppable.
	assert.Len(t, cfg.Startables, 1)
	assert.Len(t, cfg.Stoppables, 1)

	// Delay is parsed at config time.
	trig, err := GetAfterTriggerFromCapsule(val)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, trig.delay)

	assert.Contains(t, cfg.TriggerDefRanges, "warmup")
}

func TestTriggerAfterDependencyId(t *testing.T) {
	h := NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"after", "warmup"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.warmup", id)
}

func TestTriggerAfterGetBeforeFire(t *testing.T) {
	trig := &AfterTrigger{}
	result, err := trig.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "expected null before trigger fires")
}

func TestTriggerAfterGetAfterFire(t *testing.T) {
	trig := &AfterTrigger{}
	trig.mu.Lock()
	trig.result = cty.StringVal("warmed up")
	trig.mu.Unlock()

	result, err := trig.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("warmed up")))
}

func TestTriggerAfterGetError(t *testing.T) {
	trig := &AfterTrigger{}
	trig.mu.Lock()
	trig.err = assert.AnError
	trig.mu.Unlock()

	_, err := trig.Get(nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestTriggerAfterStopBeforeFire(t *testing.T) {
	// Stop() before the delay elapses must return promptly without firing.
	cfg, diags := NewConfig().WithSources(triggerAfterVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetAfterTriggerFromCapsule(cfg.CtyTriggerMap["warmup"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = trig.Stop()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked longer than expected")
	}

	// Action was skipped — result remains null.
	result, err := trig.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "action should not have fired after Stop()")
}
