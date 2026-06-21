package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	openailib "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/clients/llm"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// newObsClient builds an OpenAIClient pointed at baseURL with the given tracer
// and meter providers, bypassing VCL config so tests can inject in-memory
// exporters.
func newObsClient(baseURL string, tp oteltrace.TracerProvider, mp otelmetric.MeterProvider) *OpenAIClient {
	ocfg := openailib.DefaultConfig("test-key")
	ocfg.BaseURL = baseURL
	addr, port := serverAddrPort(baseURL)
	return &OpenAIClient{
		BaseClient: llm.BaseClient{
			BaseClient: cfg.BaseClient{Name: "gpt"},
			Model:      "gpt-4o-mini",
		},
		openaiClient:  openailib.NewClientWithConfig(ocfg),
		tracer:        tp.Tracer(instrumentationScope),
		metrics:       newOpenAIMetrics(mp),
		provider:      providerOpenAI,
		serverAddress: addr,
		serverPort:    port,
	}
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

func attrValue(attrs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

// histogram returns the named float64 histogram's data points from the reader.
func histogramDataPoints(t *testing.T, reader *sdkmetric.ManualReader, name string) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != instrumentationScope {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "%s should be a float64 histogram", name)
			return h.DataPoints
		}
	}
	t.Fatalf("metric %q not found", name)
	return nil
}

// intHistogramDataPoints returns the named int64 histogram's data points.
func intHistogramDataPoints(t *testing.T, reader *sdkmetric.ManualReader, name string) []metricdata.HistogramDataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != instrumentationScope {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[int64])
			require.True(t, ok, "%s should be an int64 histogram", name)
			return h.DataPoints
		}
	}
	t.Fatalf("metric %q not found", name)
	return nil
}

func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("span %q not found", name)
	return tracetest.SpanStub{}
}

func TestObservability_ChatSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAIResponseJSON("Hi!", "gpt-4o-mini", "stop"))
	}))
	defer srv.Close()

	exporter, tp, reader, mp := newObsProviders(t)
	c := newObsClient(srv.URL+"/", tp, mp)

	_, err := c.Call(context.Background(), []cty.Value{buildTestRequest("", "Hello")})
	require.NoError(t, err)

	// Span: "chat gpt-4o-mini", kind client, no error.
	span := findSpan(t, exporter.GetSpans(), "chat gpt-4o-mini")
	assert.Equal(t, oteltrace.SpanKindClient, span.SpanKind)
	assert.Equal(t, codes.Unset, span.Status.Code)

	op, _ := attrValue(span.Attributes, attrGenAIOperationName)
	assert.Equal(t, "chat", op.AsString())
	provider, _ := attrValue(span.Attributes, attrGenAIProviderName)
	assert.Equal(t, "openai", provider.AsString())
	reqModel, _ := attrValue(span.Attributes, attrGenAIRequestModel)
	assert.Equal(t, "gpt-4o-mini", reqModel.AsString())
	respModel, _ := attrValue(span.Attributes, attrGenAIResponseModel)
	assert.Equal(t, "gpt-4o-mini", respModel.AsString())
	inTok, _ := attrValue(span.Attributes, attrGenAIUsageInputTokens)
	assert.Equal(t, int64(10), inTok.AsInt64())
	outTok, _ := attrValue(span.Attributes, attrGenAIUsageOutputTokens)
	assert.Equal(t, int64(5), outTok.AsInt64())
	addr, ok := attrValue(span.Attributes, attrServerAddress)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", addr.AsString())
	finish, ok := attrValue(span.Attributes, attrGenAIFinishReasons)
	require.True(t, ok)
	assert.Equal(t, []string{"stop"}, finish.AsStringSlice())

	// Duration metric.
	dps := histogramDataPoints(t, reader, "gen_ai.client.operation.duration")
	require.Len(t, dps, 1)
	durOp, _ := attrValue(dps[0].Attributes.ToSlice(), attrGenAIOperationName)
	assert.Equal(t, "chat", durOp.AsString())
	_, hasErr := attrValue(dps[0].Attributes.ToSlice(), attrErrorType)
	assert.False(t, hasErr)

	// Token usage metric: two data points (input + output) distinguished by
	// gen_ai.token.type.
	tokDps := intHistogramDataPoints(t, reader, "gen_ai.client.token.usage")
	byType := map[string]int64{}
	for _, dp := range tokDps {
		tt, ok := attrValue(dp.Attributes.ToSlice(), attrGenAITokenType)
		require.True(t, ok)
		byType[tt.AsString()] = dp.Sum
	}
	assert.Equal(t, int64(10), byType["input"])
	assert.Equal(t, int64(5), byType["output"])
}

