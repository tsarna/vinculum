package openai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// instrumentationScope is the OTel instrumentation scope used for the OpenAI
// client's tracer and meter.
const instrumentationScope = "github.com/tsarna/vinculum/clients/openai"

// Attribute keys from the OpenTelemetry GenAI semantic conventions
// (model/gen-ai/*.yaml in open-telemetry/semantic-conventions-genai). These are
// still marked "development" upstream and not yet in the Go `semconv` package,
// so they are defined here as string constants.
const (
	attrGenAIOperationName      = "gen_ai.operation.name"
	attrGenAIProviderName       = "gen_ai.provider.name"
	attrGenAIRequestModel       = "gen_ai.request.model"
	attrGenAIRequestMaxTokens   = "gen_ai.request.max_tokens"
	attrGenAIRequestTemperature = "gen_ai.request.temperature"
	attrGenAIResponseModel      = "gen_ai.response.model"
	attrGenAIResponseID         = "gen_ai.response.id"
	attrGenAIFinishReasons      = "gen_ai.response.finish_reasons"
	attrGenAIUsageInputTokens   = "gen_ai.usage.input_tokens"
	attrGenAIUsageOutputTokens  = "gen_ai.usage.output_tokens"
	attrGenAITokenType          = "gen_ai.token.type"
	attrErrorType               = "error.type"
	attrServerAddress           = "server.address"
	attrServerPort              = "server.port"
)

// Well-known attribute values for the chat-completion operation.
const (
	operationChat   = "chat"
	providerOpenAI  = "openai"
	tokenTypeInput  = "input"
	tokenTypeOutput = "output"
)

// openaiMetrics holds the OTel GenAI client instruments.
type openaiMetrics struct {
	// opDuration records gen_ai.client.operation.duration (seconds).
	opDuration metric.Float64Histogram
	// tokenUsage records gen_ai.client.token.usage ({token}), split by
	// gen_ai.token.type into input and output.
	tokenUsage metric.Int64Histogram
}

// newOpenAIMetrics builds the GenAI client instruments from the given
// MeterProvider. A nil provider yields no-op instruments so the client runs
// without metrics configured.
func newOpenAIMetrics(mp metric.MeterProvider) *openaiMetrics {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	meter := mp.Meter(instrumentationScope)
	opDuration, _ := meter.Float64Histogram(
		"gen_ai.client.operation.duration",
		metric.WithUnit("s"),
		metric.WithDescription("GenAI operation duration."),
	)
	tokenUsage, _ := meter.Int64Histogram(
		"gen_ai.client.token.usage",
		metric.WithUnit("{token}"),
		metric.WithDescription("Number of input and output tokens used."),
	)
	return &openaiMetrics{opDuration: opDuration, tokenUsage: tokenUsage}
}

// withAttrs returns a fresh slice of base plus extra, so callers can derive
// per-record attribute sets without aliasing the shared base slice.
func withAttrs(base []attribute.KeyValue, extra ...attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}
