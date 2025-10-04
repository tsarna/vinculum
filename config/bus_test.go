package config

import (
	"testing"

	_ "embed"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

//go:embed testdata/bus.vcl
var bustest []byte

func TestBus(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	config, diags := NewConfig().WithSources(bustest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	assert.Contains(t, config.Constants, "bus")

	assert.Contains(t, config.Buses, "main")
	assert.Contains(t, config.Buses, "ws")

	assert.Contains(t, config.CtyBusMap, "main")
	assert.Contains(t, config.CtyBusMap, "ws")
}
