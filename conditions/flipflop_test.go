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
)

//go:embed testdata/flipflop_behaviors.vcl
var flipflopBehaviorsVCL []byte

//go:embed testdata/flipflop_baseline.vcl
var flipflopBaselineVCL []byte

//go:embed testdata/flipflop_start_active.vcl
var flipflopStartActiveVCL []byte

//go:embed testdata/flipflop_latch.vcl
var flipflopLatchVCL []byte

//go:embed testdata/flipflop_invert.vcl
var flipflopInvertVCL []byte

//go:embed testdata/flipflop_cooldown.vcl
var flipflopCooldownVCL []byte

//go:embed testdata/flipflop_inhibit.vcl
var flipflopInhibitVCL []byte

//go:embed testdata/flipflop_hooks.vcl
var flipflopHooksVCL []byte

//go:embed testdata/flipflop_no_wire.vcl
var flipflopNoWireVCL []byte

//go:embed testdata/flipflop_setfrom_no_gate.vcl
var flipflopSetfromNoGateVCL []byte

//go:embed testdata/flipflop_gate_alone.vcl
var flipflopGateAloneVCL []byte

//go:embed testdata/flipflop_bad_edge.vcl
var flipflopBadEdgeVCL []byte

//go:embed testdata/flipflop_bad_dominant.vcl
var flipflopBadDominantVCL []byte

//go:embed testdata/flipflop_unsupported_attr.vcl
var flipflopUnsupportedAttrVCL []byte

// --- helpers ---

func startFlipflops(t *testing.T, c *cfg.Config) {
	t.Helper()
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	for _, p := range c.PostStartables {
		require.NoError(t, p.PostStart())
	}
}

func stopFlipflops(c *cfg.Config) {
	for i := len(c.Stoppables) - 1; i >= 0; i-- {
		_ = c.Stoppables[i].Stop()
	}
}

// setVar drives a `var` block (a Settable + Watchable) to the given value,
// synchronously notifying any flipflop subscribed to it.
func setVar(t *testing.T, c *cfg.Config, name string, val cty.Value) {
	t.Helper()
	capsule, ok := c.CtyVarMap[name]
	require.True(t, ok, "var %q not found", name)
	s, ok := capsule.EncapsulatedValue().(interface {
		Set(context.Context, []cty.Value) (cty.Value, error)
	})
	require.True(t, ok, "var %q must be Settable", name)
	_, err := s.Set(context.Background(), []cty.Value{val})
	require.NoError(t, err)
}

func pulse(t *testing.T, c *cfg.Config, name string) {
	t.Helper()
	setVar(t, c, name, cty.True)
	setVar(t, c, name, cty.False)
}

func flipflop(t *testing.T, c *cfg.Config, name string) *FlipflopCondition {
	t.Helper()
	v, ok := c.CtyConditionMap[name]
	require.True(t, ok, "condition %q not found", name)
	require.Equal(t, FlipflopConditionCapsuleType, v.Type())
	return v.EncapsulatedValue().(*FlipflopCondition)
}

func varBool(t *testing.T, c *cfg.Config, name string) bool {
	t.Helper()
	capsule, ok := c.CtyVarMap[name]
	require.True(t, ok, "var %q not found", name)
	g := capsule.EncapsulatedValue().(interface {
		Get(context.Context, []cty.Value) (cty.Value, error)
	})
	v, err := g.Get(context.Background(), nil)
	require.NoError(t, err)
	return !v.IsNull() && v.True()
}

// --- decode / registration ---

func TestFlipflopDecodeAndExpose(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	for _, name := range []string{
		"lamp", "fault", "sr_reset_dom", "sr_set_dom", "mode",
		"captured", "tracking", "controlled",
	} {
		require.Contains(t, c.CtyConditionMap, name)
		assert.Equal(t, FlipflopConditionCapsuleType, c.CtyConditionMap[name].Type())
	}
	// condition.<name> is exposed in the eval context.
	conditionVar, ok := c.EvalCtx().Variables["condition"]
	require.True(t, ok)
	assert.Equal(t, FlipflopConditionCapsuleType, conditionVar.GetAttr("lamp").Type())

	// dominant decoded correctly.
	assert.False(t, flipflop(t, c, "sr_reset_dom").dominantSet)
	assert.True(t, flipflop(t, c, "sr_set_dom").dominantSet)
	// gate edge modes decoded.
	assert.Equal(t, edgeRising, flipflop(t, c, "captured").gateWire.edge)
	assert.Equal(t, levelHigh, flipflop(t, c, "tracking").gateWire.edge)
}

