package types

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Tests for the gauge as an observable instrument: that the map it keeps is
// what gets reported, that the reported wire type is a Gauge, and that a
// series which stops being written goes away rather than reporting its last
// value forever.

// newObservedGauge builds a gauge and registers Collect as the callback of an
// observable instrument against a manual reader -- the same wiring
// config.MetricBlockHandler.Process does.
func newObservedGauge(t *testing.T, name string, attrKeys []attribute.Key, staleAfter time.Duration) (*GaugeMetric, sdkmetric.Reader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	m := NewGaugeMetric(attrKeys, staleAfter)
	_, err := mp.Meter("test").Float64ObservableGauge(name, otelmetric.WithFloat64Callback(m.Collect))
	require.NoError(t, err)
	return m, reader
}

// gaugePoints returns the data points of the named metric, or nil when the
// metric was not exported at all. It also pins the wire type: a gauge must
// arrive as a Gauge, not as the non-monotonic cumulative Sum it used to be.
func gaugePoints(t *testing.T, rm metricdata.ResourceMetrics, name string) []metricdata.DataPoint[float64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[float64])
			require.True(t, ok, "metric %q is not a float64 Gauge, got %T", name, m.Data)
			return g.DataPoints
		}
	}
	return nil
}

// labelsFor is the cty object a labeled recording call takes.
func labelsFor(key, val string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{key: cty.StringVal(val)})
}

// --- wire type and basic reporting ---

// The instrument type is the whole point of the change, so it gets its own
// assertion rather than only riding along inside gaugePoints.
func TestGaugeMetric_IsReportedAsAGauge(t *testing.T) {
	m, reader := newObservedGauge(t, "wire_type", nil, 0)
	_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(3)})
	require.NoError(t, err)

	rm := collectInto(t, reader)
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			if md.Name != "wire_type" {
				continue
			}
			_, isSum := md.Data.(metricdata.Sum[float64])
			assert.False(t, isSum, "a gauge must no longer be emitted as a Sum")
			_, isGauge := md.Data.(metricdata.Gauge[float64])
			assert.True(t, isGauge)
			return
		}
	}
	t.Fatal("metric was not exported at all")
}

// A gauge that has never been written reports nothing. Declaring a metric is
// not a claim about its value, and a gauge that sprang into existence at 0
// would make `absent()` useless for spotting a scrape that never ran.
func TestGaugeMetric_UnwrittenIsAbsent(t *testing.T) {
	_, reader := newObservedGauge(t, "never_written", nil, 0)
	assert.Nil(t, gaugePoints(t, collectInto(t, reader), "never_written"))
}

// Setting the value outright rather than accumulating deltas: a long sequence
// of writes must land exactly on the last one.
func TestGaugeMetric_ReportsTheValueNotASumOfDeltas(t *testing.T) {
	m, reader := newObservedGauge(t, "exact", nil, 0)
	for _, v := range []float64{0.1, 0.2, 0.3, 1e9, 0.7} {
		_, err := m.Set(context.Background(), []cty.Value{cty.NumberFloatVal(v)})
		require.NoError(t, err)
	}

	pts := gaugePoints(t, collectInto(t, reader), "exact")
	require.Len(t, pts, 1)
	assert.Equal(t, 0.7, pts[0].Value, "the reported value must be the value, exactly")
}

func TestGaugeMetric_IncrementAccumulates(t *testing.T) {
	m, reader := newObservedGauge(t, "incr", []attribute.Key{"q"}, 0)
	for range 3 {
		_, err := m.Increment(context.Background(), []cty.Value{cty.NumberIntVal(2), labelsFor("q", "a")})
		require.NoError(t, err)
	}
	_, err := m.Increment(context.Background(), []cty.Value{cty.NumberIntVal(-1), labelsFor("q", "a")})
	require.NoError(t, err)

	pts := gaugePoints(t, collectInto(t, reader), "incr")
	require.Len(t, pts, 1)
	assert.Equal(t, 5.0, pts[0].Value)
}

// store reuses the attribute.Set already held under a key instead of rebuilding
// it, so a rewritten series must still be reported under its own labels, and
// two label sets must not bleed into one another however often either is
// rewritten.
func TestGaugeMetric_RewritingASeriesKeepsItsLabels(t *testing.T) {
	m, reader := newObservedGauge(t, "labelled", []attribute.Key{"account"}, 0)
	for i, acct := range []string{"a", "b", "a", "b", "a"} {
		_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(int64(i)), labelsFor("account", acct)})
		require.NoError(t, err)
	}

	byAccount := map[string]float64{}
	for _, pt := range gaugePoints(t, collectInto(t, reader), "labelled") {
		v, ok := pt.Attributes.Value("account")
		require.True(t, ok, "series lost its label")
		assert.Equal(t, 1, pt.Attributes.Len(), "series grew an attribute it was never given")
		byAccount[v.AsString()] = pt.Value
	}
	assert.Equal(t, map[string]float64{"a": 4, "b": 3}, byAccount)
}

// --- stale_after ---

// fakeClock drives staleness without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClockedGauge(t *testing.T, name string, attrKeys []attribute.Key, staleAfter time.Duration) (*GaugeMetric, sdkmetric.Reader, *fakeClock) {
	t.Helper()
	m, reader := newObservedGauge(t, name, attrKeys, staleAfter)
	clock := &fakeClock{t: time.Now()}
	m.now = clock.now
	return m, reader, clock
}

