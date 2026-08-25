package watch

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	richcty "github.com/tsarna/rich-cty-types"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// WatchTrigger fires its action expression each time a Watchable value changes.
// It implements Watcher, Gettable, Startable, and Stoppable.
type WatchTrigger struct {
	name           string
	config         *cfg.Config
	watchable      richcty.Watchable
	actionExpr     hcl.Expression
	skipWhenExpr   hcl.Expression // nil if not provided
	tracerProvider trace.TracerProvider

	mu        sync.RWMutex
	lastValue cty.Value // last observed newValue; cty.NilVal until first change

	stopped atomic.Bool
	wg      sync.WaitGroup
}

// OnChange implements Watcher. It stores the new value and dispatches action
// evaluation to a goroutine so as not to block the Set() caller.
func (t *WatchTrigger) OnChange(ctx context.Context, _ richcty.Watchable, oldValue, newValue cty.Value) {
	if t.stopped.Load() {
		return
	}

	t.mu.Lock()
	t.lastValue = newValue
	t.mu.Unlock()

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.dispatch(ctx, oldValue, newValue)
	}()
}

func (t *WatchTrigger) dispatch(ctx context.Context, oldValue, newValue cty.Value) {
	// The action runs in a goroutine that outlives the caller of Set() on
	// the watched value. Use a linked-root span + WithoutCancel so a
	// short-lived caller ctx (e.g. an HTTP request that completes before
	// the action finishes) doesn't cancel the action mid-flight.
	ctx, stopSpan := hclutil.StartLinkedTriggerSpan(ctx, t.tracerProvider, "watch", t.name)

	evalCtx, err := hclutil.NewEvalContext(ctx).
		WithStringAttribute("trigger", "watch").
		WithStringAttribute("name", t.name).
		WithAttribute("old_value", oldValue).
		WithAttribute("new_value", newValue).
		BuildEvalContext(t.config.EvalCtx())
	if err != nil {
		t.config.UserLogger.Error("watch trigger: error building eval context",
			zap.String("name", t.name), zap.Error(err))
		stopSpan(err)
		return
	}

	if t.skipWhenExpr != nil {
		skipVal, diags := t.skipWhenExpr.Value(evalCtx)
		if diags.HasErrors() {
			t.config.UserLogger.Error("watch trigger: skip_when error",
				zap.String("name", t.name), zap.Error(diags))
			stopSpan(diags)
			return
		}
		if skipVal.Type() == cty.Bool && skipVal.True() {
			stopSpan(nil)
			return
		}
	}

	val, diags := t.actionExpr.Value(evalCtx)
	if diags.HasErrors() {
		t.config.UserLogger.Error("watch trigger: action error",
			zap.String("name", t.name), t.config.ActionError(diags))
		stopSpan(diags)
		return
	}
	stopSpan(nil)
	t.config.Logger.Debug("watch trigger: action completed",
		zap.String("name", t.name), zap.Any("result", val))
}

// Get returns the most recently observed value, or null if no change has been
// observed yet. Implements Gettable.
func (t *WatchTrigger) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastValue == cty.NilVal {
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return t.lastValue, nil
}

// Start registers this trigger as a Watcher on the target Watchable.
// Implements Startable.
func (t *WatchTrigger) Start() error {
	t.watchable.Watch(t)
	return nil
}

// Stop unregisters from the Watchable and waits for any in-flight action
// goroutines to complete. Implements Stoppable.
func (t *WatchTrigger) Stop() error {
	t.stopped.Store(true)
	t.watchable.Unwatch(t)
	t.wg.Wait()
	return nil
}

// --- Capsule type ---

var WatchTriggerCapsuleType = cty.CapsuleWithOps("watch_trigger", reflect.TypeOf((*WatchTrigger)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("watch_trigger(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "WatchTrigger"
	},
})

func newWatchTriggerCapsule(t *WatchTrigger) cty.Value {
	return cty.CapsuleVal(WatchTriggerCapsuleType, t)
}

func GetWatchTriggerFromCapsule(val cty.Value) (*WatchTrigger, error) {
	if val.Type() != WatchTriggerCapsuleType {
		return nil, fmt.Errorf("expected watch_trigger capsule, got %s", val.Type().FriendlyName())
	}
	t, ok := val.EncapsulatedValue().(*WatchTrigger)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a WatchTrigger, got %T", val.EncapsulatedValue())
	}
	return t, nil
}

// --- Block processing ---

