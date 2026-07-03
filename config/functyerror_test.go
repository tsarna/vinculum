package config

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestFunctyActionErrorRendersCtySource verifies that when a functy function
// called from a VCL expression raises (a failed assert), ActionError renders the
// throw against the .cty source: the assert message, the .cty file location, and
// the captured operand detail.
func TestFunctyActionErrorRendersCtySource(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().
		WithSources("testdata/functythrow").
		WithLogger(logger).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Evaluate a call that trips the assert, as an action expression would.
	expr, pdiags := hclsyntax.ParseExpression([]byte(`boom(-3)`), "<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors())
	_, evalDiags := expr.Value(config.evalCtx)
	require.True(t, evalDiags.HasErrors())

	field := config.ActionError(evalDiags)
	require.Equal(t, "error", field.Key)
	require.NotEmpty(t, field.String, "a functy throw should render to a string field, not fall back to zap.Error")

	rendered := field.String
	assert.Contains(t, rendered, "must be positive", "the assert message")
	assert.Contains(t, rendered, "boom.cty", "the .cty source location")
	assert.Contains(t, rendered, "n = -3", "the captured operand detail")
}

// TestActionErrorFallsBackForNonFuncty verifies ActionError falls back to
// zap.Error for a plain (non-functy) evaluation error.
func TestActionErrorFallsBackForNonFuncty(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().WithLogger(logger).Build()
	require.False(t, diags.HasErrors())

	expr, pdiags := hclsyntax.ParseExpression([]byte(`nonexistent_func()`), "<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors())
	_, evalDiags := expr.Value(config.evalCtx)
	require.True(t, evalDiags.HasErrors())

	field := config.ActionError(evalDiags)
	assert.Equal(t, "error", field.Key)
	assert.Empty(t, field.String, "a non-functy error falls back to zap.Error (no string payload)")
}
