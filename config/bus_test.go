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

// A bus block used to carry a `,remain` body that nothing read, so it was the
// one block type that accepted any attribute at all: `queue_sizee = 500` parsed
// clean and the bus quietly took its default. Reintroducing the field would
// make this pass again.
func TestBusRejectsUnknownAttribute(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().
		WithSources([]byte("bus \"b\" {\n queue_sizee = 500\n}\n")).
		WithLogger(logger).
		Build()

	assert.True(t, diags.HasErrors(), "a misspelled bus attribute must be reported")
	assert.Contains(t, diags.Error(), "queue_sizee")
}
