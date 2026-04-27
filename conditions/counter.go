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
	clock     Clock
	initial   int64
	preset    int64
	rollover  bool
	countDown bool
	window    time.Duration // sliding window; > 0 enables windowed mode

	inhibitExpr *cfg.ReactiveExpr
	hooks       *HookDispatcher

	mu          sync.Mutex
	count       int64
	events      []time.Time // FIFO of in-window event timestamps; nil unless window > 0
	expiryTimer ClockTimer  // fires when the head of `events` ages out
}

func (c *CounterCondition) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	return c.sm.Get(ctx, args)
}
func (c *CounterCondition) State(ctx context.Context) (string, error) { return c.sm.State(ctx) }
func (c *CounterCondition) Watch(w richcty.Watcher)                   { c.sm.Watch(w) }
func (c *CounterCondition) Unwatch(w richcty.Watcher)                 { c.sm.Unwatch(w) }

// Count implements richcty.Countable: returns the current numeric count value.
// Distinct from Get(), which returns the boolean preset-reached output.
//
// In windowed mode, expired events are pruned opportunistically before the
// count is read so that a quiescent counter still reports an accurate value
// between expiry-timer firings.
func (c *CounterCondition) Count(_ context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window > 0 && len(c.events) > 0 {
		c.pruneLocked(c.clock.Now())
	}
	return c.count, nil
}

// Reset implements richcty.Resettable: count → initial, latch released, pending
// state cancelled, output → inactive. Spec §Functions. In windowed mode, the
// FIFO of in-window events is also discarded and the expiry timer cancelled.
func (c *CounterCondition) Reset(ctx context.Context) error {
	c.mu.Lock()
	c.count = c.initial
	c.events = nil
	if c.expiryTimer != nil {
		c.expiryTimer.Stop()
		c.expiryTimer = nil
	}
	c.mu.Unlock()
	return c.sm.Clear(ctx)
}

