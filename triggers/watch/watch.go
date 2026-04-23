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
func (t *WatchTrigger) OnChange(ctx context.Context, oldValue, newValue cty.Value) {
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
			zap.String("name", t.name), zap.Error(diags))
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
	})
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
