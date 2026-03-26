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

//go:embed testdata/trigger_interval.vcl
var triggerIntervalVCL []byte

//go:embed testdata/trigger_interval_full.vcl
var triggerIntervalFullVCL []byte

func TestTriggerInterval(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerIntervalVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Interval trigger stores an IntervalCapsuleType value in CtyTriggerMap.
	require.Contains(t, cfg.CtyTriggerMap, "poller")
	val := cfg.CtyTriggerMap["poller"]
	assert.Equal(t, IntervalCapsuleType, val.Type())

	// trigger.poller is available in the eval context.
	triggerVar, ok := cfg.evalCtx.Variables["trigger"]
	require.True(t, ok, "trigger variable should be set in evalCtx")
	assert.Equal(t, IntervalCapsuleType, triggerVar.GetAttr("poller").Type())

	// Interval trigger adds one Startable and one Stoppable.
	assert.Len(t, cfg.Startables, 1)
	assert.Len(t, cfg.Stoppables, 1)

	// Name is tracked for uniqueness enforcement.
	assert.Contains(t, cfg.TriggerDefRanges, "poller")
}

func TestTriggerIntervalFull(t *testing.T) {
	// All optional attributes parse without error.
	cfg, diags := NewConfig().WithSources(triggerIntervalFullVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	require.Contains(t, cfg.CtyTriggerMap, "full")

	trig, err := GetIntervalTriggerFromCapsule(cfg.CtyTriggerMap["full"])
	require.NoError(t, err)
	assert.Equal(t, 0.1, trig.jitter)
	assert.True(t, IsExpressionProvided(trig.initialDelayExpr))
	assert.True(t, IsExpressionProvided(trig.errorDelayExpr))
	assert.True(t, IsExpressionProvided(trig.stopWhenExpr))
}

func TestTriggerIntervalDependencyId(t *testing.T) {
	h := NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"interval", "poller"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.poller", id)
}

func TestTriggerIntervalGetBeforeRun(t *testing.T) {
	trig := &IntervalTrigger{}
	result, err := trig.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "expected null before first run")
}

func TestTriggerIntervalGetAfterRun(t *testing.T) {
	trig := &IntervalTrigger{}
	trig.mu.Lock()
	trig.runCount = 1
	trig.lastResult = cty.StringVal("hello")
	trig.mu.Unlock()

	result, err := trig.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("hello")))
}

func TestTriggerIntervalGetError(t *testing.T) {
	trig := &IntervalTrigger{}
	trig.mu.Lock()
	trig.runCount = 1
	trig.lastError = assert.AnError
	trig.mu.Unlock()

	_, err := trig.Get(nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestTriggerIntervalApplyJitter(t *testing.T) {
	base := 100 * time.Millisecond

	// jitter=0: no change.
	trig := &IntervalTrigger{jitter: 0}
	assert.Equal(t, base, trig.applyJitter(base))

	// jitter=0.2: result should fall in [90ms, 110ms].
	trig = &IntervalTrigger{jitter: 0.2}
	for i := 0; i < 100; i++ {
		got := trig.applyJitter(base)
		assert.GreaterOrEqual(t, got, 90*time.Millisecond, "jittered duration below minimum")
		assert.Less(t, got, 110*time.Millisecond, "jittered duration above maximum")
	}
}

func TestTriggerIntervalStartStop(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerIntervalVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetIntervalTriggerFromCapsule(cfg.CtyTriggerMap["poller"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	// Stop should return promptly, not block for the 1h delay.
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
}

func TestTriggerIntervalInvalidJitter(t *testing.T) {
	src := []byte(`trigger "interval" "bad" { delay = "1s" jitter = 1.5 action = "x" }`)
	_, diags := NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "expected error for jitter > 1")
}