func TestObservability_ConfigurableProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAIResponseJSON("Hi!", "llama-3.3-70b", "stop"))
	}))
	defer srv.Close()

	exporter, tp, reader, mp := newObsProviders(t)
	c := newObsClient(srv.URL+"/", tp, mp)
	c.provider = "groq" // as set by `provider = "groq"` in config

	_, err := c.Call(context.Background(), []cty.Value{buildTestRequest("", "Hello")})
	require.NoError(t, err)

	span := findSpan(t, exporter.GetSpans(), "chat gpt-4o-mini")
	provider, _ := attrValue(span.Attributes, attrGenAIProviderName)
	assert.Equal(t, "groq", provider.AsString())

	dps := histogramDataPoints(t, reader, "gen_ai.client.operation.duration")
	require.Len(t, dps, 1)
	mProvider, _ := attrValue(dps[0].Attributes.ToSlice(), attrGenAIProviderName)
	assert.Equal(t, "groq", mProvider.AsString())
}

// TestObservability_ProviderDefault verifies the provider defaults to "openai"
// when the config attribute is omitted (exercises the config path).
func TestObservability_ProviderDefault(t *testing.T) {
	vcl := []byte(`
client "openai" "gpt" {
    api_key = "test-key"
    model   = "gpt-4o-mini"
}
`)
	config, diags := cfg.NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	c := config.Clients["openai"]["gpt"].(*OpenAIClient)
	assert.Equal(t, "openai", c.provider)
}

// TestObservability_ProviderFromConfig verifies `provider =` flows into the client.
func TestObservability_ProviderFromConfig(t *testing.T) {
	vcl := []byte(`
client "openai" "llm" {
    api_key  = "test-key"
    model    = "llama-3.3-70b"
    provider = "groq"
    base_url = "https://api.groq.com/openai/v1"
}
`)
	config, diags := cfg.NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	c := config.Clients["openai"]["llm"].(*OpenAIClient)
	assert.Equal(t, "groq", c.provider)
	assert.Equal(t, "api.groq.com", c.serverAddress)
	assert.Equal(t, 443, c.serverPort)
}

func TestObservability_ChatAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"bad key","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	exporter, tp, reader, mp := newObsProviders(t)
	c := newObsClient(srv.URL+"/", tp, mp)

	// API errors are returned as an error response value, not a Go error.
	_, err := c.Call(context.Background(), []cty.Value{buildTestRequest("", "Hello")})
	require.NoError(t, err)

	span := findSpan(t, exporter.GetSpans(), "chat gpt-4o-mini")
	assert.Equal(t, codes.Error, span.Status.Code)
	errType, ok := attrValue(span.Attributes, attrErrorType)
	require.True(t, ok)
	assert.Equal(t, "401", errType.AsString())

	// Duration metric carries error.type; no token-usage metric is recorded.
	dps := histogramDataPoints(t, reader, "gen_ai.client.operation.duration")
	require.Len(t, dps, 1)
	mErr, ok := attrValue(dps[0].Attributes.ToSlice(), attrErrorType)
	require.True(t, ok)
	assert.Equal(t, "401", mErr.AsString())
}
