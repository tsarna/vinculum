package conditions

import (
	"context"
	_ "embed"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	_ "github.com/tsarna/vinculum/functions"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/timer_minimal.vcl
var timerMinimalVCL []byte

//go:embed testdata/timer_full.vcl
var timerFullVCL []byte

//go:embed testdata/timer_composed.vcl
var timerComposedVCL []byte

//go:embed testdata/timer_unknown_subtype.vcl
var timerUnknownSubtypeVCL []byte

//go:embed testdata/timer_start_active.vcl
var timerStartActiveVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	l, err := zap.NewDevelopment()
	require.NoError(t, err)
	return l
}

func buildConfig(t *testing.T, src []byte) *cfg.Config {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %s", diags.Error())
	return c
}

func TestTimerBlockMinimal(t *testing.T) {
	c := buildConfig(t, timerMinimalVCL)
	require.Contains(t, c.CtyConditionMap, "door_open")
	val := c.CtyConditionMap["door_open"]
	assert.Equal(t, TimerConditionCapsuleType, val.Type())

	cond := val.EncapsulatedValue().(*TimerCondition)
	// condition.door_open is exposed in eval context.
	conditionVar, ok := c.EvalCtx().Variables["condition"]
	require.True(t, ok)
	assert.Equal(t, TimerConditionCapsuleType, conditionVar.GetAttr("door_open").Type())
	_ = cond
}

func TestTimerBlockFullDecode(t *testing.T) {
	c := buildConfig(t, timerFullVCL)
	require.Contains(t, c.CtyConditionMap, "high_temp")
	cond := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*TimerCondition)
	assert.Equal(t, 30*time.Second, cond.sm.behavior.ActivateAfter)
	assert.Equal(t, 5*time.Minute, cond.sm.behavior.DeactivateAfter)
	assert.Equal(t, time.Hour, cond.sm.behavior.Timeout)
	assert.Equal(t, 10*time.Minute, cond.sm.behavior.Cooldown)
	assert.Equal(t, 50*time.Millisecond, cond.debounce)
}

func TestTimerUnknownSubtypeRejected(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources(timerUnknownSubtypeVCL).
		WithLogger(testLogger(t)).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "Unknown condition subtype")
}

func TestTimerBlockComposed(t *testing.T) {
	c := buildConfig(t, timerComposedVCL)
	require.Contains(t, c.CtyConditionMap, "high_temp")
	require.Contains(t, c.CtyConditionMap, "low_voltage")
	require.Contains(t, c.CtyConditionMap, "system_fault")

	high := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*TimerCondition)
	fault := c.CtyConditionMap["system_fault"].EncapsulatedValue().(*TimerCondition)
	assert.True(t, fault.hasInputExpr, "system_fault must reject set() once declared")
	require.NotNil(t, fault.inputExpr)
	assert.Len(t, fault.inputExpr.Sources(), 2,
		"fault.input references high_temp and low_voltage — two Watchables")

	// Wire up reactive exprs (normally done in Startables phase).
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	// Drive high_temp via set(); fault should latch active via input.
	_, err := high.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	require.Equal(t, "active", stateMust(t, fault))

	_, err = high.Set(context.Background(), []cty.Value{cty.False})
	require.NoError(t, err)
	assert.Equal(t, "active", stateMust(t, fault), "latch holds through input=false")
}

func TestTimerClearReSamplesDeclaredInput(t *testing.T) {
	// clear() releases the latch but must re-sample input=. If the input is
	// still asserted, the condition re-activates (and re-latches). If the
	// input is no longer asserted, the condition stays inactive.
	c := buildConfig(t, timerComposedVCL)
	high := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*TimerCondition)
	fault := c.CtyConditionMap["system_fault"].EncapsulatedValue().(*TimerCondition)
	require.True(t, fault.sm.behavior.Latch)

	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	// Latch the fault by asserting an upstream condition.
	_, err := high.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	require.Equal(t, "active", stateMust(t, fault))

	// Upstream input is still asserted — clear() must NOT silence an
	// ongoing-true input. The fault re-activates and re-latches.
	require.NoError(t, fault.Clear(context.Background()))
	assert.Equal(t, "active", stateMust(t, fault),
		"clear() with input still true should re-activate")
	require.True(t, fault.sm.latched, "re-activation must re-engage the latch")

	// Drop the upstream input. Latch holds because we haven't cleared again.
	_, err = high.Set(context.Background(), []cty.Value{cty.False})
	require.NoError(t, err)
	require.Equal(t, "active", stateMust(t, fault), "latch holds through input=false")

	// Now clear with the input no longer asserted — fault stays inactive.
	require.NoError(t, fault.Clear(context.Background()))
	assert.Equal(t, "inactive", stateMust(t, fault),
		"clear() with input now false should stay inactive")
	assert.False(t, fault.sm.latched)
}

