package hclutil

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// StartTriggerSpan starts a span for a single trigger execution using the
// provided context as parent (pass context.Background() for root spans).
// tp is the TracerProvider to use; if nil the global provider is used.
// The returned context carries the span; the stop function must be called
// when the execution completes to end the span and record any error.
func StartTriggerSpan(ctx context.Context, tp trace.TracerProvider, triggerType, name string) (context.Context, func(error)) {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	tracer := tp.Tracer("vinculum/triggers")
	ctx, span := tracer.Start(ctx, "trigger."+triggerType+" "+name)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// StartMetricPollSpan starts a span for one poll of a computed metric. Like a
// trigger execution it is an autonomous root event on a timer, so pass
// context.Background() as parent; tp nil uses the global provider.
//
// The span exists so that whatever the `value` expression does — an HTTP call,
// a query — has something to hang off, rather than emitting orphan spans.
func StartMetricPollSpan(ctx context.Context, tp trace.TracerProvider, name string) (context.Context, func(error)) {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	tracer := tp.Tracer("vinculum/metrics")
	ctx, span := tracer.Start(ctx, "metric.poll "+name)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// StartLinkedTriggerSpan starts a new root span for an async trigger or
// action that will outlive its caller. The returned context has the caller's
// cancellation severed (via context.WithoutCancel) so an upstream ctx
// cancellation — e.g. an HTTP request completing after spawning the
// dispatch goroutine — cannot interrupt the dispatched work mid-flight.
// The new span carries a Link back to the caller's span (if any) for
// trace correlation, per OTel async-messaging conventions.
//
// Use this from any goroutine spawned on behalf of a caller whose ctx may
// be short-lived. For synchronous dispatch or autonomous root events
// (timer callbacks, signal handlers), use StartTriggerSpan.
func StartLinkedTriggerSpan(ctx context.Context, tp trace.TracerProvider, triggerType, name string) (context.Context, func(error)) {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	tracer := tp.Tracer("vinculum/triggers")
	link := trace.LinkFromContext(ctx)
	ctx = context.WithoutCancel(ctx)
	ctx, span := tracer.Start(ctx, "trigger."+triggerType+" "+name,
		trace.WithNewRoot(),
		trace.WithLinks(link),
	)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}