// --- T flip-flop ---

func TestFlipflopT(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	lamp := flipflop(t, c, "lamp")

	require.Equal(t, "inactive", stateMust(t, lamp))
	pulse(t, c, "t_btn") // rising edge toggles on
	assert.Equal(t, "active", stateMust(t, lamp))
	pulse(t, c, "t_btn") // rising edge toggles off
	assert.Equal(t, "inactive", stateMust(t, lamp))

	// Falling edges do not toggle (default toggle_edge = "rising").
	setVar(t, c, "t_btn", cty.True)
	assert.Equal(t, "active", stateMust(t, lamp))
	setVar(t, c, "t_btn", cty.False) // falling — ignored
	assert.Equal(t, "active", stateMust(t, lamp))
}

// --- SR flip-flop ---

func TestFlipflopSR(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	fault := flipflop(t, c, "fault")

	require.Equal(t, "inactive", stateMust(t, fault))
	pulse(t, c, "sr_set")
	assert.Equal(t, "active", stateMust(t, fault))
	pulse(t, c, "sr_set") // already set — stays active
	assert.Equal(t, "active", stateMust(t, fault))
	pulse(t, c, "sr_reset")
	assert.Equal(t, "inactive", stateMust(t, fault))
}

// --- SR dominance on a shared source (atomic, single notification) ---

func TestFlipflopDominance(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	resetDom := flipflop(t, c, "sr_reset_dom")
	setDom := flipflop(t, c, "sr_set_dom")

	// Pre-activate the reset-dominant one imperatively so reset has something
	// to win against.
	_, err := resetDom.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	require.Equal(t, "active", stateMust(t, resetDom))

	// One rising edge on the shared source fires set AND reset in the same
	// notification. reset-dominant resolves to inactive; set-dominant to active.
	pulse(t, c, "pulse")
	assert.Equal(t, "inactive", stateMust(t, resetDom), "reset dominates")
	assert.Equal(t, "active", stateMust(t, setDom), "set dominates")
}

// --- JK flip-flop ---

func TestFlipflopJK(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	mode := flipflop(t, c, "mode")

	pulse(t, c, "jk_set")
	assert.Equal(t, "active", stateMust(t, mode))
	pulse(t, c, "jk_reset")
	assert.Equal(t, "inactive", stateMust(t, mode))
	pulse(t, c, "jk_tog")
	assert.Equal(t, "active", stateMust(t, mode), "toggle from inactive")
	pulse(t, c, "jk_tog")
	assert.Equal(t, "inactive", stateMust(t, mode), "toggle from active")
}

// --- D flip-flop (edge-triggered sample) ---

func TestFlipflopDFlipflop(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	captured := flipflop(t, c, "captured")

	// Data high, then a clock rising edge samples it.
	setVar(t, c, "d_data", cty.True)
	assert.Equal(t, "inactive", stateMust(t, captured), "no sample without a clock edge")
	pulse(t, c, "d_clk")
	assert.Equal(t, "active", stateMust(t, captured), "clock edge samples data=true")

	// Data goes low without a clock edge — output holds.
	setVar(t, c, "d_data", cty.False)
	assert.Equal(t, "active", stateMust(t, captured), "no clock edge, holds")
	pulse(t, c, "d_clk")
	assert.Equal(t, "inactive", stateMust(t, captured), "clock edge samples data=false")
}

// --- D latch (level-sensitive) ---

func TestFlipflopDLatch(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	tracking := flipflop(t, c, "tracking")

	// While enable is low, data changes are absorbed.
	setVar(t, c, "l_data", cty.True)
	assert.Equal(t, "inactive", stateMust(t, tracking), "enable low, absorbed")

	// Enable high: output tracks data reactively.
	setVar(t, c, "l_en", cty.True)
	assert.Equal(t, "active", stateMust(t, tracking), "captures data on enable")
	setVar(t, c, "l_data", cty.False)
	assert.Equal(t, "inactive", stateMust(t, tracking), "tracks data while enabled")
	setVar(t, c, "l_data", cty.True)
	assert.Equal(t, "active", stateMust(t, tracking))

	// Enable low: holds the last value; data changes are absorbed.
	setVar(t, c, "l_en", cty.False)
	setVar(t, c, "l_data", cty.False)
	assert.Equal(t, "active", stateMust(t, tracking), "holds last value while disabled")
}

