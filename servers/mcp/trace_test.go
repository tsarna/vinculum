package mcp_test

import (
	"context"
	_ "embed"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	mcpsrv "github.com/tsarna/vinculum/servers/mcp"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

//go:embed testdata/mcp_trace.vcl
var mcpTraceVCL []byte

// TestMCPTraceSpanPerRequest verifies that an inbound MCP request produces a
// span.
//
// The span comes from the SDK receiving middleware rather than from anything
// HTTP, which is what makes it a property of this package: it describes the MCP
// method being served, and would be identical over any transport the SDK
// supports. Extracting inbound trace context and opening the surrounding server
// span belong to the `server "http"` block in front of it.
func TestMCPTraceSpanPerRequest(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		tp.Shutdown(context.Background()) //nolint:errcheck
		otel.SetTracerProvider(otel.GetTracerProvider())
	})

	c, diags := config.NewConfig().WithSources(mcpTraceVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	mcpSrv := c.Servers["mcp"]["trace_test"].(*mcpsrv.McpServer)

	w := mcpCall(t, mcpSrv.GetHandler(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
	}, nil)
	require.Equal(t, http.StatusOK, w.Code, "initialize body: %s", w.Body.String())

	spans := exporter.GetSpans()
	require.NotEmpty(t, spans, "expected an MCP span to be recorded")
	assert.Contains(t, spans[0].Name, "initialize",
		"the span should name the MCP method it served")
}
