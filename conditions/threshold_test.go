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

//go:embed testdata/threshold_high.vcl
var thresholdHighVCL []byte

//go:embed testdata/threshold_low.vcl
var thresholdLowVCL []byte

//go:embed testdata/threshold_bad_pair.vcl
var thresholdBadPairVCL []byte

//go:embed testdata/threshold_bad_order.vcl
var thresholdBadOrderVCL []byte

//go:embed testdata/threshold_start_active.vcl
var thresholdStartActiveVCL []byte

//go:embed testdata/threshold_start_active_unlatched.vcl
var thresholdStartActiveUnlatchedVCL []byte

func TestThresholdHighFormDecode(t *testing.T) {
	c := buildConfig(t, thresholdHighVCL)
	require.Contains(t, c.CtyConditionMap, "high_temp")
	cond := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*ThresholdCondition)
	assert.True(t, cond.highForm)
	assert.Equal(t, 80.0, cond.onThresh)
	assert.Equal(t, 70.0, cond.offThresh)
}

func TestThresholdLowFormDecode(t *testing.T) {
	c := buildConfig(t, thresholdLowVCL)
	cond := c.CtyConditionMap["low_battery"].EncapsulatedValue().(*ThresholdCondition)
	assert.False(t, cond.highForm)
	assert.Equal(t, 20.0, cond.onThresh)
	assert.Equal(t, 25.0, cond.offThresh)
	assert.True(t, cond.sm.behavior.Latch)
}

func TestThresholdBadPairRejected(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources(thresholdBadPairVCL).
		WithLogger(testLogger(t)).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "Conflicting threshold pair")
}

func TestThresholdBadOrderRejected(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources(thresholdBadOrderVCL).
		WithLogger(testLogger(t)).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "Invalid threshold ordering")
}

// --- hysteresis behavior (unit-level, no VCL) ---

func newTestThreshold(high bool, on, off float64) *ThresholdCondition {
	clock := newFakeClock()
	return &ThresholdCondition{
		name:      "t",
		sm:        NewStateMachine(Behavior{}, clock),
		clock:     clock,
		highForm:  high,
		onThresh:  on,
		offThresh: off,
	}
}

func TestThresholdHighHysteresis(t *testing.T) {
	c := newTestThreshold(true, 80, 70)

	// Value in deadband at startup → inactive.
	c.submitValue(context.Background(), 75)
	assert.Equal(t, "inactive", stateMust(t, c))

	// Rising through deadband, not yet above on threshold.
	c.submitValue(context.Background(), 80) // exactly on boundary: not above
	assert.Equal(t, "inactive", stateMust(t, c))

	// Cross above on threshold → active.
	c.submitValue(context.Background(), 81)
	assert.Equal(t, "active", stateMust(t, c))

	// Fall back into deadband → stays active.
	c.submitValue(context.Background(), 72)
	assert.Equal(t, "active", stateMust(t, c))

	// Fall below off threshold → inactive.
	c.submitValue(context.Background(), 69)
	assert.Equal(t, "inactive", stateMust(t, c))
}

func TestThresholdLowHysteresis(t *testing.T) {
	c := newTestThreshold(false, 20, 25) // on_below=20, off_above=25

	c.submitValue(context.Background(), 50)
	assert.Equal(t, "inactive", stateMust(t, c))

	c.submitValue(context.Background(), 22) // deadband
	assert.Equal(t, "inactive", stateMust(t, c))

	c.submitValue(context.Background(), 19)
	assert.Equal(t, "active", stateMust(t, c))

	c.submitValue(context.Background(), 24) // deadband, still active
	assert.Equal(t, "active", stateMust(t, c))

	c.submitValue(context.Background(), 26)
	assert.Equal(t, "inactive", stateMust(t, c))
}

func TestThresholdInitialValueInDeadbandStaysInactive(t *testing.T) {
	// Spec §Threshold State Model: "If the input value starts within the
	// hysteresis deadband at startup, the initial output state is inactive."
	c := newTestThreshold(true, 80, 70)
	c.submitValue(context.Background(), 75) // first sample, in deadband
	assert.Equal(t, "inactive", stateMust(t, c))
}

func TestThresholdDrivesReactivelyFromVar(t *testing.T) {
	c := buildConfig(t, thresholdHighVCL)
	cond := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*ThresholdCondition)

	// Start the config (wires reactive input).
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	// var.temp starts at 0 (well below off_below=70) → inactive.
	assert.Equal(t, "inactive", stateMust(t, cond))

	// Push the var up past on_above; threshold should go active via
	// reactive input re-evaluation.
	v := c.CtyVarMap["temp"].EncapsulatedValue().(interface {
		Set(context.Context, []cty.Value) (cty.Value, error)
	})
	_, err := v.Set(context.Background(), []cty.Value{cty.NumberIntVal(90)})
	require.NoError(t, err)
	assert.Equal(t, "active", stateMust(t, cond))

	// Back into the deadband; still active.
	_, err = v.Set(context.Background(), []cty.Value{cty.NumberIntVal(75)})
	require.NoError(t, err)
	assert.Equal(t, "active", stateMust(t, cond))

	// Below off_below; inactive.
	_, err = v.Set(context.Background(), []cty.Value{cty.NumberIntVal(65)})
	require.NoError(t, err)
	assert.Equal(t, "inactive", stateMust(t, cond))
}

func TestThresholdStartActiveLatchedInDeadband(t *testing.T) {
	// var.temp = 75, which is in the deadband [70, 80]. With start_active +
	// latch, the condition boots active and the first reactive sample in the
	// deadband must not deactivate it.
	c := buildConfig(t, thresholdStartActiveVCL)
	cond := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*ThresholdCondition)

	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	assert.Equal(t, "active", stateMust(t, cond), "boot-latched in deadband")

	// Drop value below off_below — latch holds.
	v := c.CtyVarMap["temp"].EncapsulatedValue().(interface {
		Set(context.Context, []cty.Value) (cty.Value, error)
	})
	_, err := v.Set(context.Background(), []cty.Value{cty.NumberIntVal(30)})
	require.NoError(t, err)
	assert.Equal(t, "active", stateMust(t, cond), "latch holds through below-threshold sample")

	require.NoError(t, cond.Clear(context.Background()))
	assert.Equal(t, "inactive", stateMust(t, cond))
}

func TestThresholdStartActiveUnlatchedReconciles(t *testing.T) {
	// var.temp = 50, below off_below=70. With start_active + no latch, the
	// first reactive sample must deactivate. Exactly one transition event
	// fires (the deactivation), not two.
	c := buildConfig(t, thresholdStartActiveUnlatchedVCL)
	cond := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*ThresholdCondition)
	w := &capturingWatcher{}
	cond.Watch(w)

	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	assert.Equal(t, "inactive", stateMust(t, cond), "unlatched boot-active reconciles to value")

	calls := w.snapshot()
	require.Len(t, calls, 1, "only the deactivation event fires")
	assert.Equal(t, watcherCall{true, false}, calls[0])
}
