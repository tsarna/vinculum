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
	"go.uber.org/zap"
)

// TimerCondition is the `condition "timer"` subtype. It takes a boolean input
// (either supplied imperatively via set() or declared reactively via input=),
// applies optional debounce pre-filtering, and drives a StateMachine that
// owns the temporal semantics (activate_after / deactivate_after / timeout /
// cooldown / latch / invert / retentive).
type TimerCondition struct {
	name     string
	config   *cfg.Config
	sm       *StateMachine
	clock    Clock
	debounce time.Duration

	hasInputExpr bool
	inputExpr    *cfg.ReactiveExpr
	inhibitExpr  *cfg.ReactiveExpr
	hooks        *HookDispatcher

	mu          sync.Mutex
	rawInput    bool // most recent submitted value
	stableInput bool // post-debounce value currently forwarded to the SM
	debTimer    ClockTimer
}

// Get implements richcty.Gettable.
func (t *TimerCondition) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	return t.sm.Get(ctx, args)
}

// State implements richcty.Stateful.
func (t *TimerCondition) State(ctx context.Context) (string, error) { return t.sm.State(ctx) }

// Clear implements richcty.Clearable. In addition to resetting the state
// machine (cancelling pending state, releasing latch, discarding retentive
// accumulation), it cancels any in-flight debounce.
//
// When a declared input expression is currently truthy, the rising edge is
// re-asserted into the state machine so the condition re-activates (and, if
// latch is configured, re-latches): clear() releases the latch but does not
// silence an ongoing-true input. Debounce is bypassed — the signal has
// already proven stable through the debounce window. activate_after,
// cooldown, and inhibit gating still apply via the normal SetRawInput path.
func (t *TimerCondition) Clear(ctx context.Context) error {
	t.mu.Lock()
	if t.debTimer != nil {
		t.debTimer.Stop()
		t.debTimer = nil
	}
	t.rawInput = false
	t.stableInput = false
	t.mu.Unlock()
	if err := t.sm.Clear(ctx); err != nil {
		return err
	}
	if t.inputExpr == nil {
		return nil
	}
	v, diags := t.inputExpr.Eval()
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.Bool || !v.True() {
		return nil
	}
	t.mu.Lock()
	t.rawInput = true
	t.stableInput = true
	t.mu.Unlock()
	t.sm.SetRawInput(ctx, true)
	return nil
}

// Watch implements richcty.Watchable by forwarding to the state machine, whose
// watcher notifications already respect pending-state suppression and
// invert.
func (t *TimerCondition) Watch(w richcty.Watcher)   { t.sm.Watch(w) }
func (t *TimerCondition) Unwatch(w richcty.Watcher) { t.sm.Unwatch(w) }

// Set implements richcty.Settable. Rejects calls when a declarative input= was
// configured (per spec §Functions).
func (t *TimerCondition) Set(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if t.hasInputExpr {
		return cty.NilVal, fmt.Errorf("condition %q has declared input; set() is not permitted", t.name)
	}
	if len(args) < 1 {
		return cty.NilVal, fmt.Errorf("set(condition.%s): missing value argument", t.name)
	}
	v := args[0]
	if v.Type() != cty.Bool || v.IsNull() {
		return cty.NilVal, fmt.Errorf("set(condition.%s): value must be a boolean, got %s", t.name, v.Type().FriendlyName())
	}
	t.submitInput(ctx, v.True())
	return v, nil
}

// submitInput routes a raw boolean input through the debounce filter on its
// way to the state machine. ctx is forwarded to the state machine (and thus
// to watcher notifications) on direct transitions; asynchronous debounce
// firings use context.Background().
func (t *TimerCondition) submitInput(ctx context.Context, value bool) {
	t.mu.Lock()
	t.rawInput = value
	if t.debounce <= 0 {
		if value == t.stableInput {
			t.mu.Unlock()
			return
		}
		t.stableInput = value
		t.mu.Unlock()
		t.sm.SetRawInput(ctx, value)
		return
	}
	if value == t.stableInput {
		// Settled back to the stable value; cancel any in-flight edge.
		if t.debTimer != nil {
			t.debTimer.Stop()
			t.debTimer = nil
		}
		t.mu.Unlock()
		return
	}
	// Edge away from stable: (re)start the debounce timer.
	if t.debTimer != nil {
		t.debTimer.Stop()
	}
	t.debTimer = t.clock.AfterFunc(t.debounce, t.onDebounceFire)
	t.mu.Unlock()
}

// onDebounceFire runs when the debounce window elapses without the input
// flipping back. It commits the pending value as the new stable input.
func (t *TimerCondition) onDebounceFire() {
	t.mu.Lock()
	t.debTimer = nil
	if t.rawInput == t.stableInput {
		t.mu.Unlock()
		return
	}
	t.stableInput = t.rawInput
	v := t.stableInput
	t.mu.Unlock()
	t.sm.SetRawInput(context.Background(), v)
}

