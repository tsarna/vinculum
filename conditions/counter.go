package conditions

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	richcty "github.com/tsarna/rich-cty-types"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
	"go.uber.org/zap"
)

// CounterCondition is the `condition "counter"` subtype. It tracks a running
// integer count via increment()/decrement() and produces a boolean output via
// the shared StateMachine when the count reaches a configured preset.
//
// Counter does not support input=, debounce, or retentive (per spec
// §Attribute Applicability) — those are rejected at parse time. The shared
// behavioral attributes (activate_after / deactivate_after / timeout / latch
// / invert / cooldown / inhibit) are honored.
type CounterCondition struct {
	name      string
	config    *cfg.Config
	sm        *StateMachine
	initial   int64
	preset    int64
	rollover  bool
	countDown bool

	inhibitExpr *cfg.ReactiveExpr

	mu    sync.Mutex
	count int64
}

func (c *CounterCondition) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	return c.sm.Get(ctx, args)
}
func (c *CounterCondition) State(ctx context.Context) (string, error) { return c.sm.State(ctx) }
func (c *CounterCondition) Watch(w richcty.Watcher)                   { c.sm.Watch(w) }
func (c *CounterCondition) Unwatch(w richcty.Watcher)                 { c.sm.Unwatch(w) }

// Count implements richcty.Countable: returns the current numeric count value.
// Distinct from Get(), which returns the boolean preset-reached output.
func (c *CounterCondition) Count(_ context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, nil
}

// Reset implements richcty.Resettable: count → initial, latch released, pending
// state cancelled, output → inactive. Spec §Functions.
func (c *CounterCondition) Reset(ctx context.Context) error {
	c.mu.Lock()
	c.count = c.initial
	c.mu.Unlock()
	return c.sm.Clear(ctx)
}

// Increment implements richcty.Incrementable. Adds args[0] (default 1) to the
// count, applies low-side clamp at 0 (per spec — applies to decrement under
// rollover=false; we apply uniformly since a bare increment with negative
// delta is decrement), then re-evaluates the preset comparison and drives
// the state machine. With rollover=true, reaching the preset auto-resets
// the count to initial; under latch=true the latched output rides through
// the auto-reset so no spurious deactivate edge fires.
func (c *CounterCondition) Increment(ctx context.Context, args []cty.Value) (cty.Value, error) {
	delta := int64(1)
	if len(args) > 0 {
		if err := gocty.FromCtyValue(args[0], &delta); err != nil {
			return cty.NilVal, fmt.Errorf("increment(condition.%s): %w", c.name, err)
		}
	}
	c.applyDelta(ctx, delta)
	cur, _ := c.Count(ctx)
	return cty.NumberIntVal(cur), nil
}

func (c *CounterCondition) applyDelta(ctx context.Context, delta int64) {
	c.mu.Lock()
	c.count += delta
	if c.count < 0 {
		c.count = 0
	}
	matched := c.matchedLocked()
	c.mu.Unlock()

	if !matched {
		c.sm.SetRawInput(ctx, false)
		return
	}

	c.sm.SetRawInput(ctx, true)
	if !c.rollover {
		return
	}
	// Rollover: snap count back to initial. If the post-reset count no
	// longer satisfies the preset comparison, push false to emit the pulse
	// edge (suppressed by latch inside the state machine when configured).
	c.mu.Lock()
	c.count = c.initial
	stillMatched := c.matchedLocked()
	c.mu.Unlock()
	if !stillMatched {
		c.sm.SetRawInput(ctx, false)
	}
}

// matchedLocked returns whether the current count satisfies the preset
// activation comparison. Called with c.mu held.
func (c *CounterCondition) matchedLocked() bool {
	if c.countDown {
		return c.count <= c.preset
	}
	return c.count >= c.preset
}

func (c *CounterCondition) Start() error {
	if c.inhibitExpr != nil {
		if diags := c.inhibitExpr.Start(context.Background()); diags.HasErrors() {
			return fmt.Errorf("condition %q: inhibit: %s", c.name, diags.Error())
		}
	}
	// Apply the initial-value preset check at start so an initial count that
	// already satisfies the comparison is reflected in the SM.
	c.applyDelta(context.Background(), 0)
	return nil
}

func (c *CounterCondition) Stop() error {
	if c.inhibitExpr != nil {
		c.inhibitExpr.Stop()
	}
	return nil
}

// --- capsule type ---

var CounterConditionCapsuleType = cty.CapsuleWithOps("counter_condition",
	reflect.TypeOf((*CounterCondition)(nil)).Elem(), &cty.CapsuleOps{
		GoString:     func(v interface{}) string { return fmt.Sprintf("counter_condition(%p)", v) },
		TypeGoString: func(_ reflect.Type) string { return "CounterCondition" },
	})

