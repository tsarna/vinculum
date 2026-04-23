package at

import (
	"context"
	_ "embed"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	timecty "github.com/tsarna/time-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/trigger_at.vcl
var triggerAtVCL []byte

//go:embed testdata/trigger_at_dormant.vcl
var triggerAtDormantVCL []byte

//go:embed testdata/trigger_at_repeat_false.vcl
var triggerAtRepeatFalseVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

// --- Config-level tests ---

func TestTriggerAt(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerAtVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	require.Contains(t, c.CtyTriggerMap, "alarm")
	val := c.CtyTriggerMap["alarm"]
	assert.Equal(t, AtCapsuleType, val.Type())

	// trigger.alarm is available in the eval context.
	triggerVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok)
	assert.Equal(t, AtCapsuleType, triggerVar.GetAttr("alarm").Type())

	// Adds one Startable, one PostStartable, and one Stoppable.
	assert.Len(t, c.Startables, 1)
	assert.Len(t, c.PostStartables, 1)
	assert.Len(t, c.Stoppables, 1)

	assert.Contains(t, c.TriggerDefRanges, "alarm")
}

func TestTriggerAtDependencyId(t *testing.T) {
	h := cfg.NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"at", "alarm"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.alarm", id)
}

func TestTriggerAtDormantConfig(t *testing.T) {
	// A trigger with no time expression should parse and start dormant.
	c, diags := cfg.NewConfig().WithSources(triggerAtDormantVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	require.Contains(t, c.CtyTriggerMap, "alarm")

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["alarm"])
	require.NoError(t, err)
	assert.False(t, cfg.IsExpressionProvided(trig.timeExpr))
	assert.True(t, trig.repeat)
}

func TestTriggerAtRepeatFalseConfig(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerAtRepeatFalseVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["oneshot"])
	require.NoError(t, err)
	assert.False(t, trig.repeat)
	assert.True(t, cfg.IsExpressionProvided(trig.timeExpr))
}

// --- Unit tests ---

func TestTriggerAtGetBeforeEval(t *testing.T) {
	trig := &AtTrigger{}
	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "expected null before first time evaluation")
}

func TestTriggerAtGetAfterEval(t *testing.T) {
	trig := &AtTrigger{}
	target := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	trig.mu.Lock()
	trig.scheduledTime = target
	trig.mu.Unlock()

	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, timecty.TimeCapsuleType, result.Type())

	got, err := timecty.GetTime(result)
	require.NoError(t, err)
	assert.True(t, got.Equal(target))
}

func TestTriggerAtSetWithTime(t *testing.T) {
	trig := &AtTrigger{
		name:     "test",
		runCount: 5,
		setCh:    make(chan *time.Time, 1),
	}

	target := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := trig.Set(context.Background(), []cty.Value{timecty.NewTimeCapsule(target)})
	require.NoError(t, err)

	got, err := timecty.GetTime(result)
	require.NoError(t, err)
	assert.True(t, got.Equal(target))

	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount, "set() should reset runCount")
	require.NotNil(t, trig.timeOverride)
	assert.True(t, trig.timeOverride.Equal(target))
	trig.mu.RUnlock()

	select {
	case payload := <-trig.setCh:
		require.NotNil(t, payload)
		assert.True(t, payload.Equal(target))
	default:
		t.Fatal("Set() did not signal setCh")
	}
}

func TestTriggerAtSetNoArgsWithTimeExpr(t *testing.T) {
	// With a configured time expression, set() with no args is a poke.
	c, diags := cfg.NewConfig().WithSources(triggerAtVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["alarm"])
	require.NoError(t, err)
	trig.setCh = make(chan *time.Time, 1)
	trig.runCount = 3

	result, err := trig.Set(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "set() with no args should return null")

	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount)
	assert.Nil(t, trig.timeOverride)
	trig.mu.RUnlock()

	select {
	case payload := <-trig.setCh:
		assert.Nil(t, payload)
	default:
		t.Fatal("Set() did not signal setCh")
	}
}

