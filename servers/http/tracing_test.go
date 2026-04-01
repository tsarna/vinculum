package httpserver_test

import (
	_ "embed"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"

	// Register client "otlp" and server "http" via their init() functions.
	_ "github.com/tsarna/vinculum/clients/otlp"
)

//go:embed testdata/http_with_otlp.vcl
var httpWithOtlpVCL []byte

// TestTracingConfigParsed verifies that a server "http" block with tracing = client.*
// parses without errors and the server is registered correctly.
func TestTracingConfigParsed(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(httpWithOtlpVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	assert.Contains(t, c.Servers, "http")
	assert.Contains(t, c.Servers["http"], "main")
	assert.Contains(t, c.OtlpClients, "tracer")
}

// TestTracingAutoWire verifies that a server "http" with no explicit tracing attribute
// still builds when a single client "otlp" is declared (auto-wire).
func TestTracingAutoWire(t *testing.T) {
	const autoVCL = `
client "otlp" "tracer" {
    endpoint     = "http://localhost:4318"
    service_name = "test"
}

server "http" "main" {
    listen = "127.0.0.1:18082"
    handle "/ok" {
        action = "ok"
    }
}
`
	logger := zap.NewNop()
	_, diags := cfg.NewConfig().WithSources([]byte(autoVCL)).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())
}

// TestStatusCapturingResponseWriter verifies that status codes and byte counts
// are captured correctly by the internal response wrapper used by loggingMiddleware.
func TestStatusCapturingResponseWriter(t *testing.T) {
	// We test via the full HTTP handler path since statusCapturingResponseWriter
	// is unexported, but its effects are visible through the mux.
	const handlerVCL = `
server "http" "main" {
    listen = "127.0.0.1:18083"
    handle "/ok" {
        action = "hello"
    }
}
`
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources([]byte(handlerVCL)).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	srv := c.Servers["http"]["main"].(*httpserver.HttpServer)

	t.Run("200 response", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		w := httptest.NewRecorder()
		srv.Server.Handler.ServeHTTP(w, req)
		resp := w.Result()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.NotEmpty(t, body)
	})

	t.Run("404 for unknown route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/notfound", nil)
		w := httptest.NewRecorder()
		srv.Server.Handler.ServeHTTP(w, req)
		resp := w.Result()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}
