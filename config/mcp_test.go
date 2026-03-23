package config

import (
	_ "embed"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

//go:embed testdata/mcp_resources.vcl
var mcpResourcesVCL []byte

//go:embed testdata/mcp_tools.vcl
var mcpToolsVCL []byte

//go:embed testdata/mcp_prompts.vcl
var mcpPromptsVCL []byte

//go:embed testdata/mcp_mount.vcl
var mcpMountVCL []byte

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

func TestMcpToolsConfig(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(mcpToolsVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	assert.Contains(t, cfg.Servers, "mcp")
	assert.Contains(t, cfg.Servers["mcp"], "tools_test")
}

func TestMcpPromptsConfig(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(mcpPromptsVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	assert.Contains(t, cfg.Servers, "mcp")
	assert.Contains(t, cfg.Servers["mcp"], "prompts_test")
}

func TestMcpMountedUnderHttp(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(mcpMountVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// MCP server is registered
	require.Contains(t, cfg.Servers, "mcp")
	require.Contains(t, cfg.Servers["mcp"], "mounted")
	require.Contains(t, cfg.CtyServerMap, "mounted")

	// MCP server with no listen is NOT in Startables
	for _, s := range cfg.Startables {
		if mcp, ok := s.(*McpServer); ok {
			assert.NotEqual(t, "mounted", mcp.Name, "mount-only MCP server should not be in Startables")
		}
	}

	// HTTP server IS in Startables
	httpFound := false
	for _, s := range cfg.Startables {
		if _, ok := s.(*HttpServer); ok {
			httpFound = true
			break
		}
	}
	assert.True(t, httpFound, "expected HttpServer in Startables")

	// The HTTP server's mux routes /mcp/ to the MCP handler.
	// A POST with a JSON-RPC body should reach the MCP handler (not 404).
	httpSrv := cfg.Servers["http"]["main"].(*HttpServer)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.0.1"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	httpSrv.Server.Handler.ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusNotFound, w.Code, "expected MCP handler to respond (not 404)")
}
