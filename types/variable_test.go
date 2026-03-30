package types

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

var bg = context.Background()

func TestVariableGetSetIncrement(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))

	// Get on null returns default
	result, err := v.Get(bg, []cty.Value{cty.NumberIntVal(42)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(42)))

	// Get on null without default returns null
	result, err = v.Get(bg, []cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())

	// Set
	newVal, err := v.Set(bg, []cty.Value{cty.NumberIntVal(10)})
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(10)))

	// Get after Set
	result, err = v.Get(bg, []cty.Value{cty.NumberIntVal(99)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(10)))

	// Increment
	newVal, err = v.Increment(bg, []cty.Value{cty.NumberIntVal(5)})
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(15)))

	// Get after Increment
	result, err = v.Get(bg, []cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(15)))
}

func TestVariableTypedSet_Valid(t *testing.T) {
	v := NewTypedVariable(cty.NullVal(cty.DynamicPseudoType), "number")

	result, err := v.Set(bg, []cty.Value{cty.NumberIntVal(7)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(7)))
}

func TestVariableTypedSet_Invalid(t *testing.T) {
	v := NewTypedVariable(cty.NullVal(cty.DynamicPseudoType), "number")

	_, err := v.Set(bg, []cty.Value{cty.StringVal("bad")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "number")
	assert.Contains(t, err.Error(), "string")
}

func TestVariableNonNullable_SetNull(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(5))
	v.nullable = false

	_, err := v.Set(bg, []cty.Value{cty.NullVal(cty.DynamicPseudoType)})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nullable")
}

func TestVariableNonNullable_SetNoArgs(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(5))
	v.nullable = false

	_, err := v.Set(bg, []cty.Value{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nullable")
}

func TestVariableNonNullable_SetValue(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))
	v.nullable = false

	result, err := v.Set(bg, []cty.Value{cty.StringVal("ok")})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("ok")))
}

func TestVariableTypedSet_Null(t *testing.T) {
	v := NewTypedVariable(cty.NumberIntVal(5), "number")

	// Setting null is always allowed even on typed variables
	result, err := v.Set(bg, []cty.Value{cty.NullVal(cty.DynamicPseudoType)})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())
}

func TestVariableTypedSet_NoArgs(t *testing.T) {
	v := NewTypedVariable(cty.NumberIntVal(5), "number")

	// Set with no args sets to null (allowed)
	result, err := v.Set(bg, []cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())
}

func TestVariableIncrementErrors(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))

	// Increment on null fails
	_, err := v.Increment(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)

	// Increment with non-number delta fails
	_, _ = v.Set(bg, []cty.Value{cty.NumberIntVal(10)})
	_, err = v.Increment(bg, []cty.Value{cty.StringVal("bad")})
	assert.Error(t, err)

	// Set to string then increment fails
	_, _ = v.Set(bg, []cty.Value{cty.StringVal("hello")})
	_, err = v.Increment(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)
}
