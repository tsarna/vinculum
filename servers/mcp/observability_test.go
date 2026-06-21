package mcp

import (
	"context"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// newObsServer builds an MCP server wired to the given tracer/meter providers.
func newObsServer(t *testing.T, tp oteltrace.TracerProvider, mp otelmetric.MeterProvider, resources []ResourceDef, tools []ToolDef, prompts []PromptDef) *Server {
	t.Helper()
	srv, err := New(ServerConfig{
		Name:           "test",
		ServerName:     "Test Server",
		ParentEvalCtx:  emptyEvalCtx(),
		Logger:         zap.NewNop(),
		TracerProvider: tp,
		MeterProvider:  mp,
		Resources:      resources,
		Tools:          tools,
		Prompts:        prompts,
	})
	require.NoError(t, err)
	return srv
}

// findSpan returns the recorded span with the given name, or fails the test.
func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	var names []string
	for _, s := range spans {
		names = append(names, s.Name)
	}
	t.Fatalf("span %q not found; recorded spans: %v", name, names)
	return tracetest.SpanStub{}
}

// attrValue returns the string value of the named attribute, and whether it exists.
func attrValue(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value.AsString(), true
		}
	}
	return "", false
}

// opDurationDataPoints collects the mcp.server.operation.duration histogram
// data points from a manual reader.
func opDurationDataPoints(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != instrumentationScope {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "mcp.server.operation.duration" {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "operation.duration should be a float64 histogram")
			return h.DataPoints
		}
	}
	t.Fatal("mcp.server.operation.duration metric not found")
	return nil
}

// dataPointFor returns the histogram data point whose mcp.method.name matches,
// optionally also matching gen_ai.tool.name / gen_ai.prompt.name via target.
func dataPointFor(dps []metricdata.HistogramDataPoint[float64], method, target string) (metricdata.HistogramDataPoint[float64], bool) {
	for _, dp := range dps {
		attrs := dp.Attributes.ToSlice()
		m, _ := attrValue(attrs, attrMCPMethodName)
		if m != method {
			continue
		}
		if target == "" {
			return dp, true
		}
		if tn, ok := attrValue(attrs, attrGenAIToolName); ok && tn == target {
			return dp, true
		}
		if pn, ok := attrValue(attrs, attrGenAIPromptName); ok && pn == target {
			return dp, true
		}
	}
	return metricdata.HistogramDataPoint[float64]{}, false
}

func newObsProviders(t *testing.T) (*tracetest.InMemoryExporter, oteltrace.TracerProvider, *sdkmetric.ManualReader, otelmetric.MeterProvider) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	return exporter, tp, reader, mp
}

func TestObservability_ToolCallSuccess(t *testing.T) {
	exporter, tp, reader, mp := newObsProviders(t)

	srv := newObsServer(t, tp, mp, nil, []ToolDef{
		{Name: "echo", Description: "Echo", Params: []ParamDef{{Name: "text", Type: "string", Required: true}}, Action: parseExpr(t, `ctx.args.text`)},
	}, nil)
	cs := connectInMemory(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "echo",
		Arguments: callArgs(map[string]any{"text": "hi"}),
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	span := findSpan(t, exporter.GetSpans(), "tools/call echo")
	assert.Equal(t, oteltrace.SpanKindServer, span.SpanKind)
	assert.Equal(t, codes.Unset, span.Status.Code)

	method, _ := attrValue(span.Attributes, attrMCPMethodName)
	assert.Equal(t, "tools/call", method)
	tool, _ := attrValue(span.Attributes, attrGenAIToolName)
	assert.Equal(t, "echo", tool)
	op, _ := attrValue(span.Attributes, attrGenAIOperationName)
	assert.Equal(t, "execute_tool", op)
	transport, _ := attrValue(span.Attributes, attrNetworkTransport)
	assert.Equal(t, "tcp", transport)
	proto, _ := attrValue(span.Attributes, attrNetworkProtocol)
	assert.Equal(t, "http", proto)
	_, hasErr := attrValue(span.Attributes, attrErrorType)
	assert.False(t, hasErr, "successful call must not set error.type")

	dps := opDurationDataPoints(t, reader)
	dp, ok := dataPointFor(dps, "tools/call", "echo")
	require.True(t, ok, "expected a duration data point for tools/call echo")
	assert.EqualValues(t, 1, dp.Count)
	_, hasMetricErr := attrValue(dp.Attributes.ToSlice(), attrErrorType)
	assert.False(t, hasMetricErr)
}

func TestObservability_ToolCallError(t *testing.T) {
	exporter, tp, reader, mp := newObsProviders(t)

	srv := newObsServer(t, tp, mp, nil, []ToolDef{
		{Name: "boom", Description: "Fails", Action: parseExpr(t, `mcp_error("nope")`)},
	}, nil)
	cs := connectInMemory(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "boom"})
	require.NoError(t, err)
	require.True(t, res.IsError)

	span := findSpan(t, exporter.GetSpans(), "tools/call boom")
	assert.Equal(t, codes.Error, span.Status.Code)
	errType, ok := attrValue(span.Attributes, attrErrorType)
	require.True(t, ok)
	assert.Equal(t, errorTypeTool, errType)

	dps := opDurationDataPoints(t, reader)
	dp, ok := dataPointFor(dps, "tools/call", "boom")
	require.True(t, ok)
	mErrType, ok := attrValue(dp.Attributes.ToSlice(), attrErrorType)
	require.True(t, ok, "metric must carry error.type=tool_error")
	assert.Equal(t, errorTypeTool, mErrType)
}

func TestObservability_ResourceReadURI(t *testing.T) {
	exporter, tp, reader, mp := newObsProviders(t)

	srv := newObsServer(t, tp, mp, []ResourceDef{
		{URI: "status://current", Name: "Status", Action: parseExpr(t, `"OK"`)},
	}, nil, nil)
	cs := connectInMemory(t, srv)

	_, err := cs.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: "status://current"})
	require.NoError(t, err)

	// Span name must NOT include the URI (cardinality), but the URI must be a
	// span attribute.
	span := findSpan(t, exporter.GetSpans(), "resources/read")
	uri, ok := attrValue(span.Attributes, attrMCPResourceURI)
	require.True(t, ok)
	assert.Equal(t, "status://current", uri)

	// The resource URI must NOT appear on the metric (opt-in / high cardinality).
	dps := opDurationDataPoints(t, reader)
	dp, ok := dataPointFor(dps, "resources/read", "")
	require.True(t, ok)
	_, hasURI := attrValue(dp.Attributes.ToSlice(), attrMCPResourceURI)
	assert.False(t, hasURI, "resource URI must not be a metric attribute")
}

func TestObservability_PromptGet(t *testing.T) {
	exporter, tp, _, mp := newObsProviders(t)

	srv := newObsServer(t, tp, mp, nil, nil, []PromptDef{
		{Name: "greeting", Description: "Greet", Action: parseExpr(t, `mcp_usermessage("hello")`)},
	})
	cs := connectInMemory(t, srv)

	_, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{Name: "greeting"})
	require.NoError(t, err)

	span := findSpan(t, exporter.GetSpans(), "prompts/get greeting")
	name, _ := attrValue(span.Attributes, attrGenAIPromptName)
	assert.Equal(t, "greeting", name)
	method, _ := attrValue(span.Attributes, attrMCPMethodName)
	assert.Equal(t, "prompts/get", method)
}
