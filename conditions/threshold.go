package conditions

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	richcty "github.com/tsarna/rich-cty-types"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
	"go.uber.org/zap"
)

// ThresholdCondition is the `condition "threshold"` subtype. It derives a
// boolean from a numeric input using separate on/off thresholds (hysteresis)
// and drives the shared StateMachine with that derived boolean — so
// activate_after / deactivate_after / timeout / cooldown / latch / invert /
// retentive semantics carry over identically from timer. `input =` is
// required (numeric, reactive only); there is no imperative set().
type ThresholdCondition struct {
	name     string
	config   *cfg.Config
	sm       *StateMachine
	clock    Clock
	debounce time.Duration

	// Hysteresis configuration. When highForm is true, the condition
	// activates when input rises above onThresh and deactivates when it
	// falls below offThresh (onThresh > offThresh). When false, the
	// comparisons invert (onThresh < offThresh) for the low-threshold form.
	highForm  bool
	onThresh  float64
	offThresh float64

	inputExpr   *cfg.ReactiveExpr
	inhibitExpr *cfg.ReactiveExpr

	mu          sync.Mutex
	haveValue   bool
	derived     bool // current post-hysteresis boolean
	stableInput bool // post-debounce value forwarded to SM
	debTimer    ClockTimer
}

func (c *ThresholdCondition) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	return c.sm.Get(ctx, args)
}
func (c *ThresholdCondition) State(ctx context.Context) (string, error) { return c.sm.State(ctx) }
func (c *ThresholdCondition) Watch(w richcty.Watcher)                   { c.sm.Watch(w) }
func (c *ThresholdCondition) Unwatch(w richcty.Watcher)                 { c.sm.Unwatch(w) }

// Clear resets the hysteresis state machine and cancels any in-flight
// debounce. The initial-value rule applies anew: until the next numeric
// sample is observed, the condition reports inactive.
func (c *ThresholdCondition) Clear(ctx context.Context) error {
	c.mu.Lock()
	if c.debTimer != nil {
		c.debTimer.Stop()
		c.debTimer = nil
	}
	c.haveValue = false
	c.derived = false
	c.stableInput = false
	c.mu.Unlock()
	return c.sm.Clear(ctx)
}

// submitValue applies hysteresis to a new numeric sample and, on derived-
// boolean edges, forwards the result through the debounce filter to the
// state machine.
func (c *ThresholdCondition) submitValue(ctx context.Context, value float64) {
	c.mu.Lock()
	newDerived := c.deriveLocked(value)
	c.haveValue = true
	if newDerived == c.derived {
		c.mu.Unlock()
		return
	}
	c.derived = newDerived
	c.mu.Unlock()
	c.submitDerived(ctx, newDerived)
}

// deriveLocked applies hysteresis to value against the current derived state.
// Called with c.mu held.
func (c *ThresholdCondition) deriveLocked(value float64) bool {
	if c.highForm {
		if c.derived {
			// Currently above; deactivate when strictly below off threshold.
			if value < c.offThresh {
				return false
			}
			return true
		}
		// Currently below; activate when strictly above on threshold.
		if value > c.onThresh {
			return true
		}
		return false
	}
	// Low form: active when below on threshold; deactivate above off threshold.
	if c.derived {
		if value > c.offThresh {
			return false
		}
		return true
	}
	if value < c.onThresh {
		return true
	}
	return false
}

// submitDerived routes a derived boolean edge through the debounce filter to
// the state machine. Structurally identical to TimerCondition's debouncer.
func (c *ThresholdCondition) submitDerived(ctx context.Context, value bool) {
	c.mu.Lock()
	if c.debounce <= 0 {
		if value == c.stableInput {
			c.mu.Unlock()
			return
		}
		c.stableInput = value
		c.mu.Unlock()
		c.sm.SetRawInput(ctx, value)
		return
	}
	if value == c.stableInput {
		if c.debTimer != nil {
			c.debTimer.Stop()
			c.debTimer = nil
		}
		c.mu.Unlock()
		return
	}
	if c.debTimer != nil {
		c.debTimer.Stop()
	}
	c.debTimer = c.clock.AfterFunc(c.debounce, c.onDebounceFire)
	c.mu.Unlock()
}

