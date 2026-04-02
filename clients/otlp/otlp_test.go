package otlp_test

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

//go:embed testdata/basic.vcl
var basicVCL []byte

//go:embed testdata/full.vcl
var fullVCL []byte

func TestOtlpClientRegistered(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(basicVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Client registered under the "otlp" type
	assert.Contains(t, c.Clients, "otlp")
	assert.Contains(t, c.Clients["otlp"], "default")

	// Accessible via OtlpClients
	assert.Contains(t, c.OtlpClients, "default")
}

func TestOtlpClientDefaultAutoWire(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(basicVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Single OTLP client → auto-wires as default
	oc, defaultDiags := c.GetDefaultOtlpClient()
	require.False(t, defaultDiags.HasErrors(), defaultDiags.Error())
	require.NotNil(t, oc)
	assert.Equal(t, "default", oc.GetName())
}

func TestOtlpClientIsDefaultFlag(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(fullVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oc, defaultDiags := c.GetDefaultOtlpClient()
	require.False(t, defaultDiags.HasErrors(), defaultDiags.Error())
	require.NotNil(t, oc)
	assert.True(t, oc.IsDefaultClient())
}

func TestOtlpClientInStartables(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(basicVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// OTLP client must be in Startables (calls otel.SetTracerProvider on Start)
	assert.NotEmpty(t, c.Startables)

	found := false
	for _, s := range c.Startables {
		if oc, ok := s.(cfg.OtlpClient); ok {
			if oc.GetName() == "default" {
				found = true
				break
			}
		}
	}
	assert.True(t, found, "OTLP client should be in Startables")
}

func TestOtlpClientNoDefault_WhenNone(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().
		WithSources([]byte(`# empty`)).
		WithLogger(logger).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oc, defaultDiags := c.GetDefaultOtlpClient()
	require.False(t, defaultDiags.HasErrors())
	assert.Nil(t, oc)
}

//go:embed testdata/two_one_default.vcl
var twoOneDefaultVCL []byte

//go:embed testdata/two_no_default.vcl
var twoNoDefaultVCL []byte

//go:embed testdata/two_both_default.vcl
var twoBothDefaultVCL []byte

func TestOtlpClientDefaultAutoWire_TwoOneDefault(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(twoOneDefaultVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oc, defaultDiags := c.GetDefaultOtlpClient()
	require.False(t, defaultDiags.HasErrors(), defaultDiags.Error())
	require.NotNil(t, oc)
	assert.Equal(t, "primary", oc.GetName())
}

func TestOtlpClientDefaultAutoWire_TwoNoDefault(t *testing.T) {
	// Multiple clients, none marked default → nil (caller must wire explicitly)
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(twoNoDefaultVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oc, defaultDiags := c.GetDefaultOtlpClient()
	require.False(t, defaultDiags.HasErrors())
	assert.Nil(t, oc)
}

func TestOtlpClientDefaultAutoWire_TwoBothDefault(t *testing.T) {
	// Multiple clients all marked default → config error
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(twoBothDefaultVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	_, defaultDiags := c.GetDefaultOtlpClient()
	assert.True(t, defaultDiags.HasErrors(), "two default clients should produce an error")
}
