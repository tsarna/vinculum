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
	result := v.Get(cty.NumberIntVal(42))
	assert.True(t, result.RawEquals(cty.NumberIntVal(42)))

	// Get on null without default returns null
	result = v.Get(cty.NullVal(cty.DynamicPseudoType))
	assert.True(t, result.IsNull())

	// Set
	newVal, err := v.Set(cty.NumberIntVal(10))
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(10)))

	// Get after Set
	result = v.Get(cty.NumberIntVal(99))
	assert.True(t, result.RawEquals(cty.NumberIntVal(10)))

	// Increment
	newVal, err = v.Increment(cty.NumberIntVal(5))
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(15)))

	// Get after Increment
	result = v.Get(cty.NullVal(cty.DynamicPseudoType))
	assert.True(t, result.RawEquals(cty.NumberIntVal(15)))
}

func TestVariableIncrementErrors(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))

	// Increment on null fails
	_, err := v.Increment(cty.NumberIntVal(1))
	assert.Error(t, err)

	// Increment with non-number delta fails
	_, _ = v.Set(cty.NumberIntVal(10))
	_, err = v.Increment(cty.StringVal("bad"))
	assert.Error(t, err)

	// Set to string then increment fails
	_, _ = v.Set(cty.StringVal("hello"))
	_, err = v.Increment(cty.NumberIntVal(1))
	assert.Error(t, err)
}
