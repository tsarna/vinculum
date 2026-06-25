package hclutil_test

import (
	"context"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/baggage"
)

// evalExpr parses and evaluates a single HCL expression against evalCtx.
func evalExpr(t *testing.T, src string, evalCtx *hcl.EvalContext) cty.Value {
	t.Helper()
	expr, diags := hclsyntax.ParseExpression([]byte(src), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors(), diags.Error())
	v, diags := expr.Value(evalCtx)
	require.False(t, diags.HasErrors(), diags.Error())
	return v
}

// TestBaggageCapsuleSharesContextPointer verifies that a set(ctx.baggage, …)
// evaluated through the real BuildEvalContext wiring is observed when the
// context is later read back through the _ctx capsule — i.e. both capsules
// alias the same context cell.
func TestBaggageCapsuleSharesContextPointer(t *testing.T) {
	evalCtx, err := hclutil.NewEvalContext(context.Background()).
		WithFunctions(richcty.GetGenericFunctions()).
		BuildEvalContext(emptyEvalCtx())
	require.NoError(t, err)

	ctxVal := evalCtx.Variables["ctx"]

	// Write through ctx.baggage.
	evalExpr(t, `set(ctx.baggage, "tenant", "acme")`, evalCtx)

	// The mutation is visible through the same ctx object's _ctx capsule.
	goCtx, err := richcty.GetContextFromValue(ctxVal)
	require.NoError(t, err)
	assert.Equal(t, "acme", baggage.FromContext(goCtx).Member("tenant").Value())

	// And readable back through ctx.baggage via get().
	got := evalExpr(t, `get(ctx.baggage, "tenant")`, evalCtx)
	assert.Equal(t, "acme", got.AsString())

	// delete() removes it.
	evalExpr(t, `delete(ctx.baggage, "tenant")`, evalCtx)
	goCtx, err = richcty.GetContextFromValue(ctxVal)
	require.NoError(t, err)
	assert.Equal(t, "", baggage.FromContext(goCtx).Member("tenant").Value())
}
