package config

import (
	_ "embed"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

//go:embed testdata/variable.vcl
var variabletest []byte

//go:embed testdata/variable_typed.vcl
var variabletypedtest []byte

//go:embed testdata/variable_type_mismatch.vcl
var variabletypemismatchtest []byte

//go:embed testdata/variable_nullable.vcl
var variablenullabletest []byte

//go:embed testdata/variable_null_rejected.vcl
var variablenullrejectedtest []byte

//go:embed testdata/variable_typespec.vcl
var variabletypespectest []byte

func TestVariableBlock(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variabletest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}

func TestVariableTypedBlock(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variabletypedtest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}

func TestVariableTypeMismatchBlock(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variabletypemismatchtest).WithLogger(logger).Build()
	assert.True(t, diags.HasErrors(), "expected diagnostics error for type mismatch")
}

// TestVariableTypeStringDeprecationWarning verifies the quoted-string `type` form
// (type = "number") emits a non-fatal deprecation warning diagnostic.
func TestVariableTypeStringDeprecationWarning(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variabletypedtest).WithLogger(logger).Build()
	assert.False(t, diags.HasErrors(), "deprecation is a warning, not an error")

	found := false
	for _, d := range diags {
		if d.Severity == hcl.DiagWarning && d.Summary == "Deprecated var type string" {
			found = true
		}
	}
	assert.True(t, found, "expected a deprecation warning for the quoted-string var type form")
}

// TestVariableTypeSpecBlock exercises the `type` attribute as a functy type spec
// (not a string): a bare primitive, a structural collection type, and a
// host-registered capsule type, each enforced via Coerce.
func TestVariableTypeSpecBlock(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variabletypespectest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}

func TestVariableNullableBlock(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variablenullabletest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}

func TestVariableNullRejectedBlock(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variablenullrejectedtest).WithLogger(logger).Build()
	assert.True(t, diags.HasErrors(), "expected diagnostics error for null on non-nullable variable")
}

// TestVariableRejectsUnknownAttribute guards against a var block silently
// swallowing an attribute it does not understand. Process reads attributes one
// at a time rather than decoding through gohcl, and used JustAttributes, so a
// typo parsed cleanly and did nothing.
func TestVariableRejectsUnknownAttribute(t *testing.T) {
	src := []byte(`
var "v" {
    value          = 1
    typo_attribute = "silently ignored?"
}
`)
	_, diags := NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	assert.True(t, diags.HasErrors(), "an unknown var attribute should be rejected")
	assert.Contains(t, diags.Error(), "typo_attribute")
}

// TestVariableAcceptsEveryDocumentedAttribute is the other half: varBody is
// what both the parser's schema and `vinculum schema` are derived from, so
// every attribute it declares must actually parse.
func TestVariableAcceptsEveryDocumentedAttribute(t *testing.T) {
	src := []byte(`
var "v" {
    type     = number
    nullable = false
    value    = 1
}
`)
	_, diags := NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	assert.False(t, diags.HasErrors(), "config should be valid: %s", diags.Error())
}
