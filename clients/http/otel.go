// OpenTelemetry wiring for the HTTP client.
//
// Spans, header propagation, and metrics are layered onto the base
// transport via go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp.
//
// Per-call propagation suppression is implemented as a custom propagator
// that wraps the configured one and short-circuits Inject when a marker
// is set on the request context. This avoids the alternative of building
// two transports per client (one with propagation, one without) and
// picking at request time.

package http

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// otelPropagateCtxKey gates per-call propagation suppression. A value of
// true means "skip injecting trace/baggage headers for this request".
type otelPropagateCtxKey struct{}

// WithOTelPropagate returns a derived context that overrides the
// client's default propagation setting for one request. Pass false to
// suppress; pass true to force on (rare — usually the client default
// already enables it).
//
// The verb function uses this to thread per-call opts.otel.propagate
// through to the conditional propagator without rebuilding the
// transport stack.
func WithOTelPropagate(ctx context.Context, propagate bool) context.Context {
	return context.WithValue(ctx, otelPropagateCtxKey{}, propagate)
}

// otelPropagateFromContext returns (override, true) if a per-call value
// was set, or (false, false) if not. The verb function only sets the
// value when it has a per-call override; otherwise the client default
// (encoded into the propagator wrapper) wins.
func otelPropagateFromContext(ctx context.Context) (bool, bool) {
	v := ctx.Value(otelPropagateCtxKey{})
	if v == nil {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// conditionalPropagator wraps a TextMapPropagator and consults the
// request context (and a static client default) before injecting. It
// implements propagation.TextMapPropagator.
//
// The default field is the client-level setting. The per-call ctx value
// (if set) overrides it for that one request.
type conditionalPropagator struct {
	inner   propagation.TextMapPropagator
	default_ bool
}

func newConditionalPropagator(inner propagation.TextMapPropagator, defaultPropagate bool) propagation.TextMapPropagator {
	if inner == nil {
		inner = otel.GetTextMapPropagator()
	}
	return conditionalPropagator{inner: inner, default_: defaultPropagate}
}

func (p conditionalPropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	propagate := p.default_
	if v, ok := otelPropagateFromContext(ctx); ok {
		propagate = v
	}
	if !propagate {
		return
	}
	p.inner.Inject(ctx, carrier)
}

func (p conditionalPropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	// Extract is called on incoming requests; the HTTP client only
	// uses Inject. We delegate so the propagator is also reusable as a
	// general-purpose composite if needed.
	return p.inner.Extract(ctx, carrier)
}

func (p conditionalPropagator) Fields() []string {
	return p.inner.Fields()
}

// wrapTransportWithOTel wraps base with otelhttp instrumentation
// configured for this client. The propagator is the
// conditionalPropagator built from defaultPropagate; the meter and
// tracer providers come from the global config wiring (passing nil
// makes otelhttp use the global default, or a no-op if none is set).
//
// We always wrap, even when both providers are nil and propagation is
// disabled. The reason is per-call opts.otel.propagate: a request may
// flip propagation back on at call time, so the conditional propagator
// has to be installed up front for that override to have anywhere to
// take effect.
func wrapTransportWithOTel(base http.RoundTripper, tp trace.TracerProvider, mp metric.MeterProvider, defaultPropagate bool) http.RoundTripper {
	opts := []otelhttp.Option{
		otelhttp.WithPropagators(newConditionalPropagator(otel.GetTextMapPropagator(), defaultPropagate)),
	}
	if tp != nil {
		opts = append(opts, otelhttp.WithTracerProvider(tp))
	}
	if mp != nil {
		opts = append(opts, otelhttp.WithMeterProvider(mp))
	}
	return otelhttp.NewTransport(base, opts...)
}
