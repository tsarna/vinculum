package config

import (
	"context"
	"testing"
	"time"

	"github.com/tsarna/vinculum/types"
	"go.opentelemetry.io/otel/metric"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestMetricPollScope covers what a computed metric's `value` can see. It used
// to be evaluated against the bare global namespace with no `ctx` at all, so
// the expression could not call any function that takes one — no http::get()
// to poll, no send() to report, no log::warn() when it failed.
func TestMetricPollScope(t *testing.T) {
	config, _ := newHookTestConfig(t)
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	scope, err := config.metricPollScope("queue_depth", tp)(context.Background())
	require.NoError(t, err)
	require.NotNil(t, scope.EvalCtx)

	ctxVal, ok := scope.EvalCtx.Variables["ctx"]
	require.True(t, ok, "the expression must see a ctx")
	attrs := ctxVal.AsValueMap()

	assert.Equal(t, "queue_depth", attrs["metric"].AsString())

	// A poll is an autonomous timer event: nobody called it, so there is no
	// identity to carry, but the field is present rather than missing.
	require.Contains(t, attrs, "auth")
	assert.True(t, attrs["auth"].IsNull())

	// The span is what an http::get() inside the expression hangs off, so the
	// ctx must carry it rather than leaving such calls to emit orphans.
	span := trace.SpanFromContext(scope.Ctx)
	require.True(t, span.SpanContext().IsValid())
	assert.Equal(t, span.SpanContext().TraceID().String(), attrs["trace_id"].AsString())

	assert.Empty(t, exporter.GetSpans(), "the span stays open until the poll finishes")
	scope.Done(nil)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "metric.poll queue_depth", spans[0].Name)
}

// TestMetricPollScopeRecordsFailure covers the error path: a poll that could
// not produce a value marks its span, so a broken expression is visible in the
// trace and not only in the log.
func TestMetricPollScopeRecordsFailure(t *testing.T) {
	config, _ := newHookTestConfig(t)
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	scope, err := config.metricPollScope("broken", tp)(context.Background())
	require.NoError(t, err)
	scope.Done(assert.AnError)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "Error", spans[0].Status.Code.String())
	require.NotEmpty(t, spans[0].Events, "the error is recorded on the span")
}

// TestComputedMetricEvalErrorGoesToUserLogger covers which logger reports a
// broken `value`. A user's expression failing is a VCL error, not an internal
// one, so the Go caller and stacktrace are noise: they always point at the same
// generic polling plumbing and say nothing about the expression that failed.
func TestComputedMetricEvalErrorGoesToUserLogger(t *testing.T) {
	config, logs := newHookTestConfig(t)

	// An expression referring to something that does not exist fails on every
	// poll, forever — which is exactly the case that used to print a stacktrace
	// pointing into types/metric.go.
	m := types.NewComputedGaugeMetric(
		noopFloat64UpDownCounter{},
		parseExpr(t, `no_such_thing`),
		config.metricPollScope("broken", nil),
		config.UserLogger,
		time.Hour,
	)
	m.Start(context.Background())

	require.Eventually(t, func() bool {
		return len(logs.FilterMessageSnippet("expression evaluation failed").All()) > 0
	}, time.Second, 5*time.Millisecond)

	entry := logs.FilterMessageSnippet("expression evaluation failed").All()[0]
	assert.Contains(t, entry.Message, "computed gauge")
	assert.Empty(t, entry.Stack, "a VCL error must not carry a Go stacktrace")
	assert.False(t, entry.Caller.Defined, "nor a Go caller")
}

// noopFloat64UpDownCounter records nothing; the test is about the eval path.
type noopFloat64UpDownCounter struct{ metric.Float64UpDownCounter }

func (noopFloat64UpDownCounter) Add(context.Context, float64, ...metric.AddOption) {}
