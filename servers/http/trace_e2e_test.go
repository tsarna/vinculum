package httpserver_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

//go:embed testdata/http_trace_e2e.vcl
var traceE2EVCL []byte

// buildE2EServer builds the config and calls Start() so that otelhttp wraps
// the mux. It returns the HTTP server and registers cleanup to shut it down.
func buildE2EServer(t *testing.T) *httpserver.HttpServer {
	t.Helper()
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(traceE2EVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	srv := c.Servers["http"]["main"].(*httpserver.HttpServer)
	require.NoError(t, srv.Start())

	t.Cleanup(func() {
		srv.Server.Shutdown(context.Background()) //nolint:errcheck
	})
	return srv
}

// TestTraceE2E_PropagationOnly verifies the full pipeline with no client "otlp":
// otelhttp extracts the incoming traceparent, the remote SpanContext lands in
// the Go context, and ctx.trace_id / ctx.span_id are accessible in the action.
func TestTraceE2E_PropagationOnly(t *testing.T) {
	srv := buildE2EServer(t)
	ts := httptest.NewServer(srv.Server.Handler)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/trace", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", "00-80e1afed08e019fc1110464cfa66635c-7a085853722dc6d2-01")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]string
	require.NoError(t, json.Unmarshal(body, &result))

	assert.Equal(t, "80e1afed08e019fc1110464cfa66635c", result["trace_id"])
	assert.Equal(t, "7a085853722dc6d2", result["span_id"],
		"span_id should be the incoming span (no local span created without OTLP client)")
}

// TestTraceE2E_WithTracerProvider verifies that when a real TracerProvider is
// active, otelhttp creates a local child span. ctx.trace_id should still match
// the incoming trace ID, and ctx.span_id should be the *new* child span's ID
// (different from the incoming parent span ID).
func TestTraceE2E_WithTracerProvider(t *testing.T) {
	// Set up a real TracerProvider with an in-memory exporter.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	t.Cleanup(func() {
		tp.Shutdown(context.Background())                //nolint:errcheck
		otel.SetTracerProvider(otel.GetTracerProvider()) // reset to NOOP
	})

	srv := buildE2EServer(t)
	ts := httptest.NewServer(srv.Server.Handler)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/trace", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", "00-80e1afed08e019fc1110464cfa66635c-7a085853722dc6d2-01")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]string
	require.NoError(t, json.Unmarshal(body, &result))

	// Trace ID must be inherited from the incoming traceparent.
	assert.Equal(t, "80e1afed08e019fc1110464cfa66635c", result["trace_id"])

	// A real child span was created — span_id should differ from the parent's.
	assert.NotEmpty(t, result["span_id"])
	assert.NotEqual(t, "7a085853722dc6d2", result["span_id"],
		"a new child span should have been created with a different span_id")

	// Confirm a span was actually recorded by the exporter.
	spans := exporter.GetSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "80e1afed08e019fc1110464cfa66635c", spans[0].SpanContext.TraceID().String())
}

// TestTraceE2E_NoTraceparent verifies that a request without a traceparent
// header results in empty ctx.trace_id / ctx.span_id when no TracerProvider
// is active (NOOP tracer creates no span, no remote context present).
func TestTraceE2E_NoTraceparent(t *testing.T) {
	srv := buildE2EServer(t)
	ts := httptest.NewServer(srv.Server.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/trace")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]string
	require.NoError(t, json.Unmarshal(body, &result))

	assert.Equal(t, "", result["trace_id"])
	assert.Equal(t, "", result["span_id"])
}