func (c *ThresholdCondition) onDebounceFire() {
	c.mu.Lock()
	c.debTimer = nil
	if c.derived == c.stableInput {
		c.mu.Unlock()
		return
	}
	c.stableInput = c.derived
	v := c.stableInput
	c.mu.Unlock()
	c.sm.SetRawInput(context.Background(), v)
}

func (c *ThresholdCondition) Start() error {
	c.sm.Bootstrap()
	if c.sm.behavior.StartActive {
		// Keep the hysteresis baseline consistent with the forced-active SM
		// output so the first numeric sample doesn't submit a spurious rising
		// edge. derived=true means subsequent samples evaluate against the
		// "currently above" branch of deriveLocked (for high form) or "currently
		// below" (for low form); stableInput=true suppresses redundant
		// SetRawInput(true) calls on the first settle.
		c.mu.Lock()
		c.derived = true
		c.stableInput = true
		c.mu.Unlock()
	}
	if c.inhibitExpr != nil {
		if diags := c.inhibitExpr.Start(context.Background()); diags.HasErrors() {
			return fmt.Errorf("condition %q: inhibit: %s", c.name, diags.Error())
		}
	}
	if diags := c.inputExpr.Start(context.Background()); diags.HasErrors() {
		return fmt.Errorf("condition %q: input: %s", c.name, diags.Error())
	}
	return nil
}

func (c *ThresholdCondition) Stop() error {
	c.inputExpr.Stop()
	if c.inhibitExpr != nil {
		c.inhibitExpr.Stop()
	}
	return nil
}

// --- capsule type ---

var ThresholdConditionCapsuleType = cty.CapsuleWithOps("threshold_condition",
	reflect.TypeOf((*ThresholdCondition)(nil)).Elem(), &cty.CapsuleOps{
		GoString:     func(v interface{}) string { return fmt.Sprintf("threshold_condition(%p)", v) },
		TypeGoString: func(_ reflect.Type) string { return "ThresholdCondition" },
	})

func newThresholdConditionCapsule(c *ThresholdCondition) cty.Value {
	return cty.CapsuleVal(ThresholdConditionCapsuleType, c)
}

// --- HCL decode ---

type thresholdBody struct {
	Input           hcl.Expression `hcl:"input"`
	OnAbove         *float64       `hcl:"on_above,optional"`
	OffBelow        *float64       `hcl:"off_below,optional"`
	OnBelow         *float64       `hcl:"on_below,optional"`
	OffAbove        *float64       `hcl:"off_above,optional"`
	ActivateAfter   hcl.Expression `hcl:"activate_after,optional"`
	DeactivateAfter hcl.Expression `hcl:"deactivate_after,optional"`
	Timeout         hcl.Expression `hcl:"timeout,optional"`
	Cooldown        hcl.Expression `hcl:"cooldown,optional"`
	Latch           *bool          `hcl:"latch,optional"`
	Invert          *bool          `hcl:"invert,optional"`
	Retentive       *bool          `hcl:"retentive,optional"`
	StartActive     *bool          `hcl:"start_active,optional"`
	Inhibit         hcl.Expression `hcl:"inhibit,optional"`
	Debounce        hcl.Expression `hcl:"debounce,optional"`
}

func init() {
	cfg.RegisterConditionSubtype("threshold", cfg.ConditionRegistration{
		Process:         processThresholdCondition,
		HasDependencyId: true,
	})
}

