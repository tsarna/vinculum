package config

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

//go:embed testdata/random.vcl
var randomTestdata []byte

func TestRandomFunctions(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(randomTestdata).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}
