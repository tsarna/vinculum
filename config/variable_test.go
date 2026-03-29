package config

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
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

func TestVariableBlock(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(variabletest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}

func TestVariableGetSetIncrement(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))

	// Get on null returns default
	result, err := v.Get([]cty.Value{cty.NumberIntVal(42)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(42)))

	// Get on null without default returns null
	result, err = v.Get([]cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())

	// Set
	newVal, err := v.Set([]cty.Value{cty.NumberIntVal(10)})
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(10)))

	// Get after Set
	result, err = v.Get([]cty.Value{cty.NumberIntVal(99)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(10)))

	// Increment
	newVal, err = v.Increment([]cty.Value{cty.NumberIntVal(5)})
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(15)))

	// Get after Increment
	result, err = v.Get([]cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(15)))
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

func TestVariableTypedSet_Valid(t *testing.T) {
	v := NewTypedVariable(cty.NullVal(cty.DynamicPseudoType), "number")

	result, err := v.Set([]cty.Value{cty.NumberIntVal(7)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(7)))
}

func TestVariableTypedSet_Invalid(t *testing.T) {
	v := NewTypedVariable(cty.NullVal(cty.DynamicPseudoType), "number")

	_, err := v.Set([]cty.Value{cty.StringVal("bad")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "number")
	assert.Contains(t, err.Error(), "string")
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

func TestVariableNonNullable_SetNull(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(5))
	v.nullable = false

	_, err := v.Set([]cty.Value{cty.NullVal(cty.DynamicPseudoType)})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nullable")
}

func TestVariableNonNullable_SetNoArgs(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(5))
	v.nullable = false

	_, err := v.Set([]cty.Value{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nullable")
}

func TestVariableNonNullable_SetValue(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))
	v.nullable = false

	result, err := v.Set([]cty.Value{cty.StringVal("ok")})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("ok")))
}

func TestVariableTypedSet_Null(t *testing.T) {
	v := NewTypedVariable(cty.NumberIntVal(5), "number")

	// Setting null is always allowed even on typed variables
	result, err := v.Set([]cty.Value{cty.NullVal(cty.DynamicPseudoType)})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())
}

func TestVariableTypedSet_NoArgs(t *testing.T) {
	v := NewTypedVariable(cty.NumberIntVal(5), "number")

	// Set with no args sets to null (allowed)
	result, err := v.Set([]cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())
}

func TestVariableIncrementErrors(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))

	// Increment on null fails
	_, err := v.Increment([]cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)

	// Increment with non-number delta fails
	_, _ = v.Set([]cty.Value{cty.NumberIntVal(10)})
	_, err = v.Increment([]cty.Value{cty.StringVal("bad")})
	assert.Error(t, err)

	// Set to string then increment fails
	_, _ = v.Set([]cty.Value{cty.StringVal("hello")})
	_, err = v.Increment([]cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)
}
