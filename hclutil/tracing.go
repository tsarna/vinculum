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