type triggerWatchBody struct {
	Watch    hcl.Expression `hcl:"watch"`
	Action   hcl.Expression `hcl:"action"`
	SkipWhen hcl.Expression `hcl:"skip_when,optional"`
}

func init() {
	cfg.RegisterTriggerType("watch", cfg.TriggerRegistration{
		Process:         processWatchTrigger,
		HasDependencyId: true,
	}, cfg.WithSchema(watchTriggerSchema))

	cfg.RegisterContextSchema("trigger-watch", cfg.ContextSchema{
		Summary: "Evaluated on each observed change to the watched value.",
		Doc:     "The same shape is used for `skip_when`, which is evaluated first.",
		Fields: []cfg.ContextField{
			{Name: "trigger", Type: "string", Summary: "Always `\"watch\"`."},
			{Name: "name", Type: "string", Summary: "Name of this trigger block."},
			{Name: "old_value", Type: cfg.CtxTypeDynamic, Summary: "The value before the change."},
			{Name: "new_value", Type: cfg.CtxTypeDynamic, Summary: "The value after the change."},
		},
	})
}

var watchTriggerSchema = cfg.TypeSchema{
	Sample:  &triggerWatchBody{},
	Summary: "Fires each time a watched value changes.",
	DocPage: "trigger.md#trigger-watch",
	Doc: `Watchable values are ` + "`var`" + `, non-computed gauge and counter ` + "`metric`" + `,
` + "`condition`" + `, and ` + "`fsm`" + ` values. They notify on **every** ` + "`set()`" + ` /
` + "`increment()`" + `, even when the new value equals the old — a producer rewriting
the same value is still alive. For changes-only semantics, say
` + "`skip_when = ctx.old_value == ctx.new_value`" + `.

` + "`sys.ready`" + ` and ` + "`check.<name>`" + ` are watchable too, and behave differently
in two ways worth knowing: they notify only when the value actually *changes*,
since a derived state repeating itself is not an event, and they are **sampled**
— readiness is recomputed only when something asks for it, so in a configuration
with no health endpoint and no polling trigger a watch on one never fires. See
[health](health.md).

The action is dispatched to its own goroutine, so the caller of ` + "`set()`" + ` is not
blocked. It keeps the caller's context values (trace span, auth) but not its
cancellation, so a short-lived caller cannot interrupt an in-flight action. On
shutdown the trigger unregisters and waits for in-flight actions to finish.

` + "`get(trigger.<name>)`" + ` returns the most recently observed new value.

For reactions local to one condition, the condition's own ` + "`on_activate`" + ` /
` + "`on_deactivate`" + ` / ` + "`on_init`" + ` hooks are usually a better fit: they dispatch
synchronously and guarantee post-bootstrap ordering. Use this trigger for async
dispatch and cross-cutting observers.`,
	Attrs: map[string]cfg.AttrMeta{
		"watch": {
			Summary: "The value to watch.",
			Doc:     "Evaluated once at config build time and must produce a watchable capsule; anything else is a config error.",
			Hint:    cfg.HintExpression,
		},
		"action": {
			Summary: "Evaluated on each observed change.",
			Hint:    cfg.HintActionExpression,
			Context: "trigger-watch",
		},
		"skip_when": {
			Summary: "Skip this firing when true.",
			Doc:     "Evaluated first, in the same goroutine, against the same `ctx`. Each firing evaluates it independently.",
			Hint:    cfg.HintPredicateExpression,
			Context: "trigger-watch",
		},
	},
}

func processWatchTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerWatchBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	// Evaluate the watch expression to obtain the Watchable.
	watchVal, watchDiags := body.Watch.Value(config.EvalCtx())
	diags = diags.Extend(watchDiags)
	if diags.HasErrors() {
		return diags
	}

	watchable, err := richcty.WatchableFromCtyValue(watchVal)
	if err != nil {
		return append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid watch target",
			Detail:   err.Error(),
			Subject:  body.Watch.StartRange().Ptr(),
		})
	}

	var skipWhenExpr hcl.Expression
	if cfg.IsExpressionProvided(body.SkipWhen) {
		skipWhenExpr = body.SkipWhen
	}

	name := block.Labels[1]
	t := &WatchTrigger{
		name:           name,
		config:         config,
		watchable:      watchable,
		actionExpr:     body.Action,
		skipWhenExpr:   skipWhenExpr,
		tracerProvider: triggerDef.TracerProvider,
	}

	config.CtyTriggerMap[name] = newWatchTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}