// --- Gated SR ---

func TestFlipflopGatedSR(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	controlled := flipflop(t, c, "controlled")

	// Set edge while disabled is ignored.
	pulse(t, c, "gs_set")
	assert.Equal(t, "inactive", stateMust(t, controlled), "set ignored while gate low")

	// Enable, then a set edge takes effect.
	setVar(t, c, "gs_en", cty.True)
	pulse(t, c, "gs_set")
	assert.Equal(t, "active", stateMust(t, controlled), "set honored while gate high")

	// Reset edge while disabled is ignored.
	setVar(t, c, "gs_en", cty.False)
	pulse(t, c, "gs_reset")
	assert.Equal(t, "active", stateMust(t, controlled), "reset ignored while gate low")
}

// --- baseline: no boot trigger ---

func TestFlipflopBaselineNoBootTrigger(t *testing.T) {
	c := buildConfig(t, flipflopBaselineVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	lamp := flipflop(t, c, "lamp")

	// boot_high was true at Start; the baseline must not toggle the flipflop.
	assert.Equal(t, "inactive", stateMust(t, lamp), "source high at boot must not fire")

	// A genuine falling then rising edge now toggles.
	setVar(t, c, "boot_high", cty.False)
	assert.Equal(t, "inactive", stateMust(t, lamp), "falling ignored (rising edge)")
	setVar(t, c, "boot_high", cty.True)
	assert.Equal(t, "active", stateMust(t, lamp), "first real rising edge toggles")
}

// --- start_active + on_init / on_activate ---

func TestFlipflopStartActive(t *testing.T) {
	c := buildConfig(t, flipflopStartActiveVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	armed := flipflop(t, c, "armed")

	v, err := armed.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, v.True(), "start_active boots output true")
	assert.Equal(t, "active", stateMust(t, armed))

	assert.True(t, varBool(t, c, "sa_init_new"), "on_init sees new_value=true")
	assert.False(t, varBool(t, c, "sa_activated"), "no synthetic on_activate at boot")

	// A reset edge deactivates.
	pulse(t, c, "sa_reset")
	assert.Equal(t, "inactive", stateMust(t, armed))
}

// --- latch ---

func TestFlipflopLatch(t *testing.T) {
	c := buildConfig(t, flipflopLatchVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	sticky := flipflop(t, c, "sticky")

	pulse(t, c, "lt_set")
	require.Equal(t, "active", stateMust(t, sticky))

	// Reset and toggle-down are ignored while latched.
	pulse(t, c, "lt_reset")
	assert.Equal(t, "active", stateMust(t, sticky), "latched: reset ignored")
	pulse(t, c, "lt_tog")
	assert.Equal(t, "active", stateMust(t, sticky), "latched: toggle-down ignored")

	// clear() releases the latch.
	require.NoError(t, sticky.Clear(context.Background()))
	assert.Equal(t, "inactive", stateMust(t, sticky), "clear releases latch")
	pulse(t, c, "lt_set")
	assert.Equal(t, "active", stateMust(t, sticky), "re-latches after clear")
}

// --- invert ---

func TestFlipflopInvert(t *testing.T) {
	c := buildConfig(t, flipflopInvertVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	inv := flipflop(t, c, "inv")

	// start_active makes the internal state active, but invert flips get().
	assert.Equal(t, "active", stateMust(t, inv))
	v, err := inv.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, v.True(), "start_active + invert => get() false")

	// Toggle the internal state off; inverted output becomes true.
	pulse(t, c, "iv_tog")
	assert.Equal(t, "inactive", stateMust(t, inv))
	v, err = inv.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, v.True())
}

// --- cooldown (drives the state machine's clock via a fake) ---

func TestFlipflopCooldown(t *testing.T) {
	c := buildConfig(t, flipflopCooldownVCL)
	cooled := flipflop(t, c, "cooled")
	fc := newFakeClock()
	cooled.sm.clock = fc // swap before Start; Bootstrap schedules nothing here

	startFlipflops(t, c)
	defer stopFlipflops(c)

	pulse(t, c, "cd_set")
	require.Equal(t, "active", stateMust(t, cooled))
	pulse(t, c, "cd_reset")
	require.Equal(t, "inactive", stateMust(t, cooled))

	// Within the cooldown window a new set edge is suppressed.
	pulse(t, c, "cd_set")
	assert.Equal(t, "inactive", stateMust(t, cooled), "activation suppressed during cooldown")

	// After the cooldown elapses, a fresh set edge activates again.
	fc.Advance(6 * time.Minute)
	pulse(t, c, "cd_set")
	assert.Equal(t, "active", stateMust(t, cooled), "activation allowed after cooldown")
}

// --- inhibit ---

func TestFlipflopInhibit(t *testing.T) {
	c := buildConfig(t, flipflopInhibitVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	guarded := flipflop(t, c, "guarded")

	// While inhibited, a set edge cannot activate.
	setVar(t, c, "ih_block", cty.True)
	pulse(t, c, "ih_set")
	assert.Equal(t, "inactive", stateMust(t, guarded), "inhibited: activation suppressed")

	// Clearing inhibit with the desired output still asserted activates.
	setVar(t, c, "ih_block", cty.False)
	assert.Equal(t, "active", stateMust(t, guarded), "activation resumes when inhibit clears")

	// An already-active flipflop is unaffected by inhibit.
	setVar(t, c, "ih_block", cty.True)
	assert.Equal(t, "active", stateMust(t, guarded), "active state unaffected by inhibit")
}

// --- imperative set / clear / toggle coexist with wires ---

func TestFlipflopImperative(t *testing.T) {
	c := buildConfig(t, flipflopBehaviorsVCL)
	startFlipflops(t, c)
	defer stopFlipflops(c)
	fault := flipflop(t, c, "fault")

	out, err := fault.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	assert.True(t, out.True())
	assert.Equal(t, "active", stateMust(t, fault))

	out, err = fault.Toggle(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, out.True())
	assert.Equal(t, "inactive", stateMust(t, fault))

	// A reset edge still works after imperative use.
	pulse(t, c, "sr_set")
	assert.Equal(t, "active", stateMust(t, fault))
	require.NoError(t, fault.Clear(context.Background()))
	assert.Equal(t, "inactive", stateMust(t, fault))

	// Bad imperative input is rejected.
	_, err = fault.Set(context.Background(), []cty.Value{cty.StringVal("nope")})
	assert.Error(t, err)
}

// --- hooks ---

func TestFlipflopHooks(t *testing.T) {
	c := buildConfig(t, flipflopHooksVCL)
	hooked := flipflop(t, c, "hooked")

	// on_init fires only at PostStart.
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	assert.False(t, varBool(t, c, "hk_init"), "on_init not fired before PostStart")
	for _, p := range c.PostStartables {
		require.NoError(t, p.PostStart())
	}
	defer stopFlipflops(c)
	assert.True(t, varBool(t, c, "hk_init"), "on_init fired at PostStart")
	_ = hooked

	assert.False(t, varBool(t, c, "hk_act"))
	pulse(t, c, "hk_set")
	assert.True(t, varBool(t, c, "hk_act"), "on_activate fired on set edge")

	assert.False(t, varBool(t, c, "hk_deact"))
	pulse(t, c, "hk_reset")
	assert.True(t, varBool(t, c, "hk_deact"), "on_deactivate fired on reset edge")
}

// --- validation errors ---

func TestFlipflopValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		src  []byte
		want string
	}{
		{"no wire", flipflopNoWireVCL, "no driven wire"},
		{"set_from without gate", flipflopSetfromNoGateVCL, "set_from requires gate_on"},
		{"gate alone", flipflopGateAloneVCL, "Gate has nothing to gate"},
		{"bad edge", flipflopBadEdgeVCL, "Invalid edge mode"},
		{"bad dominant", flipflopBadDominantVCL, "Invalid dominant"},
		{"unsupported attr", flipflopUnsupportedAttrVCL, "Unsupported argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := cfg.NewConfig().WithSources(tc.src).WithLogger(testLogger(t)).Build()
			require.True(t, diags.HasErrors(), "expected an error")
			assert.Contains(t, diags.Error(), tc.want)
		})
	}
}
