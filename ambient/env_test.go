package ambient_test

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	_ "github.com/tsarna/vinculum/ambient"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

//go:embed testdata/env.vcl
var envtest []byte

func TestEnv(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := config.NewConfig().WithSources(envtest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}