// Clear implements richcty.Clearable. For counters there is no `input =` to
// re-sample (counters reject input=), so clear() is equivalent to reset(): it
// empties the count (and the windowed FIFO), releases any latch, and returns
// the output to inactive.
func (c *CounterCondition) Clear(ctx context.Context) error {
	return c.Reset(ctx)
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
	if c.window > 0 {
		c.applyWindowedDelta(ctx, delta)
		return
	}
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

// applyWindowedDelta is the windowed-mode counterpart to applyDelta. Positive
// delta appends `delta` events at the current clock; negative delta pops
// `|delta|` oldest events from the FIFO. Expired events are pruned and the
// expiry timer is re-armed for the new head before the preset comparison is
// pushed to the SM. Window mode is incompatible with rollover and count_down
// (rejected at parse time), so this path stays straightforward.
func (c *CounterCondition) applyWindowedDelta(ctx context.Context, delta int64) {
	now := c.clock.Now()
	c.mu.Lock()
	c.pruneLocked(now)
	if delta > 0 {
		for i := int64(0); i < delta; i++ {
			c.events = append(c.events, now)
		}
	} else if delta < 0 {
		pop := -delta
		if pop > int64(len(c.events)) {
			pop = int64(len(c.events))
		}
		c.events = c.events[pop:]
	}
	c.count = int64(len(c.events))
	c.armExpiryLocked(now)
	matched := c.matchedLocked()
	c.mu.Unlock()
	c.sm.SetRawInput(ctx, matched)
}

// pruneLocked drops events older than `now - window` from the FIFO front.
// Caller holds c.mu. Updates c.count to reflect the new length. Releases the
// underlying array when the FIFO empties so a long quiet period after a burst
// doesn't pin a large backing slice. No-op when window is not configured.
func (c *CounterCondition) pruneLocked(now time.Time) {
	if c.window <= 0 {
		return
	}
	cutoff := now.Add(-c.window)
	drop := 0
	for drop < len(c.events) && !c.events[drop].After(cutoff) {
		drop++
	}
	if drop == 0 {
		return
	}
	if drop == len(c.events) {
		c.events = nil
	} else {
		c.events = c.events[drop:]
	}
	c.count = int64(len(c.events))
}

// armExpiryLocked (re)schedules the expiry timer for the next-to-expire event,
// or stops it if the FIFO is empty. Caller holds c.mu and must have just
// pruned, so events[0] (if present) is guaranteed to be still in-window.
func (c *CounterCondition) armExpiryLocked(now time.Time) {
	if c.expiryTimer != nil {
		c.expiryTimer.Stop()
		c.expiryTimer = nil
	}
	if len(c.events) == 0 {
		return
	}
	delay := c.events[0].Add(c.window).Sub(now)
	if delay < 0 {
		delay = 0
	}
	c.expiryTimer = c.clock.AfterFunc(delay, c.onWindowExpire)
}

// onWindowExpire fires when the head event ages out. It prunes, re-arms the
// timer for the new head, and pushes the new matched value to the SM so a
// quiescent counter correctly transitions to inactive when its last event
// drops out of the window.
func (c *CounterCondition) onWindowExpire() {
	now := c.clock.Now()
	c.mu.Lock()
	c.pruneLocked(now)
	c.armExpiryLocked(now)
	matched := c.matchedLocked()
	c.mu.Unlock()
	c.sm.SetRawInput(context.Background(), matched)
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
	c.sm.Bootstrap()
	if c.inhibitExpr != nil {
		if diags := c.inhibitExpr.Start(context.Background()); diags.HasErrors() {
			return fmt.Errorf("condition %q: inhibit: %s", c.name, diags.Error())
		}
	}
	// Apply the initial-value preset check at start so an initial count that
	// already satisfies the comparison is reflected in the SM. When start_active
	// is set alongside latch, the latched SM ignores a deasserted reconcile
	// (state.go's StateActive branch returns early on !value && latched); when
	// start_active is set without latch, the reconcile correctly overrides the
	// forced-active boot state with whatever the current count dictates.
	c.applyDelta(context.Background(), 0)
	return nil
}

// PostStart fires the on_init hook after all Startables have bootstrapped.
// Implements config.PostStartable.
func (c *CounterCondition) PostStart() error {
	c.hooks.FireInit(c.sm.Output())
	return nil
}

func (c *CounterCondition) Stop() error {
	c.mu.Lock()
	if c.expiryTimer != nil {
		c.expiryTimer.Stop()
		c.expiryTimer = nil
	}
	c.mu.Unlock()
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
	Window          hcl.Expression `hcl:"window,optional"`
	ActivateAfter   hcl.Expression `hcl:"activate_after,optional"`
	DeactivateAfter hcl.Expression `hcl:"deactivate_after,optional"`
	Timeout         hcl.Expression `hcl:"timeout,optional"`
	Cooldown        hcl.Expression `hcl:"cooldown,optional"`
	Latch           *bool          `hcl:"latch,optional"`
	Invert          *bool          `hcl:"invert,optional"`
	StartActive     *bool          `hcl:"start_active,optional"`
	Inhibit         hcl.Expression `hcl:"inhibit,optional"`
	OnInit          hcl.Expression `hcl:"on_init,optional"`
	OnActivate      hcl.Expression `hcl:"on_activate,optional"`
	OnDeactivate    hcl.Expression `hcl:"on_deactivate,optional"`

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
	window, moreDiags := parseOptDuration(config, body.Window)
	diags = diags.Extend(moreDiags)
	if body.Latch != nil {
		behavior.Latch = *body.Latch
	}
	if body.Invert != nil {
		behavior.Invert = *body.Invert
	}
	if body.StartActive != nil {
		behavior.StartActive = *body.StartActive
	}

	// Window is incompatible with rollover, count_down, and a non-zero
	// initial: there's no meaningful interpretation of "rollover snap-back",
	// "count down toward zero", or "synthetic baseline events" when the
	// count is derived from a FIFO of timestamped event arrivals.
	if window > 0 {
		if body.Rollover != nil && *body.Rollover {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Incompatible counter attributes",
				Detail:   "rollover cannot be combined with window",
				Subject:  &def.DefRange,
			})
		}
		if body.CountDown != nil && *body.CountDown {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Incompatible counter attributes",
				Detail:   "count_down cannot be combined with window",
				Subject:  &def.DefRange,
			})
		}
		if body.Initial != nil && *body.Initial != 0 {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Incompatible counter attributes",
				Detail:   "initial must be 0 (or omitted) when window is set; the count is the number of in-window events",
				Subject:  &def.DefRange,
			})
		}
	}
	if diags.HasErrors() {
		return diags
	}

	clock := RealClock{}
	c := &CounterCondition{
		name:    def.Name,
		config:  config,
		sm:      NewStateMachine(behavior, clock),
		clock:   clock,
		initial: 0,
		preset:  *body.Preset,
		window:  window,
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

	tp, _ := config.ResolveTracerProvider(hcl.Expression(nil))
	c.hooks = NewHookDispatcher(def.Name, Hooks{
		OnInit:       body.OnInit,
		OnActivate:   body.OnActivate,
		OnDeactivate: body.OnDeactivate,
	}, config, tp)
	if c.hooks != nil {
		c.sm.Watch(c.hooks)
	}

	config.CtyConditionMap[def.Name] = newCounterConditionCapsule(c)
	config.EvalCtx().Variables["condition"] = cfg.CtyObjectOrEmpty(config.CtyConditionMap)
	config.Startables = append(config.Startables, c)
	if c.hooks != nil {
		config.PostStartables = append(config.PostStartables, c)
	}
	config.Stoppables = append(config.Stoppables, c)
	return diags
}