func TestTimerSetRejectedWhenInputDeclared(t *testing.T) {
	c := buildConfig(t, timerComposedVCL)
	fault := c.CtyConditionMap["system_fault"].EncapsulatedValue().(*TimerCondition)
	_, err := fault.Set(context.Background(), []cty.Value{cty.True})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has declared input")
}

func TestTimerDebounceFiltersTransients(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{}, clock)
	tc := &TimerCondition{
		name:     "bounce",
		sm:       sm,
		clock:    clock,
		debounce: 50 * time.Millisecond,
	}
	// Transient pulse: true then false within the debounce window.
	tc.submitInput(context.Background(), true)
	clock.Advance(30 * time.Millisecond)
	tc.submitInput(context.Background(), false)
	clock.Advance(100 * time.Millisecond)
	assert.Equal(t, "inactive", stateMust(t, tc), "transient pulse should not activate")

	// Stable assertion: true holds past the window.
	tc.submitInput(context.Background(), true)
	clock.Advance(100 * time.Millisecond)
	assert.Equal(t, "active", stateMust(t, tc))
}

func TestTimerDebouncePlusActivateAfter(t *testing.T) {
	// Spec §Timer-Specific Attributes: debounce first, then activate_after.
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{ActivateAfter: 2 * time.Second}, clock)
	tc := &TimerCondition{
		name:     "seq",
		sm:       sm,
		clock:    clock,
		debounce: 100 * time.Millisecond,
	}
	tc.submitInput(context.Background(), true)
	clock.Advance(100 * time.Millisecond)
	assert.Equal(t, "pending_activation", stateMust(t, tc), "debounce elapsed, activate_after begins")
	clock.Advance(2 * time.Second)
	assert.Equal(t, "active", stateMust(t, tc))
}

func TestTimerTimeoutAllowsReassertion(t *testing.T) {
	// After a timeout-driven deactivation, set(true) must register as a fresh
	// rising edge and re-activate. Previously the wrapper's stableInput cache
	// (and the SM's rawInput) remained "true" after timeout, so the
	// re-assertion was silently deduped at both layers and the SM never saw
	// the edge.
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Timeout: 5 * time.Minute}, clock)
	tc := &TimerCondition{
		name:  "preempt",
		sm:    sm,
		clock: clock,
	}

	_, err := tc.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	require.Equal(t, "active", stateMust(t, tc))

	clock.Advance(5 * time.Minute)
	require.Equal(t, "inactive", stateMust(t, tc),
		"timeout should auto-deactivate")

	// Re-assert. Before the fix this was silently dropped.
	_, err = tc.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	assert.Equal(t, "active", stateMust(t, tc),
		"set(true) after timeout should re-activate")

	// And the new active session has its own timeout running.
	clock.Advance(5 * time.Minute)
	assert.Equal(t, "inactive", stateMust(t, tc),
		"second timeout should fire on the re-asserted session")
}

func TestTimerTimeoutAllowsReassertionWithDebounce(t *testing.T) {
	// Same scenario as TestTimerTimeoutAllowsReassertion, but with debounce
	// enabled — the wrapper's stableInput must still be reconciled with the
	// SM's rawInput so the post-timeout re-assertion starts a fresh debounce
	// edge rather than being collapsed as "settled to stable".
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Timeout: 5 * time.Minute}, clock)
	tc := &TimerCondition{
		name:     "preempt",
		sm:       sm,
		clock:    clock,
		debounce: 50 * time.Millisecond,
	}

	tc.submitInput(context.Background(), true)
	clock.Advance(50 * time.Millisecond)
	require.Equal(t, "active", stateMust(t, tc))

	clock.Advance(5 * time.Minute)
	require.Equal(t, "inactive", stateMust(t, tc))

	tc.submitInput(context.Background(), true)
	clock.Advance(50 * time.Millisecond)
	assert.Equal(t, "active", stateMust(t, tc),
		"debounced re-assertion after timeout should re-activate")
}

func TestTimerStartActiveLatched(t *testing.T) {
	c := buildConfig(t, timerStartActiveVCL)
	cond := c.CtyConditionMap["fault"].EncapsulatedValue().(*TimerCondition)
	assert.True(t, cond.sm.behavior.StartActive)
	assert.True(t, cond.sm.behavior.Latch)

	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	assert.Equal(t, "active", stateMust(t, cond), "boot-latched")
	out, err := cond.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, out.True())

	// set(false) cannot clear a latched condition.
	_, err = cond.Set(context.Background(), []cty.Value{cty.False})
	require.NoError(t, err)
	assert.Equal(t, "active", stateMust(t, cond), "latch holds through set(false)")

	require.NoError(t, cond.Clear(context.Background()))
	assert.Equal(t, "inactive", stateMust(t, cond))
}

func stateMust(t *testing.T, tc interface {
	State(context.Context) (string, error)
}) string {
	t.Helper()
	s, err := tc.State(context.Background())
	require.NoError(t, err)
	return s
}
