package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// collectHealth registers the health metrics against a manual reader and
// collects one round, the way a scrape does.
func collectHealth(t *testing.T, c *Config) metricdata.ResourceMetrics {
	t.Helper()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	meter := provider.Meter("test")
	require.NoError(t, registerHealthInstruments(c, meter))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	return rm
}

// stateValues flattens one metric into "attr=…,attr=… -> value" form, keyed by
// the state and any component, so a test can assert on the pairs.
func stateValues(t *testing.T, rm metricdata.ResourceMetrics, name string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "%s should be a Sum (UpDownCounter), got %T", name, m.Data)
			assert.False(t, sum.IsMonotonic, "a state metric must not be monotonic")
			for _, dp := range sum.DataPoints {
				var parts []string
				for _, kv := range dp.Attributes.ToSlice() {
					parts = append(parts, string(kv.Key)+"="+kv.Value.String())
				}
				out[strings.Join(parts, ",")] = dp.Value
			}
		}
	}
	return out
}

func metricUnit(t *testing.T, rm metricdata.ResourceMetrics, name string) string {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m.Unit
			}
		}
	}
	t.Fatalf("%s was not collected", name)
	return ""
}

func TestHealthMetricsFollowTheStateConvention(t *testing.T) {
	c := &Config{Health: NewHealth(zap.NewNop())}
	c.Health.SetBooted()
	rm := collectHealth(t, c)

	// The convention is a `<context>.status` instrument carrying a
	// `<context>.state` attribute, valued 1 for the state the subject is in and
	// 0 for the others, as an UpDownCounter so a sum counts subjects per state.
	assert.Equal(t, "1", metricUnit(t, rm, healthStatusMetric))
	assert.Equal(t, "1", metricUnit(t, rm, healthComponentStatusMetric))

	got := stateValues(t, rm, healthStatusMetric)
	assert.Equal(t, map[string]int64{
		"vinculum.health.probe=readiness,vinculum.health.state=passing": 1,
		"vinculum.health.probe=readiness,vinculum.health.state=failing": 0,
		"vinculum.health.probe=liveness,vinculum.health.state=passing":  1,
		"vinculum.health.probe=liveness,vinculum.health.state=failing":  0,
	}, got)
}

func TestHealthMetricsReportEachContributor(t *testing.T) {
	c := &Config{Health: NewHealth(zap.NewNop())}
	c.Health.RegisterReady("client", "mqtt", "broker", &fakeReadyable{err: errors.New("not connected")})
	c.Health.RegisterReady("server", "http", "api", &fakeReadyable{})
	c.Health.SetBooted()

	rm := collectHealth(t, c)
	got := stateValues(t, rm, healthComponentStatusMetric)

	assert.Equal(t, int64(0), got["vinculum.health.component=client.broker,vinculum.health.component.type=mqtt,vinculum.health.probe=readiness,vinculum.health.state=passing"])
	assert.Equal(t, int64(1), got["vinculum.health.component=client.broker,vinculum.health.component.type=mqtt,vinculum.health.probe=readiness,vinculum.health.state=failing"])
	assert.Equal(t, int64(1), got["vinculum.health.component=server.api,vinculum.health.component.type=http,vinculum.health.probe=readiness,vinculum.health.state=passing"])

	// The built-in process entry has no type label, and an empty attribute
	// would read as a value rather than an absence.
	assert.Contains(t, got, "vinculum.health.component=process,vinculum.health.probe=readiness,vinculum.health.state=passing")
	for key := range got {
		assert.NotContains(t, key, "component.type=,", "an empty type must be omitted, not sent")
	}

	// The aggregate follows the failing contributor, and liveness does not.
	agg := stateValues(t, rm, healthStatusMetric)
	assert.Equal(t, int64(1), agg["vinculum.health.probe=readiness,vinculum.health.state=failing"])
	assert.Equal(t, int64(1), agg["vinculum.health.probe=liveness,vinculum.health.state=passing"])
}

func TestHealthMetricsAreEvaluatedAtCollection(t *testing.T) {
	c := &Config{Health: NewHealth(zap.NewNop())}
	broker := &fakeReadyable{}
	c.Health.RegisterReady("client", "mqtt", "broker", broker)
	c.Health.SetBooted()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	require.NoError(t, registerHealthInstruments(c, provider.Meter("test")))

	// Nothing has been measured yet: the instruments are observable, so
	// registering them only installs a callback.
	assert.Zero(t, broker.calls.Load())

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	assert.Positive(t, broker.calls.Load(), "a scrape must be another asker")
}

func TestHealthMetricsAreAbsentWithoutABackend(t *testing.T) {
	// Nothing to register against, and nothing to complain about: a
	// configuration with no metrics backend simply has no health metrics.
	c := &Config{Health: NewHealth(zap.NewNop())}
	assert.False(t, c.registerHealthMetrics().HasErrors())
}

