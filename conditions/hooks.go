package conditions

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	cfg "github.com/tsarna/vinculum/config"
)

// HookDispatcher evaluates condition lifecycle hooks. It is registered as an
// internal Watcher on the condition's StateMachine so on_activate /
// on_deactivate fire synchronously on every output transition; on_init fires
// once from the subtype's PostStart().
//
// Error handling: any diagnostics from building the eval context or evaluating
// the hook expression are logged to UserLogger and do not propagate — matches
// the existing trigger "watch" behaviour.
//
// Context propagation: OnChange receives the ctx from sm.notifyAll(ctx, ...).
// That ctx carries any caller trace span (e.g. from an inbound message that
// drove the transition) or is context.Background() for autonomous timer-
// driven transitions; either way the hook opens a child/root span named
// "trigger.condition.<hook> <name>".
type HookDispatcher struct {
	name           string
	hooks          Hooks
	config         *cfg.Config
	tracerProvider trace.TracerProvider
}

// NewHookDispatcher constructs a dispatcher. Returns nil when no hooks are
// configured so callers can skip the Watch() registration entirely.
func NewHookDispatcher(name string, hooks Hooks, config *cfg.Config, tp trace.TracerProvider) *HookDispatcher {
	if !cfg.IsExpressionProvided(hooks.OnInit) &&
		!cfg.IsExpressionProvided(hooks.OnActivate) &&
		!cfg.IsExpressionProvided(hooks.OnDeactivate) {
		return nil
	}
	return &HookDispatcher{
		name:           name,
		hooks:          hooks,
		config:         config,
		tracerProvider: tp,
	}
}

// OnChange implements richcty.Watcher. StateMachine already guarantees output
// changes, so the defensive equal check is only for safety.
func (h *HookDispatcher) OnChange(ctx context.Context, old, new cty.Value) {
	if h == nil {
		return
	}
	oldB, newB := old.True(), new.True()
	if oldB == newB {
		return
	}
	if newB {
		h.eval(ctx, h.hooks.OnActivate, "on_activate", oldB, newB, false)
	} else {
		h.eval(ctx, h.hooks.OnDeactivate, "on_deactivate", oldB, newB, false)
	}
}

// FireInit evaluates on_init once. Called from the owning subtype's
// PostStart() with the condition's current output.
func (h *HookDispatcher) FireInit(currentOutput bool) {
	if h == nil {
		return
	}
	h.eval(context.Background(), h.hooks.OnInit, "on_init", false, currentOutput, true)
}

func (h *HookDispatcher) eval(ctx context.Context, expr hcl.Expression, hookName string, old, new, initHook bool) {
	if !cfg.IsExpressionProvided(expr) {
		return
	}
	ctx, stopSpan := hclutil.StartTriggerSpan(ctx, h.tracerProvider, "condition."+hookName, h.name)

	builder := hclutil.NewEvalContext(ctx).
		WithStringAttribute("trigger", "condition").
		WithStringAttribute("name", h.name).
		WithAttribute("new_value", cty.BoolVal(new))
	if !initHook {
		builder = builder.WithAttribute("old_value", cty.BoolVal(old))
	}
	evalCtx, err := builder.BuildEvalContext(h.config.EvalCtx())
	if err != nil {
		h.config.UserLogger.Error("condition hook: eval context build failed",
			zap.String("name", h.name), zap.String("hook", hookName), zap.Error(err))
		stopSpan(err)
		return
	}
	_, diags := expr.Value(evalCtx)
	if diags.HasErrors() {
		h.config.UserLogger.Error("condition hook error",
			zap.String("name", h.name), zap.String("hook", hookName), zap.Error(diags))
		stopSpan(diags)
		return
	}
	stopSpan(nil)
}
