package config

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

//go:embed testdata/env.vcl
var envtest []byte

func TestEnv(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(envtest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}
