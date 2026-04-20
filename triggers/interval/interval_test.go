package interval

import (
	"context"
	_ "embed"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/trigger_interval.vcl
var triggerIntervalVCL []byte

//go:embed testdata/trigger_interval_full.vcl
var triggerIntervalFullVCL []byte

//go:embed testdata/trigger_interval_dormant.vcl
var triggerIntervalDormantVCL []byte

//go:embed testdata/trigger_interval_repeat_false.vcl
var triggerIntervalRepeatFalseVCL []byte

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

	// Interval trigger adds one Startable, one PostStartable, and one Stoppable.
	assert.Len(t, c.Startables, 1)
	assert.Len(t, c.PostStartables, 1)
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
	require.NoError(t, trig.PostStart())
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

// --- Set / Reset / dormant / repeat tests ---

func TestTriggerIntervalDormantConfig(t *testing.T) {
	// A trigger with no delay should parse without error and start dormant.
	c, diags := cfg.NewConfig().WithSources(triggerIntervalDormantVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	require.Contains(t, c.CtyTriggerMap, "timer")

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["timer"])
	require.NoError(t, err)
	assert.False(t, trig.repeat)
	assert.False(t, cfg.IsExpressionProvided(trig.delayExpr))
}

func TestTriggerIntervalRepeatFalseConfig(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerIntervalRepeatFalseVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["oneshot"])
	require.NoError(t, err)
	assert.False(t, trig.repeat)
	assert.True(t, cfg.IsExpressionProvided(trig.delayExpr))
}

func TestTriggerIntervalInitialDelayWithoutDelay(t *testing.T) {
	src := []byte(`trigger "interval" "bad" { initial_delay = "1s" action = "x" }`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "expected error for initial_delay without delay")
}

func TestTriggerIntervalErrorDelayWithoutDelay(t *testing.T) {
	src := []byte(`trigger "interval" "bad" { error_delay = "1s" action = "x" }`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "expected error for error_delay without delay")
}

func TestTriggerIntervalSetWithDuration(t *testing.T) {
	trig := &IntervalTrigger{
		name:     "test",
		runCount: 5,
		setCh:    make(chan *time.Duration, 1),
	}

	result, err := trig.Set(context.Background(), []cty.Value{cty.StringVal("30s")})
	require.NoError(t, err)
	assert.Equal(t, "30s", result.AsString())

	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount, "set() should reset runCount")
	assert.NotNil(t, trig.delayOverride)
	assert.Equal(t, 30*time.Second, *trig.delayOverride)
	trig.mu.RUnlock()

	// Channel should have been signalled.
	select {
	case <-trig.setCh:
	default:
		t.Fatal("expected signal on setCh")
	}
}

func TestTriggerIntervalSetNoArgsWithDelay(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerIntervalVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["poller"])
	require.NoError(t, err)
	trig.setCh = make(chan *time.Duration, 1)
	trig.runCount = 3

	result, err := trig.Set(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "set() with no args should return null")

	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount)
	assert.Nil(t, trig.delayOverride)
	trig.mu.RUnlock()
}

func TestTriggerIntervalSetNoArgsWithoutDelay(t *testing.T) {
	trig := &IntervalTrigger{
		name:  "test",
		setCh: make(chan *time.Duration, 1),
		// delayExpr is nil/not provided
	}

	_, err := trig.Set(context.Background(), nil)
	assert.Error(t, err, "set() with no args and no configured delay should error")
}

func TestTriggerIntervalSetInvalidDuration(t *testing.T) {
	trig := &IntervalTrigger{
		name:  "test",
		setCh: make(chan *time.Duration, 1),
	}

	_, err := trig.Set(context.Background(), []cty.Value{cty.StringVal("not-a-duration")})
	assert.Error(t, err)
}

func TestTriggerIntervalResetClearsState(t *testing.T) {
	dur := 5 * time.Second
	trig := &IntervalTrigger{
		name:          "test",
		runCount:      3,
		lastResult:    cty.StringVal("hello"),
		lastError:     assert.AnError,
		delayOverride: &dur,
		resetCh:       make(chan struct{}, 1),
	}

	err := trig.Reset(context.Background())
	require.NoError(t, err)

	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount)
	assert.Equal(t, cty.NilVal, trig.lastResult)
	assert.Nil(t, trig.lastError)
	assert.Nil(t, trig.delayOverride)
	trig.mu.RUnlock()

	// Channel should have been signalled.
	select {
	case <-trig.resetCh:
	default:
		t.Fatal("expected signal on resetCh")
	}
}

func TestTriggerIntervalDormantStartAndSet(t *testing.T) {
	// A dormant trigger (no delay) should not fire until set() is called.
	c, diags := cfg.NewConfig().WithSources(triggerIntervalDormantVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["timer"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())

	// Wait a bit — trigger should not fire.
	time.Sleep(100 * time.Millisecond)
	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount, "dormant trigger should not fire before set()")
	trig.mu.RUnlock()

	// Set with a short delay — trigger should fire once then go dormant again.
	_, err = trig.Set(context.Background(), []cty.Value{cty.StringVal("50ms")})
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)
	trig.mu.RLock()
	assert.Equal(t, int64(1), trig.runCount, "trigger should have fired once after set()")
	trig.mu.RUnlock()

	// Should stay at 1 (dormant again because repeat=false).
	time.Sleep(200 * time.Millisecond)
	trig.mu.RLock()
	assert.Equal(t, int64(1), trig.runCount, "trigger should remain dormant after firing")
	trig.mu.RUnlock()

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

func TestTriggerIntervalRepeatFalseFiresOnce(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerIntervalRepeatFalseVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["oneshot"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())

	// delay=50ms, repeat=false: should fire once then stop.
	time.Sleep(300 * time.Millisecond)
	trig.mu.RLock()
	assert.Equal(t, int64(1), trig.runCount, "repeat=false trigger should fire exactly once")
	trig.mu.RUnlock()

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

func TestTriggerIntervalSetInterruptsSleep(t *testing.T) {
	// Start a trigger with a very long delay, then set() with a short one.
	c, diags := cfg.NewConfig().WithSources(triggerIntervalVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["poller"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())

	// The configured delay is 1h — should not fire.
	time.Sleep(50 * time.Millisecond)
	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount)
	trig.mu.RUnlock()

	// Set with 50ms — should interrupt the 1h sleep and fire quickly.
	_, err = trig.Set(context.Background(), []cty.Value{cty.StringVal("50ms")})
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)
	trig.mu.RLock()
	assert.GreaterOrEqual(t, trig.runCount, int64(1), "set() should have interrupted the sleep and caused the trigger to fire")
	trig.mu.RUnlock()

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

func TestTriggerIntervalResetGoesDormant(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerIntervalRepeatFalseVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetIntervalTriggerFromCapsule(c.CtyTriggerMap["oneshot"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())

	// Reset immediately — should cancel the 50ms delay and go dormant.
	require.NoError(t, trig.Reset(context.Background()))

	time.Sleep(200 * time.Millisecond)
	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount, "reset() should have prevented firing")
	trig.mu.RUnlock()

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