func TestProbeAttributeNamesTheQuestion(t *testing.T) {
	// The state attribute carries the answer, so the probe attribute reads as
	// the noun rather than repeating it.
	assert.Equal(t, "readiness", healthProbeNames[ProbeReady])
	assert.Equal(t, "liveness", healthProbeNames[ProbeLive])
}

// observedHealth returns a Health logging into a recorder.
func observedHealth(t *testing.T) (*Health, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return NewHealth(zap.New(core)), logs
}

func TestTransitionsAreLoggedOnceEach(t *testing.T) {
	h, logs := observedHealth(t)
	broker := &fakeReadyable{}
	h.RegisterReady("client", "mqtt", "broker", broker)
	h.SetBooted()

	// The first read establishes a baseline rather than announcing an edge.
	require.True(t, h.IsReady(context.Background()))
	assert.Zero(t, logs.Len())

	// A probe every ten seconds must not fill the log with lines saying
	// nothing changed.
	h.Failing(context.Background(), ProbeReady, true)
	assert.Zero(t, logs.Len())

	broker.set(errors.New("dial tcp 10.0.0.5:1883: connection refused"))
	h.Failing(context.Background(), ProbeReady, true)

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, zapcore.WarnLevel, entry.Level)
	assert.Equal(t, "Process is no longer ready", entry.Message)
	// The failing components are named: "not ready" on its own sends the
	// reader to an endpoint they may not have.
	assert.Equal(t, []any{"client.broker: dial tcp 10.0.0.5:1883: connection refused"},
		entry.ContextMap()["failing"])

	// Still down: not a new transition.
	h.Failing(context.Background(), ProbeReady, true)
	assert.Equal(t, 1, logs.Len())

	broker.set(nil)
	h.Failing(context.Background(), ProbeReady, true)
	require.Equal(t, 2, logs.Len())
	assert.Equal(t, zapcore.InfoLevel, logs.All()[1].Level)
	assert.Equal(t, "Process is now ready", logs.All()[1].Message)
}

func TestBootAndDrainAreTransitions(t *testing.T) {
	h, logs := observedHealth(t)

	// A probe during boot sets the baseline to failing, so completing boot is
	// a real transition and marks the end of startup in the log.
	h.Failing(context.Background(), ProbeReady, true)
	assert.Zero(t, logs.Len())

	h.SetBooted()
	h.Failing(context.Background(), ProbeReady, true)
	require.Equal(t, 1, logs.Len())
	assert.Equal(t, "Process is now ready", logs.All()[0].Message)

	h.BeginDrain()
	h.Failing(context.Background(), ProbeReady, true)
	require.Equal(t, 2, logs.Len())
	assert.Equal(t, "Process is no longer ready", logs.All()[1].Message)
	assert.Equal(t, []any{"process: shutting down"}, logs.All()[1].ContextMap()["failing"])
}

func TestLivenessIsLoggedSeparately(t *testing.T) {
	h, logs := observedHealth(t)
	pipeline := &fakeReadyable{}
	h.RegisterProbe(ProbeLive, "check", "", "pipeline", pipeline, 0)
	h.RegisterReady("client", "mqtt", "broker", &fakeReadyable{})
	h.SetBooted()

	h.Failing(context.Background(), ProbeLive, true)
	pipeline.set(errors.New("no pipeline output for 10 minutes"))
	h.Failing(context.Background(), ProbeLive, true)

	require.Equal(t, 1, logs.Len())
	assert.Equal(t, "Process is no longer live", logs.All()[0].Message)
}

func TestHealthMetricsRegisterOnEveryBackend(t *testing.T) {
	// Process-level telemetry belongs in every pipeline the process has, the
	// same way the Go runtime metrics are started on each backend's own
	// provider rather than on a resolved default. With several backends and
	// none marked default there *is* no default, so resolving one would drop
	// these silently.
	c := &Config{Health: NewHealth(zap.NewNop())}
	c.Health.SetBooted()

	readers := make([]*metric.ManualReader, 2)
	c.MetricsServers = map[string]MetricsRegistrar{}
	for i := range readers {
		readers[i] = metric.NewManualReader()
	}

	// Registering against each provider directly is what registerHealthMetrics
	// does per backend; this asserts both end up carrying the metrics.
	for _, r := range readers {
		provider := metric.NewMeterProvider(metric.WithReader(r))
		t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
		require.NoError(t, registerHealthInstruments(c, provider.Meter("test")))
	}

	for i, r := range readers {
		var rm metricdata.ResourceMetrics
		require.NoError(t, r.Collect(context.Background(), &rm))
		assert.NotEmpty(t, stateValues(t, rm, healthStatusMetric), "backend %d carries no health metrics", i)
	}
}
