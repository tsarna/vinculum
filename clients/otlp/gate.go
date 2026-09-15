package otlp

import (
	"context"
	"sync/atomic"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// exportGate drops every export made before the client starts.
//
// The providers have to exist when the block is processed, because the metric
// blocks processed after it take the MeterProvider then. But a provider starts
// working as soon as it exists: the periodic reader exports on its interval,
// and shutting it down exports once more. Building a config without running it,
// as `vinculum check` and man::check do, would otherwise POST to whatever
// endpoint the config names, with whatever headers it gives. The gate keeps the
// providers where the metric blocks need them and the network behind Start.
//
// Nothing is lost in a process that runs: nothing records telemetry before the
// startables run, and metrics are cumulative, so an export dropped before Start
// is carried by the next one.
type exportGate struct{ open atomic.Bool }

type gatedSpanExporter struct {
	sdktrace.SpanExporter
	gate *exportGate
}

func (e gatedSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if !e.gate.open.Load() {
		return nil
	}
	return e.SpanExporter.ExportSpans(ctx, spans)
}

type gatedMetricExporter struct {
	sdkmetric.Exporter
	gate *exportGate
}

func (e gatedMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	if !e.gate.open.Load() {
		return nil
	}
	return e.Exporter.Export(ctx, rm)
}
