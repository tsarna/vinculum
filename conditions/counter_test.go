package conditions

import (
	"context"
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

//go:embed testdata/counter_basic.vcl
var counterBasicVCL []byte

//go:embed testdata/counter_count_down.vcl
var counterCountDownVCL []byte

//go:embed testdata/counter_rejects_input.vcl
var counterRejectsInputVCL []byte

//go:embed testdata/counter_missing_preset.vcl
var counterMissingPresetVCL []byte

//go:embed testdata/counter_start_active.vcl
var counterStartActiveVCL []byte

//go:embed testdata/counter_start_active_unlatched.vcl
var counterStartActiveUnlatchedVCL []byte

func TestCounterBasicDecode(t *testing.T) {
	c := buildConfig(t, counterBasicVCL)
	require.Contains(t, c.CtyConditionMap, "fault_count")
	cond := c.CtyConditionMap["fault_count"].EncapsulatedValue().(*CounterCondition)
	assert.Equal(t, int64(5), cond.preset)
	assert.Equal(t, int64(0), cond.initial)
	assert.True(t, cond.sm.behavior.Latch)
	assert.False(t, cond.countDown)
	assert.False(t, cond.rollover)
}

func TestCounterCountDownDecode(t *testing.T) {
	c := buildConfig(t, counterCountDownVCL)
	cond := c.CtyConditionMap["batch_remaining"].EncapsulatedValue().(*CounterCondition)
	assert.Equal(t, int64(10), cond.initial)
	assert.Equal(t, int64(0), cond.preset)
	assert.True(t, cond.countDown)
	cnt, _ := cond.Count(context.Background())
	assert.Equal(t, int64(10), cnt, "count seeded from initial")
}

func TestCounterRejectsForbiddenAttrs(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources(counterRejectsInputVCL).
		WithLogger(testLogger(t)).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "do not support")
}

func TestCounterRequiresPreset(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources(counterMissingPresetVCL).
		WithLogger(testLogger(t)).Build()
	require.True(t, diags.HasErrors())
}

// --- behavior (unit-level) ---

func newTestCounter(preset, initial int64, rollover, countDown, latch bool) *CounterCondition {
	clock := newFakeClock()
	return &CounterCondition{
		name:      "c",
		sm:        NewStateMachine(Behavior{Latch: latch}, clock),
		initial:   initial,
		preset:    preset,
		count:     initial,
		rollover:  rollover,
		countDown: countDown,
	}
}

func TestCounterCountUpReachesPreset(t *testing.T) {
	c := newTestCounter(3, 0, false, false, false)
	require.Equal(t, "inactive", stateMust(t, c))

	for i := 0; i < 2; i++ {
		_, _ = c.Increment(context.Background(), nil)
	}
	assert.Equal(t, "inactive", stateMust(t, c), "below preset")

	_, _ = c.Increment(context.Background(), nil)
	assert.Equal(t, "active", stateMust(t, c), "reached preset")

	// Decrement back below preset.
	_, _ = c.Increment(context.Background(), []cty.Value{cty.NumberIntVal(-1)})
	assert.Equal(t, "inactive", stateMust(t, c))
}

func TestCounterDecrementClampsAtZero(t *testing.T) {
	c := newTestCounter(3, 0, false, false, false)
	_, _ = c.Increment(context.Background(), []cty.Value{cty.NumberIntVal(-5)})
	cnt, _ := c.Count(context.Background())
	assert.Equal(t, int64(0), cnt, "low side clamp")
}

func TestCounterRolloverPulsesAndAutoResets(t *testing.T) {
	c := newTestCounter(3, 0, true, false, false)
	w := &capturingWatcher{}
	c.Watch(w)

	// Increment to preset; expect a one-shot pulse and count back to 0.
	for i := 0; i < 3; i++ {
		_, _ = c.Increment(context.Background(), nil)
	}
	cnt, _ := c.Count(context.Background())
	assert.Equal(t, int64(0), cnt, "auto-reset to initial")
	assert.Equal(t, "inactive", stateMust(t, c))

	calls := w.snapshot()
	require.Len(t, calls, 2, "false→true→false pulse")
	assert.Equal(t, watcherCall{false, true}, calls[0])
	assert.Equal(t, watcherCall{true, false}, calls[1])
}

