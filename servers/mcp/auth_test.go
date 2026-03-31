package mcp_test

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	mcpsrv "github.com/tsarna/vinculum/servers/mcp"
	"go.uber.org/zap"
)

//go:embed testdata/mcp_auth.vcl
var mcpAuthVCL []byte

// mcpCall sends a JSON-RPC request to the MCP handler and returns the raw response recorder.
func mcpCall(t *testing.T, handler http.Handler, method string, params any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func basicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func TestMcpAuthMiddleware_NoAuth(t *testing.T) {
	c, diags := config.NewConfig().WithSources(mcpAuthVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	srv := c.Servers["mcp"]["auth_test"].(*mcpsrv.McpServer)

	// No credentials → auth middleware should return 401 before MCP sees the request.
	w := mcpCall(t, srv.GetHandler(), "tools/call", map[string]any{
		"name":      "whoami",
		"arguments": map[string]any{},
	}, nil)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMcpAuthMiddleware_WithAuth(t *testing.T) {
	c, diags := config.NewConfig().WithSources(mcpAuthVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	srv := c.Servers["mcp"]["auth_test"].(*mcpsrv.McpServer)

	// Initialize the session with Basic auth credentials.
	initW := mcpCall(t, srv.GetHandler(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
	}, map[string]string{"Authorization": basicAuth("alice", "ignored")})
	require.Equal(t, http.StatusOK, initW.Code, "initialize body: %s", initW.Body.String())

	sessionID := initW.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "expected Mcp-Session-Id header")

	// Call the whoami tool — auth re-evaluated on each request.
	w := mcpCall(t, srv.GetHandler(), "tools/call", map[string]any{
		"name":      "whoami",
		"arguments": map[string]any{},
	}, map[string]string{
		"Authorization":  basicAuth("alice", "ignored"),
		"Mcp-Session-Id": sessionID,
	})

	require.Equal(t, http.StatusOK, w.Code, "tools/call body: %s", w.Body.String())

	// Streamable HTTP returns SSE: find the "data: {...}" line and parse it.
	var resp map[string]any
	for _, line := range strings.Split(w.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if err := json.Unmarshal([]byte(data), &resp); err == nil {
			break
		}
	}
	require.NotNil(t, resp, "no JSON data line found in SSE response: %s", w.Body.String())

	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "expected result in response: %s", w.Body.String())

	content, ok := result["content"].([]any)
	require.True(t, ok, "expected content array")
	require.Len(t, content, 1)

	item := content[0].(map[string]any)
	assert.Equal(t, "alice", item["text"])
}
