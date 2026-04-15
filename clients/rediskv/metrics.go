package rediskv

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// kvMetrics captures the OTel DB-semconv instruments for redis_kv. All
// writes carry db.system.name = "redis" and vinculum.client.name = <block>.
type kvMetrics struct {
	opCount   metric.Int64Counter
	cacheHit  metric.Int64Counter
	cacheMiss metric.Int64Counter
	errors    metric.Int64Counter
	clientTag attribute.KeyValue
}

func newKVMetrics(clientName string, mp metric.MeterProvider) *kvMetrics {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	m := mp.Meter("github.com/tsarna/vinculum/clients/rediskv")
	opCount, _ := m.Int64Counter("db.client.operation.count",
		metric.WithDescription("Number of Redis KV operations performed"))
	hit, _ := m.Int64Counter("vinculum.db.cache.hits",
		metric.WithDescription("GET operations that found a key"))
	miss, _ := m.Int64Counter("vinculum.db.cache.misses",
		metric.WithDescription("GET operations that found no key"))
	errs, _ := m.Int64Counter("vinculum.db.errors",
		metric.WithDescription("Errors from Redis KV operations, labeled by error.type and db.operation.name"))
	return &kvMetrics{
		opCount:   opCount,
		cacheHit:  hit,
		cacheMiss: miss,
		errors:    errs,
		clientTag: attribute.String("vinculum.client.name", clientName),
	}
}

func (m *kvMetrics) RecordOp(ctx context.Context, op string) {
	m.opCount.Add(ctx, 1, metric.WithAttributes(
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
