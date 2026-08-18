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
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"
)

//go:embed testdata/mcp_auth.vcl
var mcpAuthVCL []byte

// TestAuthReachesToolAction pins the seam between the route that authenticates
// and the tool that runs: an MCP server owns no listener, so the `server "http"`
// route mounting it establishes the identity, and that identity has to survive
// into the tool's own evaluation context as ctx.auth.
//
// It crosses two packages and the MCP SDK's context plumbing — the auth
// middleware puts the value in the request context, the SDK carries it through
// session handling, and the tool's eval context reads it back out — so nothing
// on either side of it would notice the chain breaking. Rejection itself is
// covered by servers/http/auth_test.go and is not repeated here.
func TestAuthReachesToolAction(t *testing.T) {
	c, diags := config.NewConfig().WithSources(mcpAuthVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	handler := c.Servers["http"]["main"].(*httpserver.HttpServer).Server.Handler
	creds := map[string]string{"Authorization": basicAuth("alice", "ignored")}

	initW := mcpCall(t, handler, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
	}, creds)
	require.Equal(t, http.StatusOK, initW.Code, "initialize body: %s", initW.Body.String())

	sessionID := initW.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "expected Mcp-Session-Id header")

	creds["Mcp-Session-Id"] = sessionID
	w := mcpCall(t, handler, "tools/call", map[string]any{
		"name":      "whoami",
		"arguments": map[string]any{},
	}, creds)
	require.Equal(t, http.StatusOK, w.Code, "tools/call body: %s", w.Body.String())

	assert.Equal(t, "alice", toolText(t, w))
}

// mcpCall sends a JSON-RPC request to the mounted MCP route and returns the raw
// response recorder.
func mcpCall(t *testing.T, handler http.Handler, method string, params any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// toolText pulls the single text content item out of a tools/call response.
// Streamable HTTP answers with SSE, so the JSON is on a "data:" line.
func toolText(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()

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

	return content[0].(map[string]any)["text"].(string)
}

func basicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}