func TestCounterRolloverPlusLatchHoldsActive(t *testing.T) {
	c := newTestCounter(3, 0, true, false, true)
	w := &capturingWatcher{}
	c.Watch(w)

	for i := 0; i < 3; i++ {
		_, _ = c.Increment(context.Background(), nil)
	}
	cnt, _ := c.Count(context.Background())
	assert.Equal(t, int64(0), cnt, "auto-reset still happens")
	assert.Equal(t, "active", stateMust(t, c), "latch holds across rollover")

	calls := w.snapshot()
	assert.Equal(t, []watcherCall{{false, true}}, calls,
		"only one transition fires; the auto-reset is suppressed by latch")
}

func TestCounterCountDownFromInitial(t *testing.T) {
	c := newTestCounter(0, 5, false, true, false)
	require.Equal(t, "inactive", stateMust(t, c))

	_, _ = c.Increment(context.Background(), []cty.Value{cty.NumberIntVal(-3)})
	assert.Equal(t, "inactive", stateMust(t, c))

	_, _ = c.Increment(context.Background(), []cty.Value{cty.NumberIntVal(-2)})
	assert.Equal(t, "active", stateMust(t, c), "reached preset (count == 0)")
}

func TestCounterResetReturnsToInactive(t *testing.T) {
	c := newTestCounter(3, 0, false, false, true)
	for i := 0; i < 3; i++ {
		_, _ = c.Increment(context.Background(), nil)
	}
	require.Equal(t, "active", stateMust(t, c))

	require.NoError(t, c.Reset(context.Background()))
	assert.Equal(t, "inactive", stateMust(t, c), "latch released, output cleared")
	cnt, _ := c.Count(context.Background())
	assert.Equal(t, int64(0), cnt)
}

func TestCounterStartAppliesInitialPreset(t *testing.T) {
	// Counter where initial already satisfies the activation comparison
	// (e.g. count_down with initial == preset).
	c := newTestCounter(0, 0, false, true, false)
	require.NoError(t, c.Start())
	assert.Equal(t, "active", stateMust(t, c), "initial value already at preset")
}

func TestCounterStartActiveLatched(t *testing.T) {
	c := buildConfig(t, counterStartActiveVCL)
	cond := c.CtyConditionMap["fault_count"].EncapsulatedValue().(*CounterCondition)
	assert.True(t, cond.sm.behavior.StartActive)

	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	assert.Equal(t, "active", stateMust(t, cond), "boot-latched despite count < preset")
	cnt, _ := cond.Count(context.Background())
	assert.Equal(t, int64(0), cnt)

	// Increment a few times — latched output holds regardless.
	for i := 0; i < 3; i++ {
		_, _ = cond.Increment(context.Background(), nil)
	}
	assert.Equal(t, "active", stateMust(t, cond))

	require.NoError(t, cond.Reset(context.Background()))
	assert.Equal(t, "inactive", stateMust(t, cond), "reset returns to inactive")
	cnt, _ = cond.Count(context.Background())
	assert.Equal(t, int64(0), cnt)
}

func TestCounterStartActiveUnlatchedReconciles(t *testing.T) {
	// Unlatched start_active + count (0) < preset (5): the applyDelta(0) in
	// Start() must reconcile the forced-active SM to inactive.
	c := buildConfig(t, counterStartActiveUnlatchedVCL)
	cond := c.CtyConditionMap["fault_count"].EncapsulatedValue().(*CounterCondition)

	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	assert.Equal(t, "inactive", stateMust(t, cond),
		"unlatched start_active yields to count reconcile")
}
