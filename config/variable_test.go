package config

import (
	_ "embed"
	"testing"

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