func processThresholdCondition(config *cfg.Config, block *hcl.Block, def *cfg.ConditionDefinition) hcl.Diagnostics {
	body := thresholdBody{}
	diags := gohcl.DecodeBody(def.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	highForm := body.OnAbove != nil || body.OffBelow != nil
	lowForm := body.OnBelow != nil || body.OffAbove != nil
	if highForm && lowForm {
		return append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Conflicting threshold pair",
			Detail:   "on_above/off_below cannot be mixed with on_below/off_above in the same condition",
			Subject:  &def.DefRange,
		})
	}
	if !highForm && !lowForm {
		return append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Missing threshold pair",
			Detail:   "threshold condition requires either on_above+off_below or on_below+off_above",
			Subject:  &def.DefRange,
		})
	}

	var onT, offT float64
	if highForm {
		if body.OnAbove == nil || body.OffBelow == nil {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Incomplete high-threshold pair",
				Detail:   "high form requires both on_above and off_below",
				Subject:  &def.DefRange,
			})
		}
		onT, offT = *body.OnAbove, *body.OffBelow
		if !(onT > offT) {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid threshold ordering",
				Detail:   fmt.Sprintf("on_above (%v) must be greater than off_below (%v)", onT, offT),
				Subject:  &def.DefRange,
			})
		}
	} else {
		if body.OnBelow == nil || body.OffAbove == nil {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Incomplete low-threshold pair",
				Detail:   "low form requires both on_below and off_above",
				Subject:  &def.DefRange,
			})
		}
		onT, offT = *body.OnBelow, *body.OffAbove
		if !(offT > onT) {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid threshold ordering",
				Detail:   fmt.Sprintf("off_above (%v) must be greater than on_below (%v)", offT, onT),
				Subject:  &def.DefRange,
			})
		}
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
	if body.Retentive != nil {
		behavior.Retentive = *body.Retentive
	}
	if body.StartActive != nil {
		behavior.StartActive = *body.StartActive
	}
	debounce, moreDiags := parseOptDuration(config, body.Debounce)
	diags = diags.Extend(moreDiags)
	if diags.HasErrors() {
		return diags
	}

	clock := RealClock{}
	c := &ThresholdCondition{
		name:      def.Name,
		config:    config,
		sm:        NewStateMachine(behavior, clock),
		clock:     clock,
		debounce:  debounce,
		highForm:  highForm,
		onThresh:  onT,
		offThresh: offT,
	}

	re, d := cfg.NewReactiveExpr(body.Input, config.EvalCtx(), func(ctx context.Context, v cty.Value) {
		if v.IsNull() || !v.IsKnown() {
			return
		}
		f, err := ctyToFloat(v)
		if err != nil {
			config.UserLogger.Warn("condition threshold input is not numeric",
				zap.String("name", c.name), zap.String("type", v.Type().FriendlyName()))
			return
		}
		c.submitValue(ctx, f)
	})
	diags = diags.Extend(d)
	c.inputExpr = re

	if cfg.IsExpressionProvided(body.Inhibit) {
		re, d := cfg.NewReactiveExpr(body.Inhibit, config.EvalCtx(), func(ctx context.Context, v cty.Value) {
			if v.Type() != cty.Bool || v.IsNull() || !v.IsKnown() {
				config.UserLogger.Warn("condition inhibit expression did not produce a boolean",
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

	config.CtyConditionMap[def.Name] = newThresholdConditionCapsule(c)
	config.EvalCtx().Variables["condition"] = cfg.CtyObjectOrEmpty(config.CtyConditionMap)
	config.Startables = append(config.Startables, c)
	config.Stoppables = append(config.Stoppables, c)
	return diags
}

// ctyToFloat converts a numeric cty.Value (Number) to a float64, accepting
// integers as a subset.
func ctyToFloat(v cty.Value) (float64, error) {
	if v.Type() != cty.Number {
		return 0, fmt.Errorf("not numeric: %s", v.Type().FriendlyName())
	}
	var f float64
	if err := gocty.FromCtyValue(v, &f); err != nil {
		return 0, err
	}
	return f, nil
}
