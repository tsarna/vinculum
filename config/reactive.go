package config

import (
	"context"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

// ReactiveExpr wraps an HCL expression that must be re-evaluated whenever any
// Watchable it references changes — the mechanism behind condition block
// input = and inhibit = attributes.
//
// Unlike a one-shot gohcl.DecodeBody, ReactiveExpr extracts every Watchable
// referenced by the expression at construction time, subscribes to each as a
// Watcher on Start(), and invokes a callback with the re-evaluated value on
// every change. Stop() unsubscribes. Construction is cheap; Start() is the
// point at which the caller commits to receiving callbacks.
//
// Circular references between blocks are rejected at config load time by the
// existing block-level dependency DAG (ExtractBlockDependencies walks the
// block body and Kahn's sort fails on cycles), so ReactiveExpr itself does
// not need additional cycle detection.
type ReactiveExpr struct {
	expr     hcl.Expression
	evalCtx  *hcl.EvalContext
	sources  []types.Watchable
	callback func(ctx context.Context, value cty.Value)

	mu      sync.Mutex
	started bool
}

// NewReactiveExpr constructs a ReactiveExpr by extracting every Watchable
// referenced by expr against evalCtx. callback is invoked with the current
// value on Start(), and again whenever any source Watchable's value changes.
//
// References that do not resolve to a Watchable capsule (constants, plain cty
// values, env references, etc.) are silently ignored — the expression may
// freely mix reactive and static inputs. If a reference cannot be resolved
// at all, a diagnostic is returned; the returned ReactiveExpr is still usable
// (it just won't fire for that branch).
func NewReactiveExpr(expr hcl.Expression, evalCtx *hcl.EvalContext, callback func(ctx context.Context, value cty.Value)) (*ReactiveExpr, hcl.Diagnostics) {
	var sources []types.Watchable
	var diags hcl.Diagnostics
	seen := map[types.Watchable]bool{}
	for _, tr := range expr.Variables() {
		val, d := tr.TraverseAbs(evalCtx)
		diags = diags.Extend(d)
		if d.HasErrors() || !val.IsKnown() || val.IsNull() {
			continue
		}
		if !val.Type().IsCapsuleType() {
			continue
		}
		w, ok := val.EncapsulatedValue().(types.Watchable)
		if !ok {
			continue
		}
		if seen[w] {
			continue
		}
		seen[w] = true
		sources = append(sources, w)
	}
	return &ReactiveExpr{
		expr:     expr,
		evalCtx:  evalCtx,
		sources:  sources,
		callback: callback,
	}, diags
}

// Sources returns the list of Watchables this expression depends on. Useful
// for tests and introspection.
func (r *ReactiveExpr) Sources() []types.Watchable {
	return r.sources
}

// Eval re-evaluates the expression against the current eval context and
// returns the resulting value.
func (r *ReactiveExpr) Eval() (cty.Value, hcl.Diagnostics) {
	return r.expr.Value(r.evalCtx)
}

// Start subscribes to every source Watchable and invokes the callback with
// the expression's current value. Idempotent.
func (r *ReactiveExpr) Start(ctx context.Context) hcl.Diagnostics {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = true
	r.mu.Unlock()

	for _, s := range r.sources {
		s.Watch(r)
	}
	val, diags := r.Eval()
	if !diags.HasErrors() && r.callback != nil {
		r.callback(ctx, val)
	}
	return diags
}

// Stop unsubscribes from every source Watchable. Idempotent.
func (r *ReactiveExpr) Stop() {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	r.started = false
	r.mu.Unlock()

	for _, s := range r.sources {
		s.Unwatch(r)
	}
}

// OnChange implements types.Watcher. When any source Watchable changes, the
// expression is re-evaluated and the callback is invoked with the new value.
// Evaluation errors are swallowed here (the previous value remains in effect
// from the caller's perspective); config-time errors surface at Start().
func (r *ReactiveExpr) OnChange(ctx context.Context, _, _ cty.Value) {
	val, diags := r.Eval()
	if diags.HasErrors() {
		return
	}
	if r.callback != nil {
		r.callback(ctx, val)
	}
}