func newCounterConditionCapsule(c *CounterCondition) cty.Value {
	return cty.CapsuleVal(CounterConditionCapsuleType, c)
}

// --- HCL decode ---

type counterBody struct {
	Preset          *int64         `hcl:"preset"`
	Initial         *int64         `hcl:"initial,optional"`
	Rollover        *bool          `hcl:"rollover,optional"`
	CountDown       *bool          `hcl:"count_down,optional"`
	ActivateAfter   hcl.Expression `hcl:"activate_after,optional"`
	DeactivateAfter hcl.Expression `hcl:"deactivate_after,optional"`
	Timeout         hcl.Expression `hcl:"timeout,optional"`
	Cooldown        hcl.Expression `hcl:"cooldown,optional"`
	Latch           *bool          `hcl:"latch,optional"`
	Invert          *bool          `hcl:"invert,optional"`
	Inhibit         hcl.Expression `hcl:"inhibit,optional"`

	// Forbidden — declared so we can produce a friendly diagnostic instead
	// of HCL's generic "argument not expected" message.
	Input     hcl.Expression `hcl:"input,optional"`
	Debounce  hcl.Expression `hcl:"debounce,optional"`
	Retentive *bool          `hcl:"retentive,optional"`
}

func init() {
	cfg.RegisterConditionSubtype("counter", cfg.ConditionRegistration{
		Process:         processCounterCondition,
		HasDependencyId: true,
	})
}

func processCounterCondition(config *cfg.Config, block *hcl.Block, def *cfg.ConditionDefinition) hcl.Diagnostics {
	body := counterBody{}
	diags := gohcl.DecodeBody(def.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	// Reject attributes that don't apply to counter (spec §Counter Attribute Applicability).
	for name, present := range map[string]bool{
		"input":     cfg.IsExpressionProvided(body.Input),
		"debounce":  cfg.IsExpressionProvided(body.Debounce),
		"retentive": body.Retentive != nil,
	} {
		if present {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Attribute not valid on counter condition",
				Detail:   fmt.Sprintf("counter conditions do not support %q (spec §Counter Attribute Applicability)", name),
				Subject:  &def.DefRange,
			})
		}
	}

	if body.Preset == nil {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Missing required attribute",
			Detail:   "counter conditions require preset",
			Subject:  &def.DefRange,
		})
	}
	if diags.HasErrors() {
		return diags
	}

	var moreDiags hcl.Diagnostics
	behavior := Behavior{Inhibit: body.Inhibit}
	behavior.ActivateAfter, moreDiags = parseOptDuration(config, body.ActivateAfter)
	diags = diags.Extend(moreDiags)
	behavior.DeactivateAfter, moreDiags = parseOptDuration(config, body.DeactivateAfter)
	diags = diags.Extend(moreDiags)
	behavior.Timeout, moreDiags = parseOptDuration(config, body.Timeout)
	diags = diags.Extend(moreDiags)
	behavior.Cooldown, moreDiags = parseOptDuration(config, body.Cooldown)
	diags = diags.Extend(moreDiags)
	if body.Latch != nil {
		behavior.Latch = *body.Latch
	}
	if body.Invert != nil {
		behavior.Invert = *body.Invert
	}
	if diags.HasErrors() {
		return diags
	}

	clock := RealClock{}
	c := &CounterCondition{
		name:    def.Name,
		config:  config,
		sm:      NewStateMachine(behavior, clock),
		initial: 0,
		preset:  *body.Preset,
	}
	if body.Initial != nil {
		c.initial = *body.Initial
		c.count = *body.Initial
	}
	if body.Rollover != nil {
		c.rollover = *body.Rollover
	}
	if body.CountDown != nil {
		c.countDown = *body.CountDown
	}

	if cfg.IsExpressionProvided(body.Inhibit) {
		re, d := cfg.NewReactiveExpr(body.Inhibit, config.EvalCtx(), func(ctx context.Context, v cty.Value) {
			if v.Type() != cty.Bool || v.IsNull() || !v.IsKnown() {
				config.Logger.Warn("condition inhibit expression did not produce a boolean",
					zap.String("name", c.name), zap.String("type", v.Type().FriendlyName()))
				return
			}
			c.sm.SetInhibited(ctx, v.True())
		})
		diags = diags.Extend(d)
		c.inhibitExpr = re
	}
	if diags.HasErrors() {
		return diags
	}

	config.CtyConditionMap[def.Name] = newCounterConditionCapsule(c)
	config.EvalCtx().Variables["condition"] = cfg.CtyObjectOrEmpty(config.CtyConditionMap)
	config.Startables = append(config.Startables, c)
	config.Stoppables = append(config.Stoppables, c)
	return diags
}
