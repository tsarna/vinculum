package rediskv

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// kvMetrics captures the OTel DB-semconv instruments for redis_kv. All
// writes carry db.system.name = "redis" and vinculum.client.name = <block>.
type kvMetrics struct {
	opCount    metric.Int64Counter
	opDuration metric.Float64Histogram
	cacheHit   metric.Int64Counter
	cacheMiss  metric.Int64Counter
	errors     metric.Int64Counter
	clientTag  attribute.KeyValue
}

func newKVMetrics(clientName string, mp metric.MeterProvider) *kvMetrics {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	m := mp.Meter("github.com/tsarna/vinculum/clients/rediskv")
	opCount, _ := m.Int64Counter("db.client.operation.count",
		metric.WithDescription("Number of Redis KV operations performed"))
	opDur, _ := m.Float64Histogram("db.client.operation.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Per-operation Redis KV round-trip duration, seconds"))
	hit, _ := m.Int64Counter("vinculum.db.cache.hits",
		metric.WithDescription("GET operations that found a key"))
	miss, _ := m.Int64Counter("vinculum.db.cache.misses",
		metric.WithDescription("GET operations that found no key"))
	errs, _ := m.Int64Counter("vinculum.db.errors",
		metric.WithDescription("Errors from Redis KV operations, labeled by error.type and db.operation.name"))
	return &kvMetrics{
		opCount:    opCount,
		opDuration: opDur,
		cacheHit:   hit,
		cacheMiss:  miss,
		errors:     errs,
		clientTag:  attribute.String("vinculum.client.name", clientName),
	}
}

func (m *kvMetrics) RecordOp(ctx context.Context, op string) {
	m.opCount.Add(ctx, 1, metric.WithAttributes(
		attribute.String("db.system.name", "redis"),
		attribute.String("db.operation.name", op),
		m.clientTag,
	))
}

// Timed increments the operation counter and returns a stop function that
// records the elapsed duration when called. Typical use:
//
//	defer c.metrics.Timed(ctx, "GET")()
func (m *kvMetrics) Timed(ctx context.Context, op string) func() {
	m.RecordOp(ctx, op)
	start := time.Now()
	return func() {
		m.RecordDuration(ctx, op, time.Since(start).Seconds())
	}
}

func (m *kvMetrics) RecordDuration(ctx context.Context, op string, seconds float64) {
	m.opDuration.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("db.system.name", "redis"),
		attribute.String("db.operation.name", op),
		m.clientTag,
	))
}

func (m *kvMetrics) RecordHit(ctx context.Context)  { m.cacheHit.Add(ctx, 1, metric.WithAttributes(m.clientTag)) }
func (m *kvMetrics) RecordMiss(ctx context.Context) { m.cacheMiss.Add(ctx, 1, metric.WithAttributes(m.clientTag)) }

func (m *kvMetrics) RecordError(ctx context.Context, op, errType string) {
	m.errors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("db.system.name", "redis"),
		attribute.String("db.operation.name", op),
		attribute.String("error.type", errType),
		m.clientTag,
	))
}