func TestTriggerAtSetNoArgsWithoutTimeExpr(t *testing.T) {
	trig := &AtTrigger{
		name:  "test",
		setCh: make(chan *time.Time, 1),
		// timeExpr is not provided
	}
	_, err := trig.Set(context.Background(), nil)
	assert.Error(t, err, "set() with no args and no time expression should error")
}

func TestTriggerAtSetInvalidTime(t *testing.T) {
	trig := &AtTrigger{
		name:  "test",
		setCh: make(chan *time.Time, 1),
	}
	_, err := trig.Set(context.Background(), []cty.Value{cty.StringVal("not-a-time")})
	assert.Error(t, err)
}

func TestTriggerAtSetNonBlocking(t *testing.T) {
	// A second Set() while the buffer is full must not block.
	trig := &AtTrigger{
		name:  "test",
		setCh: make(chan *time.Time, 1),
	}

	target := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := trig.Set(context.Background(), []cty.Value{timecty.NewTimeCapsule(target)})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = trig.Set(context.Background(), []cty.Value{timecty.NewTimeCapsule(target)})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Set() blocked unexpectedly")
	}
}

func TestTriggerAtResetClearsState(t *testing.T) {
	target := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	trig := &AtTrigger{
		name:          "test",
		runCount:      3,
		scheduledTime: target,
		lastResult:    cty.StringVal("hello"),
		lastError:     assert.AnError,
		timeOverride:  &target,
		resetCh:       make(chan struct{}, 1),
	}

	require.NoError(t, trig.Reset(context.Background()))

	trig.mu.RLock()
	assert.Equal(t, int64(0), trig.runCount)
	assert.Equal(t, cty.NilVal, trig.lastResult)
	assert.Nil(t, trig.lastError)
	assert.Nil(t, trig.timeOverride)
	assert.True(t, trig.scheduledTime.IsZero())
	trig.mu.RUnlock()

	select {
	case <-trig.resetCh:
	default:
		t.Fatal("Reset() did not signal resetCh")
	}
}

// --- Integration tests ---

