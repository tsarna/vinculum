package interval

import (
	"context"
	_ "embed"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/trigger_interval.vcl
var triggerIntervalVCL []byte

//go:embed testdata/trigger_interval_full.vcl
var triggerIntervalFullVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func TestTriggerInterval(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerIntervalVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Interval trigger stores an IntervalCapsuleType value in CtyTriggerMap.
	require.Contains(t, c.CtyTriggerMap, "poller")
	val := c.CtyTriggerMap["poller"]
	assert.Equal(t, IntervalCapsuleType, val.Type())

	// trigger.poller is available in the eval context.
	triggerVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok, "trigger variable should be set in evalCtx")
	assert.Equal(t, IntervalCapsuleType, triggerVar.GetAttr("poller").Type())

	// Interval trigger adds one Startable and one Stoppable.
	assert.Len(t, c.Startables, 1)
	assert.Len(t, c.Stoppables, 1)

	// Name is tracked for uniqueness enforcement.
	assert.Contains(t, c.TriggerDefRanges, "poller")
}

func TestTriggerIntervalFull(t *testing.T) {
	// All optional attributes parse without error.
	c, diags := cfg.NewConfig().WithSources(triggerIntervalFullVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	require.Contains(t, c.CtyTriggerMap, "full")

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["full"])
	require.NoError(t, err)
	assert.Equal(t, 0.1, trig.jitter)
	assert.True(t, cfg.IsExpressionProvided(trig.initialDelayExpr))
	assert.True(t, cfg.IsExpressionProvided(trig.errorDelayExpr))
	assert.True(t, cfg.IsExpressionProvided(trig.stopWhenExpr))
}

func TestTriggerIntervalDependencyId(t *testing.T) {
	h := cfg.NewTriggerBlockHandler()
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
	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "expected null before first run")
}

func TestTriggerIntervalGetAfterRun(t *testing.T) {
	trig := &IntervalTrigger{}
	trig.mu.Lock()
	trig.runCount = 1
	trig.lastResult = cty.StringVal("hello")
	trig.mu.Unlock()

	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("hello")))
}

func TestTriggerIntervalGetError(t *testing.T) {
	trig := &IntervalTrigger{}
	trig.mu.Lock()
	trig.runCount = 1
	trig.lastError = assert.AnError
	trig.mu.Unlock()

	_, err := trig.Get(context.Background(), nil)
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
	c, diags := cfg.NewConfig().WithSources(triggerIntervalVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["poller"])
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
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "expected error for jitter > 1")
}
