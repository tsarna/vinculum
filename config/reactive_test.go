package config

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// mockWatchable is a test stand-in that exposes a cty.Value and fires
// OnChange notifications when Update() is called. It is an ordinary Watchable
// via the public WatchableMixin; behavior matches the real Variable/Metric
// types closely enough for reactive-expression tests.
type mockWatchable struct {
	types.WatchableMixin
	mu    sync.Mutex
	value cty.Value
}

func newMockWatchable(initial cty.Value) *mockWatchable {
	return &mockWatchable{value: initial}
}

func (m *mockWatchable) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.value, nil
}

func (m *mockWatchable) Update(ctx context.Context, v cty.Value) {
	m.mu.Lock()
	old := m.value
	m.value = v
	m.mu.Unlock()
	m.NotifyAll(ctx, old, v)
}

var mockCapsuleType = cty.CapsuleWithOps("mock_watchable",
	reflect.TypeOf((*mockWatchable)(nil)).Elem(), &cty.CapsuleOps{
		GoString: func(val interface{}) string { return fmt.Sprintf("mock(%p)", val) },
	})

func mockVal(m *mockWatchable) cty.Value { return cty.CapsuleVal(mockCapsuleType, m) }

// getFn is a minimal stand-in for the generic get() used in reactive
// expressions during tests, so we don't pull in the functions package.
var getFn = function.New(&function.Spec{
	Params: []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
	Type:   function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		m := args[0].EncapsulatedValue().(*mockWatchable)
		return m.Get(context.Background(), nil)
	},
})

func parseExpr(t *testing.T, src string) hcl.Expression {
	t.Helper()
	expr, diags := hclsyntax.ParseExpression([]byte(src), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors(), "parse: %s", diags)
	return expr
}

func makeCtx(vars map[string]cty.Value) *hcl.EvalContext {
	return &hcl.EvalContext{
		Variables: vars,
		Functions: map[string]function.Function{"get": getFn},
	}
}

// --- tests ---

func TestReactiveExprExtractsSources(t *testing.T) {
	a := newMockWatchable(cty.True)
	b := newMockWatchable(cty.False)
	ctx := makeCtx(map[string]cty.Value{
		"condition": cty.ObjectVal(map[string]cty.Value{
			"a": mockVal(a),
			"b": mockVal(b),
		}),
	})

	var got cty.Value
	r, diags := NewReactiveExpr(parseExpr(t, "get(condition.a) || get(condition.b)"), ctx,
		func(_ context.Context, v cty.Value) { got = v })
	require.False(t, diags.HasErrors(), "diags: %s", diags)
	require.Len(t, r.Sources(), 2)

	require.Nil(t, r.Start(context.Background()))
	assert.True(t, got.True(), "initial evaluation: a=true || b=false")

	b.Update(context.Background(), cty.True)
	assert.True(t, got.True())

	a.Update(context.Background(), cty.False)
	b.Update(context.Background(), cty.False)
	assert.False(t, got.True(), "both false")

	r.Stop()
	// After Stop, further updates must not fire the callback.
	a.Update(context.Background(), cty.True)
	assert.False(t, got.True(), "callback gated after Stop")
}

func TestReactiveExprDeduplicatesSources(t *testing.T) {
	a := newMockWatchable(cty.True)
	ctx := makeCtx(map[string]cty.Value{
		"condition": cty.ObjectVal(map[string]cty.Value{"a": mockVal(a)}),
	})
	r, diags := NewReactiveExpr(parseExpr(t, "get(condition.a) && get(condition.a)"), ctx, nil)
	require.False(t, diags.HasErrors())
	assert.Len(t, r.Sources(), 1, "same Watchable referenced twice is one source")
}

func TestReactiveExprIgnoresNonWatchableRefs(t *testing.T) {
	ctx := makeCtx(map[string]cty.Value{
		"env": cty.ObjectVal(map[string]cty.Value{"FOO": cty.StringVal("hi")}),
	})
	r, diags := NewReactiveExpr(parseExpr(t, "env.FOO == \"hi\""), ctx, nil)
	require.False(t, diags.HasErrors())
	assert.Empty(t, r.Sources(), "plain cty values contribute no reactive sources")
}

func TestReactiveExprStartIsIdempotent(t *testing.T) {
	a := newMockWatchable(cty.True)
	ctx := makeCtx(map[string]cty.Value{
		"condition": cty.ObjectVal(map[string]cty.Value{"a": mockVal(a)}),
	})
	calls := 0
	r, _ := NewReactiveExpr(parseExpr(t, "get(condition.a)"), ctx,
		func(_ context.Context, _ cty.Value) { calls++ })
	require.Nil(t, r.Start(context.Background()))
	require.Nil(t, r.Start(context.Background()))

	// Only one registration as a Watcher: the single Update should fire the
	// callback exactly once.
	before := calls
	a.Update(context.Background(), cty.False)
	assert.Equal(t, before+1, calls, "duplicate Start must not double-subscribe")
}

func TestReactiveExprStopIsIdempotent(t *testing.T) {
	a := newMockWatchable(cty.True)
	ctx := makeCtx(map[string]cty.Value{
		"condition": cty.ObjectVal(map[string]cty.Value{"a": mockVal(a)}),
	})
	r, _ := NewReactiveExpr(parseExpr(t, "get(condition.a)"), ctx, nil)
	require.Nil(t, r.Start(context.Background()))
	r.Stop()
	r.Stop() // must not panic
}
