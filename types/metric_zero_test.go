package types

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Regression tests for a gauge/counter whose FIRST value is 0.
//
// Both are emulated on top of cumulative OTel instruments by tracking the
// previous value and Adding the delta. A first write of 0 produces a delta of
// 0 against the zero-valued cache, so the Add used to be skipped entirely and
// the instrument never materialized the series. The metric was then absent
// rather than zero -- indistinguishable from a thing that does not exist,
// which defeats the whole point of a zero-valued gauge (and silently breaks
// `== 0` alerting, `sum()` panels and `absent()` rules built on it).

func collectInto(t *testing.T, reader sdkmetric.Reader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	return rm
}

// sumPoints returns the data points of the named metric, or nil if the metric
// was never exported at all.
func sumPoints(t *testing.T, rm metricdata.ResourceMetrics, name string) []metricdata.DataPoint[float64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			s, ok := m.Data.(metricdata.Sum[float64])
			require.True(t, ok, "metric %q is not a float64 Sum", name)
			return s.DataPoints
		}
	}
	return nil
}

func pointFor(pts []metricdata.DataPoint[float64], key, val string) (metricdata.DataPoint[float64], bool) {
	for _, p := range pts {
		if v, ok := p.Attributes.Value(attribute.Key(key)); ok && v.AsString() == val {
			return p, true
		}
	}
	return metricdata.DataPoint[float64]{}, false
}

func TestGaugeMetric_FirstValueZeroIsExported(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	inst, err := mp.Meter("test").Float64UpDownCounter("zero_gauge")
	require.NoError(t, err)
	m := NewGaugeMetric(inst, []attribute.Key{})

	_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(0)})
	require.NoError(t, err)

	pts := sumPoints(t, collectInto(t, reader), "zero_gauge")
	require.Len(t, pts, 1, "a gauge set to 0 must still be exported, not absent")
	assert.Equal(t, 0.0, pts[0].Value)
}

func TestGaugeMetric_FirstValueZeroIsExported_Labeled(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	inst, err := mp.Meter("test").Float64UpDownCounter("zero_gauge_labeled")
	require.NoError(t, err)
	m := NewGaugeMetric(inst, []attribute.Key{"which"})

	labels := func(v string) cty.Value {
		return cty.ObjectVal(map[string]cty.Value{"which": cty.StringVal(v)})
	}

	// "zero" is written 0 first; "one" is written 1. Before the fix only
	// "one" ever appeared.
	_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(0), labels("zero")})
	require.NoError(t, err)
	_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(1), labels("one")})
	require.NoError(t, err)

	pts := sumPoints(t, collectInto(t, reader), "zero_gauge_labeled")
	require.Len(t, pts, 2, "both attribute sets must be exported")

	zero, ok := pointFor(pts, "which", "zero")
	require.True(t, ok, `attribute set which="zero" must be exported`)
	assert.Equal(t, 0.0, zero.Value)

	one, ok := pointFor(pts, "which", "one")
	require.True(t, ok)
	assert.Equal(t, 1.0, one.Value)
}

// A gauge that goes 0 -> 5 -> 0 must report 0 at the end, not stay at 5.
func TestGaugeMetric_ZeroAfterNonZeroStillTracks(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	inst, err := mp.Meter("test").Float64UpDownCounter("round_trip")
	require.NoError(t, err)
	m := NewGaugeMetric(inst, []attribute.Key{})

	for _, v := range []int64{0, 5, 0} {
		_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(v)})
		require.NoError(t, err)
	}

	pts := sumPoints(t, collectInto(t, reader), "round_trip")
	require.Len(t, pts, 1)
	assert.Equal(t, 0.0, pts[0].Value)
}

func TestCounterMetric_FirstValueZeroIsExported(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	inst, err := mp.Meter("test").Float64Counter("zero_counter")
	require.NoError(t, err)
	m := NewCounterMetric(inst, []attribute.Key{})

	_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(0)})
	require.NoError(t, err)

	pts := sumPoints(t, collectInto(t, reader), "zero_counter")
	require.Len(t, pts, 1, "a counter sitting at 0 must still be exported, not absent")
	assert.Equal(t, 0.0, pts[0].Value)
}

// Materializing at 0 must not let a counter run backwards afterwards.
func TestCounterMetric_StaysMonotonicAfterZeroStart(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	inst, err := mp.Meter("test").Float64Counter("mono_counter")
	require.NoError(t, err)
	m := NewCounterMetric(inst, []attribute.Key{})

	for _, v := range []int64{0, 7, 3} { // the 3 is a regression, must be ignored
		_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(v)})
		require.NoError(t, err)
	}

	pts := sumPoints(t, collectInto(t, reader), "mono_counter")
	require.Len(t, pts, 1)
	assert.Equal(t, 7.0, pts[0].Value, "counter must not move backwards")
}
