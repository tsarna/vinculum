package config

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
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

// A VCL expression that fails at event time is rendered against its own line,
// the way a functy throw is: the failure is in the config, so the report says
// where in the config.
func TestActionErrorRendersVCLSource(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	src := []byte("bus \"main\" {}\n\ntrigger \"start\" \"boom\" {\n  action = length(42)\n}\n")
	config, diags := NewConfig().WithSources(src).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// The action where it sits in the file, so its range is the one a trigger
	// would carry into the failure.
	expr, evalDiags := evalInSource(t, config, src, "length(42)")
	require.True(t, evalDiags.HasErrors())
	require.NotNil(t, expr)

	field := config.ActionError(evalDiags)
	require.Equal(t, "error", field.Key)
	require.NotEmpty(t, field.String, "a located failure renders, rather than falling back to zap.Error")
	assert.Contains(t, field.String, "Error in function call")
	assert.Contains(t, field.String, "line 4", "the line the action is on")
	assert.Contains(t, field.String, "action = length(42)", "quoted from the source Build retained")
}

// evalInSource evaluates one expression of src against the config, positioned
// where it actually appears — the filename and line Build recorded it under, so
// a diagnostic about it names something the file map can quote.
func evalInSource(t *testing.T, c *Config, src []byte, expression string) (hclsyntax.Expression, hcl.Diagnostics) {
	t.Helper()

	// By content, not by name: the map holds every source Build parsed,
	// including the externs a functy package embeds.
	var filename string
	for name, file := range c.files {
		if bytes.Equal(file.Bytes, src) {
			filename = name
			break
		}
	}
	require.NotEmpty(t, filename, "Build should have retained the source")

	offset := bytes.Index(src, []byte(expression))
	require.GreaterOrEqual(t, offset, 0, "expression is not in the source")
	start := hcl.Pos{
		Byte:   offset,
		Line:   bytes.Count(src[:offset], []byte("\n")) + 1,
		Column: offset - bytes.LastIndexByte(src[:offset], '\n'),
	}

	expr, pdiags := hclsyntax.ParseExpression([]byte(expression), filename, start)
	require.False(t, pdiags.HasErrors())
	_, diags := expr.Value(c.evalCtx)
	return expr, diags
}

// A subscriber that has reported a failure itself says so on the way out, so
// the bus does not repeat it in the plain form the rich one replaced — and only
// then, so a failure the subscriber stayed quiet about is still the bus's to
// log. The two have to be exactly complementary or a failure goes unlogged.
func TestActionSubscriberMarksWhatItReported(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	src := []byte("bus \"main\" {}\n\nsubscription \"s\" {\n  target = bus.main\n  topics = [\"in\"]\n  action = length(42)\n}\n")
	config, diags := NewConfig().WithSources(src).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	deliver := func(expr hclsyntax.Expression) error {
		sub := &ActionSubscriber{Config: config, ActionExpr: expr}
		return sub.OnEvent(context.Background(), "in", "hello", nil)
	}

	located, _ := evalInSource(t, config, src, "length(42)")
	err = deliver(located)
	require.Error(t, err)
	var reported bus.ReportedError
	assert.True(t, errors.As(err, &reported),
		"a failure reported here must be marked, or the bus logs it again")

	// Nothing to render, so nothing was reported, so the mark would silence a
	// failure no one else has logged.
	elsewhere, pdiags := hclsyntax.ParseExpression([]byte(`length(42)`), "<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors())
	err = deliver(elsewhere)
	require.Error(t, err)
	assert.False(t, errors.As(err, &reported), "unreported failures stay the bus's to log")
}

// Without a source to quote, the rendered form is longer than the one-line
// fallback and says less: a range into something synthesized at runtime — a
// `var` type spec, an expression parsed from a string — names no file that was
// read, so there is no line to show.
func TestActionErrorFallsBackWithoutSource(t *testing.T) {
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
	assert.Empty(t, field.String, "no source to quote, so zap.Error's one line")
}