// Start is invoked from the config Startables phase. It wires up the
// reactive input and inhibit expressions; each subscribes to its Watchables
// and pushes initial values into the condition.
func (t *TimerCondition) Start() error {
	t.sm.Bootstrap()
	if t.sm.behavior.StartActive {
		// Mirror StateMachine.Bootstrap's rawInput pre-assertion at this
		// layer so a subsequent set(false) — or a declared input whose
		// initial value is false — registers as an edge in submitInput
		// rather than hitting the "value unchanged" early-out.
		t.mu.Lock()
		t.rawInput = true
		t.stableInput = true
		t.mu.Unlock()
	}
	if t.inhibitExpr != nil {
		if diags := t.inhibitExpr.Start(context.Background()); diags.HasErrors() {
			return fmt.Errorf("condition %q: inhibit: %s", t.name, diags.Error())
		}
	}
	if t.inputExpr != nil {
		if diags := t.inputExpr.Start(context.Background()); diags.HasErrors() {
			return fmt.Errorf("condition %q: input: %s", t.name, diags.Error())
		}
	}
	return nil
}

// PostStart fires the on_init hook after all Startables have bootstrapped.
// Implements config.PostStartable. Called by the config runtime exactly once,
// in Startables order, after the Startables phase completes. No-op when no
// hooks were configured (hooks is nil and the subtype was not added to
// PostStartables in that case, so this method should not be invoked — the
// nil-guard in FireInit makes it safe regardless).
func (t *TimerCondition) PostStart() error {
	t.hooks.FireInit(t.sm.Output())
	return nil
}

// Stop unsubscribes reactive expressions so the condition stops reacting to
// upstream changes during shutdown.
func (t *TimerCondition) Stop() error {
	if t.inputExpr != nil {
		t.inputExpr.Stop()
	}
	if t.inhibitExpr != nil {
		t.inhibitExpr.Stop()
	}
	return nil
}

// --- capsule type ---

var TimerConditionCapsuleType = cty.CapsuleWithOps("timer_condition",
	reflect.TypeOf((*TimerCondition)(nil)).Elem(), &cty.CapsuleOps{
		GoString:     func(v interface{}) string { return fmt.Sprintf("timer_condition(%p)", v) },
		TypeGoString: func(_ reflect.Type) string { return "TimerCondition" },
	})

func newTimerConditionCapsule(t *TimerCondition) cty.Value {
	return cty.CapsuleVal(TimerConditionCapsuleType, t)
}

// --- HCL decode ---

type timerBody struct {
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
	Input           hcl.Expression `hcl:"input,optional"`
	OnInit          hcl.Expression `hcl:"on_init,optional"`
	OnActivate      hcl.Expression `hcl:"on_activate,optional"`
	OnDeactivate    hcl.Expression `hcl:"on_deactivate,optional"`
}

func init() {
	cfg.RegisterConditionSubtype("timer", cfg.ConditionRegistration{
		Process:         processTimerCondition,
		HasDependencyId: true,
	})
}

func processTimerCondition(config *cfg.Config, block *hcl.Block, def *cfg.ConditionDefinition) hcl.Diagnostics {
	body := timerBody{}
	diags := gohcl.DecodeBody(def.RemainingBody, config.EvalCtx(), &body)
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
	t := &TimerCondition{
		name:         def.Name,
		config:       config,
		sm:           NewStateMachine(behavior, clock),
		clock:        clock,
		debounce:     debounce,
		hasInputExpr: cfg.IsExpressionProvided(body.Input),
	}

	if t.hasInputExpr {
		re, d := cfg.NewReactiveExpr(body.Input, config.EvalCtx(), func(ctx context.Context, v cty.Value) {
			if v.Type() != cty.Bool || v.IsNull() || !v.IsKnown() {
				config.UserLogger.Warn("condition input expression did not produce a boolean",
					zap.String("name", t.name), zap.String("type", v.Type().FriendlyName()))
				return
			}
			t.submitInput(ctx, v.True())
		})
		diags = diags.Extend(d)
		t.inputExpr = re
	}

	if cfg.IsExpressionProvided(body.Inhibit) {
		re, d := cfg.NewReactiveExpr(body.Inhibit, config.EvalCtx(), func(ctx context.Context, v cty.Value) {
			if v.Type() != cty.Bool || v.IsNull() || !v.IsKnown() {
				config.UserLogger.Warn("condition inhibit expression did not produce a boolean",
					zap.String("name", t.name), zap.String("type", v.Type().FriendlyName()))
				return
			}
			t.sm.SetInhibited(ctx, v.True())
		})
		diags = diags.Extend(d)
		t.inhibitExpr = re
	}
	if diags.HasErrors() {
		return diags
	}

	tp, _ := config.ResolveTracerProvider(hcl.Expression(nil))
	t.hooks = NewHookDispatcher(def.Name, Hooks{
		OnInit:       body.OnInit,
		OnActivate:   body.OnActivate,
		OnDeactivate: body.OnDeactivate,
	}, config, tp)
	if t.hooks != nil {
		t.sm.Watch(t.hooks)
	}

	config.CtyConditionMap[def.Name] = newTimerConditionCapsule(t)
	config.EvalCtx().Variables["condition"] = cfg.CtyObjectOrEmpty(config.CtyConditionMap)
	config.Startables = append(config.Startables, t)
	if t.hooks != nil {
		config.PostStartables = append(config.PostStartables, t)
	}
	config.Stoppables = append(config.Stoppables, t)
	return diags
}

// parseOptDuration parses an optional duration expression, returning zero
// when the expression was not provided in the source.
func parseOptDuration(config *cfg.Config, expr hcl.Expression) (time.Duration, hcl.Diagnostics) {
	if !cfg.IsExpressionProvided(expr) {
		return 0, nil
	}
	return config.ParseDuration(expr)
}