// The case stale_after exists for: a scrape that used to report an account
// stops mentioning it, and the series ends instead of latching at its last
// value. The other account, still being written, is untouched.
func TestGaugeMetric_StaleSeriesIsWithdrawn(t *testing.T) {
	m, reader, clock := newClockedGauge(t, "stale_one", []attribute.Key{"who"}, 10*time.Minute)

	_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(1), labelsFor("who", "gone")})
	require.NoError(t, err)
	_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(1), labelsFor("who", "kept")})
	require.NoError(t, err)
	require.Len(t, gaugePoints(t, collectInto(t, reader), "stale_one"), 2)

	// Five minutes on, only "kept" is rewritten. Nothing has expired yet.
	clock.advance(5 * time.Minute)
	_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(1), labelsFor("who", "kept")})
	require.NoError(t, err)
	require.Len(t, gaugePoints(t, collectInto(t, reader), "stale_one"), 2)

	// Six more, and "gone" is now 11 minutes old.
	clock.advance(6 * time.Minute)
	pts := gaugePoints(t, collectInto(t, reader), "stale_one")
	require.Len(t, pts, 1, "the series nobody rewrote must be gone")
	kept, ok := pointFor(pts, "who", "kept")
	require.True(t, ok, "the series still being written must survive")
	assert.Equal(t, 1.0, kept.Value)
}

// Staleness is not a one-way door: writing the label set again brings it back.
func TestGaugeMetric_StaleSeriesReturnsOnNextWrite(t *testing.T) {
	m, reader, clock := newClockedGauge(t, "stale_return", []attribute.Key{"who"}, time.Minute)

	_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(1), labelsFor("who", "x")})
	require.NoError(t, err)

	clock.advance(2 * time.Minute)
	require.Nil(t, gaugePoints(t, collectInto(t, reader), "stale_return"))

	_, err = m.Set(context.Background(), []cty.Value{cty.NumberIntVal(7), labelsFor("who", "x")})
	require.NoError(t, err)
	pts := gaugePoints(t, collectInto(t, reader), "stale_return")
	require.Len(t, pts, 1)
	assert.Equal(t, 7.0, pts[0].Value)
}

// A stale series is gone, not merely unreported: get() reads 0 and
// increment() starts a new series from 0 rather than resuming the old total.
// Resuming would make a gap in the data silently invisible in the value.
func TestGaugeMetric_StaleIsAbsentToGetAndIncrement(t *testing.T) {
	m, _, clock := newClockedGauge(t, "stale_reads", []attribute.Key{"who"}, time.Minute)

	_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(10), labelsFor("who", "x")})
	require.NoError(t, err)

	got, err := m.Get(context.Background(), []cty.Value{labelsFor("who", "x")})
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.NumberFloatVal(10)))

	clock.advance(2 * time.Minute)

	got, err = m.Get(context.Background(), []cty.Value{labelsFor("who", "x")})
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.NumberFloatVal(0)), "a stale series reads as absent, got %#v", got)

	cur, err := m.Increment(context.Background(), []cty.Value{cty.NumberIntVal(1), labelsFor("who", "x")})
	require.NoError(t, err)
	assert.True(t, cur.RawEquals(cty.NumberFloatVal(1)), "increment must restart from 0, got %#v", cur)
}

// The unlabeled series expires on the same rule as a labeled one.
func TestGaugeMetric_StaleAppliesToTheUnlabeledSeries(t *testing.T) {
	m, reader, clock := newClockedGauge(t, "stale_nolabel", nil, time.Minute)

	_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(4)})
	require.NoError(t, err)
	require.Len(t, gaugePoints(t, collectInto(t, reader), "stale_nolabel"), 1)

	clock.advance(2 * time.Minute)
	assert.Nil(t, gaugePoints(t, collectInto(t, reader), "stale_nolabel"))
}

// Without stale_after nothing ever expires, which is what every gauge did
// before this attribute existed and what they must keep doing by default.
func TestGaugeMetric_NoStaleAfterNeverExpires(t *testing.T) {
	m, reader, clock := newClockedGauge(t, "no_stale", []attribute.Key{"who"}, 0)

	_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(1), labelsFor("who", "x")})
	require.NoError(t, err)

	clock.advance(30 * 24 * time.Hour)
	assert.Len(t, gaugePoints(t, collectInto(t, reader), "no_stale"), 1)
}

// Collection prunes what it withdraws, so a gauge fed unbounded label values
// does not grow a map entry per value seen for the lifetime of the process.
func TestGaugeMetric_CollectPrunesExpiredEntries(t *testing.T) {
	m, reader, clock := newClockedGauge(t, "prune", []attribute.Key{"who"}, time.Minute)

	for _, who := range []string{"a", "b", "c"} {
		_, err := m.Set(context.Background(), []cty.Value{cty.NumberIntVal(1), labelsFor("who", who)})
		require.NoError(t, err)
	}
	require.Len(t, m.labelVals, 3)

	clock.advance(2 * time.Minute)
	collectInto(t, reader)
	assert.Empty(t, m.labelVals, "expired entries must be dropped, not merely skipped")
}
