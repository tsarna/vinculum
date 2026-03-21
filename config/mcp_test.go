package config

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

//go:embed testdata/mcp_resources.vcl
var mcpResourcesVCL []byte

func TestMcpResourcesConfig(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(mcpResourcesVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Server is registered by type and name
	assert.Contains(t, cfg.Servers, "mcp")
	assert.Contains(t, cfg.Servers["mcp"], "test")

	// Server is accessible via the cty server map
	assert.Contains(t, cfg.CtyServerMap, "test")

	// Server is in the startables list
	found := false
	for _, s := range cfg.Startables {
		if _, ok := s.(*McpServer); ok {
			found = true
			break
		}
	}
	assert.True(t, found, "expected McpServer in Startables")
}