func TestTriggerAtStopBeforeFire(t *testing.T) {
	// A trigger with a far-future time must stop promptly without firing.
	src := []byte(`
trigger "at" "future" {
    time   = timeadd(now(), duration("999h"))
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["future"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())

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

	trig.mu.RLock()
	count := trig.runCount
	trig.mu.RUnlock()
	assert.Equal(t, int64(0), count, "action should not have fired before stop")
}

func TestTriggerAtFiresAtScheduledTime(t *testing.T) {
	src := []byte(`
trigger "at" "fast" {
    time   = timeadd(now(), duration("50ms"))
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["fast"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(300 * time.Millisecond)

	trig.mu.RLock()
	count := trig.runCount
	trig.mu.RUnlock()
	assert.GreaterOrEqual(t, count, int64(1), "expected at trigger to have fired at least once")

	require.NoError(t, trig.Stop())
}

func TestTriggerAtRepeats(t *testing.T) {
	// After each firing the trigger re-evaluates time; it should fire multiple times.
	src := []byte(`
trigger "at" "fast" {
    time   = timeadd(now(), duration("50ms"))
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["fast"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(500 * time.Millisecond)

	trig.mu.RLock()
	count := trig.runCount
	trig.mu.RUnlock()
	assert.GreaterOrEqual(t, count, int64(2), "expected at trigger to have repeated")

	require.NoError(t, trig.Stop())
}

func TestTriggerAtRepeatFalseFiresOnce(t *testing.T) {
	src := []byte(`
trigger "at" "oneshot" {
    time   = timeadd(now(), duration("50ms"))
    repeat = false
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["oneshot"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(400 * time.Millisecond)

	trig.mu.RLock()
	count := trig.runCount
	trig.mu.RUnlock()
	assert.Equal(t, int64(1), count, "expected exactly one fire with repeat=false")

	require.NoError(t, trig.Stop())
}

func TestTriggerAtPastTimeFiresImmediately(t *testing.T) {
	src := []byte(`
trigger "at" "past" {
    time   = timeadd(now(), duration("-1s"))
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["past"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(200 * time.Millisecond)

	trig.mu.RLock()
	count := trig.runCount
	trig.mu.RUnlock()
	assert.GreaterOrEqual(t, count, int64(1), "expected immediate fire for past time")

	require.NoError(t, trig.Stop())
}

func TestTriggerAtGetReturnsScheduledTime(t *testing.T) {
	src := []byte(`
trigger "at" "future" {
    time   = timeadd(now(), duration("999h"))
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["future"])
	require.NoError(t, err)

	// Before start: Get() returns null.
	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "expected null before Start()")

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(100 * time.Millisecond) // let first time evaluation complete

	// After first evaluation: Get() returns a time capsule roughly 999h from now.
	result, err = trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, timecty.TimeCapsuleType, result.Type(), "expected time capsule from Get()")

	got, err := timecty.GetTime(result)
	require.NoError(t, err)
	assert.True(t, got.After(time.Now().Add(998*time.Hour)), "scheduled time should be ~999h in the future")

	require.NoError(t, trig.Stop())
}

func TestTriggerAtSetForcesReevaluation(t *testing.T) {
	// Start with a far-future time. After a brief wait, record scheduledTime.
	// Then call Set() and after another brief wait verify scheduledTime advanced
	// (because now() was called again in re-evaluation).
	src := []byte(`
trigger "at" "slow" {
    time   = timeadd(now(), duration("999h"))
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["slow"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(100 * time.Millisecond) // wait for first evaluation

	trig.mu.RLock()
	first := trig.scheduledTime
	trig.mu.RUnlock()
	require.False(t, first.IsZero(), "expected scheduledTime to be set after first eval")

	// Small delay so the clocks advance before calling Set().
	time.Sleep(20 * time.Millisecond)

	_, err = trig.Set(context.Background(), nil)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond) // wait for re-evaluation

	trig.mu.RLock()
	second := trig.scheduledTime
	trig.mu.RUnlock()

	// Re-evaluation called now() again, so the new scheduled time should be
	// strictly later than the first one.
	assert.True(t, second.After(first), "Set() should cause scheduledTime to advance via re-evaluation")

	require.NoError(t, trig.Stop())
}

func TestTriggerAtDormantRevivedBySet(t *testing.T) {
	// Dormant config (no time); Set() with an override should cause a fire.
	src := []byte(`
trigger "at" "armed" {
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["armed"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(50 * time.Millisecond) // let the goroutine enter waitForRevival

	// runCount should still be zero — trigger is dormant.
	trig.mu.RLock()
	count := trig.runCount
	trig.mu.RUnlock()
	assert.Equal(t, int64(0), count, "dormant trigger should not fire")

	// Revive with an override 50ms out.
	target := time.Now().Add(50 * time.Millisecond)
	_, err = trig.Set(context.Background(), []cty.Value{timecty.NewTimeCapsule(target)})
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond) // wait for fire + attempt next iteration

	trig.mu.RLock()
	count = trig.runCount
	trig.mu.RUnlock()
	assert.GreaterOrEqual(t, count, int64(1), "expected at least one fire after Set()")

	require.NoError(t, trig.Stop())
}

func TestTriggerAtResetGoesDormant(t *testing.T) {
	// A running trigger that gets Reset() should go dormant and not fire.
	src := []byte(`
trigger "at" "cancellable" {
    time   = timeadd(now(), duration("100ms"))
    action = "ring"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetAtTriggerFromCapsule(c.CtyTriggerMap["cancellable"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	time.Sleep(20 * time.Millisecond) // let scheduling happen

	require.NoError(t, trig.Reset(context.Background()))

	time.Sleep(200 * time.Millisecond) // past the original 100ms fire time

	trig.mu.RLock()
	count := trig.runCount
	trig.mu.RUnlock()
	assert.Equal(t, int64(0), count, "Reset() should prevent the scheduled fire")

	require.NoError(t, trig.Stop())
}
