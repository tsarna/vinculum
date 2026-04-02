package hclutil_test

import (
	"context"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/hclutil"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func emptyEvalCtx() *hcl.EvalContext {
	return &hcl.EvalContext{}
}

func TestTraceIDSpanID_NoSpan(t *testing.T) {
	ctx := context.Background()
	evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(emptyEvalCtx())
	require.NoError(t, err)

	ctxVal := evalCtx.Variables["ctx"]
	require.False(t, ctxVal.IsNull())

	traceID := ctxVal.GetAttr("trace_id")
	spanID := ctxVal.GetAttr("span_id")

	assert.Equal(t, "", traceID.AsString(), "trace_id should be empty with no active span")
	assert.Equal(t, "", spanID.AsString(), "span_id should be empty with no active span")
}

func TestTraceIDSpanID_WithSpan(t *testing.T) {
	// Set up an in-memory exporter and tracer provider
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exporter))
	tracer := tp.Tracer("test")

	ctx := context.Background()
	ctx, span := tracer.Start(ctx, "test-span")
	defer span.End()

	sc := span.SpanContext()
	require.True(t, sc.IsValid())

	evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(emptyEvalCtx())
	require.NoError(t, err)

	ctxVal := evalCtx.Variables["ctx"]
	require.False(t, ctxVal.IsNull())

	traceIDVal := ctxVal.GetAttr("trace_id")
	spanIDVal := ctxVal.GetAttr("span_id")

	assert.Equal(t, sc.TraceID().String(), traceIDVal.AsString())
	assert.Equal(t, sc.SpanID().String(), spanIDVal.AsString())
}

// TestTraceIDSpanID_RemoteSpanContext simulates a scenario that was
// broken: otelhttp extracted a traceparent header and stored the remote
// SpanContext in the Go context, but no local span was started (NOOP tracer,
// i.e. no client "otlp" configured). trace_id/span_id should still be
// populated from the remote span context.
func TestTraceIDSpanID_RemoteSpanContext(t *testing.T) {
	// Build a remote SpanContext as otelhttp would after extracting traceparent.
	traceID, err := oteltrace.TraceIDFromHex("80e1afed08e019fc1110464cfa66635c")
	require.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("7a085853722dc6d2")
	require.NoError(t, err)

	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	require.True(t, sc.IsValid())

	// Store the remote span context the same way otelhttp does after propagation extraction.
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), sc)

	evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(emptyEvalCtx())
	require.NoError(t, err)

	ctxVal := evalCtx.Variables["ctx"]
	assert.Equal(t, traceID.String(), ctxVal.GetAttr("trace_id").AsString())
	assert.Equal(t, spanID.String(), ctxVal.GetAttr("span_id").AsString())
}

func TestTraceIDSpanID_UnsamledSpan(t *testing.T) {
	// A NOOP span (from the global NOOP provider) has an invalid SpanContext
	ctx := context.Background()
	noopSpan := oteltrace.SpanFromContext(ctx) // always NOOP when no provider active
	assert.False(t, noopSpan.SpanContext().IsValid())

	evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(emptyEvalCtx())
	require.NoError(t, err)

	ctxVal := evalCtx.Variables["ctx"]
	assert.Equal(t, "", ctxVal.GetAttr("trace_id").AsString())
	assert.Equal(t, "", ctxVal.GetAttr("span_id").AsString())
}
