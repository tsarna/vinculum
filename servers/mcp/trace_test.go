package mcp_test

import (
	"context"
	_ "embed"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	mcpsrv "github.com/tsarna/vinculum/servers/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

//go:embed testdata/mcp_trace.vcl
var mcpTraceVCL []byte

// TestMCPTrace_WithTracerProvider verifies that a standalone MCP server creates
// a child span when a real TracerProvider is active.
func TestMCPTrace_WithTracerProvider(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	t.Cleanup(func() {
		tp.Shutdown(context.Background()) //nolint:errcheck
		otel.SetTracerProvider(otel.GetTracerProvider())
	})

	c, diags := config.NewConfig().WithSources(mcpTraceVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	mcpSrv := c.Servers["mcp"]["trace_test"].(*mcpsrv.McpServer)

	ts := httptest.NewServer(mcpSrv.GetHandler())
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", "00-80e1afed08e019fc1110464cfa66635c-7a085853722dc6d2-01")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.NotEqual(t, 0, resp.StatusCode)

	// Verify a span was recorded with the correct parent trace ID.
	spans := exporter.GetSpans()
	require.NotEmpty(t, spans, "expected at least one span to be recorded")
	assert.Equal(t, "80e1afed08e019fc1110464cfa66635c", spans[0].SpanContext.TraceID().String())
}
