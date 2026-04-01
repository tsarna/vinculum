package types

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func newTestGaugeMetric(t *testing.T) *GaugeMetric {
	t.Helper()
	reg := prometheus.NewRegistry()
	vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_gauge", Help: "test"}, []string{})
	reg.MustRegister(vec)
	return NewGaugeMetric(vec, []string{})
}

func newTestCounterMetric(t *testing.T) *CounterMetric {
	t.Helper()
	reg := prometheus.NewRegistry()
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_counter", Help: "test"}, []string{})
	reg.MustRegister(vec)
	return NewCounterMetric(vec, []string{})
}

// --- GaugeMetric ---

func TestGaugeMetric_Set_Notifies(t *testing.T) {
	m := newTestGaugeMetric(t)
	w := &testWatcher{}
	m.Watch(w)

	_, err := m.Set(bg, []cty.Value{cty.NumberFloatVal(3.14)})
	require.NoError(t, err)

	assert.Equal(t, 1, w.count())
	c := w.get(0)
	assert.True(t, c.old.RawEquals(cty.NumberFloatVal(0)))   // initial cache is 0
	assert.True(t, c.new.RawEquals(cty.NumberFloatVal(3.14)))
}

func TestGaugeMetric_Set_EqualValueStillNotifies(t *testing.T) {
	m := newTestGaugeMetric(t)
	w := &testWatcher{}
	m.Watch(w)

	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(5)})
	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(5)})
	assert.Equal(t, 2, w.count())
}

func TestGaugeMetric_Set_OldValueIsCorrect(t *testing.T) {
	m := newTestGaugeMetric(t)
	w := &testWatcher{}
	m.Watch(w)

	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(10)})
	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(20)})

	assert.Equal(t, 2, w.count())
	assert.True(t, w.get(0).old.RawEquals(cty.NumberFloatVal(0)))
	assert.True(t, w.get(0).new.RawEquals(cty.NumberIntVal(10)))
	assert.True(t, w.get(1).old.RawEquals(cty.NumberIntVal(10)))
	assert.True(t, w.get(1).new.RawEquals(cty.NumberIntVal(20)))
}

func TestGaugeMetric_Increment_Notifies(t *testing.T) {
	m := newTestGaugeMetric(t)
	w := &testWatcher{}
	m.Watch(w)

	_, err := m.Increment(bg, []cty.Value{cty.NumberIntVal(7)})
	require.NoError(t, err)

	assert.Equal(t, 1, w.count())
	c := w.get(0)
	assert.True(t, c.old.RawEquals(cty.NumberFloatVal(0)))
	assert.True(t, c.new.RawEquals(cty.NumberFloatVal(7)))
}

func TestGaugeMetric_Unwatch_StopsNotifications(t *testing.T) {
	m := newTestGaugeMetric(t)
	w := &testWatcher{}
	m.Watch(w)
	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Equal(t, 1, w.count())

	m.Unwatch(w)
	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(2)})
	assert.Equal(t, 1, w.count())
}

// --- CounterMetric ---

func TestCounterMetric_Set_Notifies(t *testing.T) {
	m := newTestCounterMetric(t)
	w := &testWatcher{}
	m.Watch(w)

	_, err := m.Set(bg, []cty.Value{cty.NumberIntVal(100)})
	require.NoError(t, err)

	assert.Equal(t, 1, w.count())
	c := w.get(0)
	assert.True(t, c.old.RawEquals(cty.NumberFloatVal(0)))
	assert.True(t, c.new.RawEquals(cty.NumberFloatVal(100)))
}

func TestCounterMetric_Set_DeltaZeroStillNotifies(t *testing.T) {
	// Even when the counter doesn't advance (delta <= 0), OnChange must fire
	// with old == new (so watchdog heartbeat semantics still work).
	m := newTestCounterMetric(t)
	w := &testWatcher{}
	m.Watch(w)

	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(50)})
	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(30)}) // delta < 0: counter unchanged

	assert.Equal(t, 2, w.count())
	// Second call: old == new == 50 (counter didn't advance)
	c := w.get(1)
	assert.True(t, c.old.RawEquals(cty.NumberFloatVal(50)))
	assert.True(t, c.new.RawEquals(cty.NumberFloatVal(50)))
}

func TestCounterMetric_Increment_Notifies(t *testing.T) {
	m := newTestCounterMetric(t)
	w := &testWatcher{}
	m.Watch(w)

	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(10)})   // baseline
	_, err := m.Increment(bg, []cty.Value{cty.NumberIntVal(5)})
	require.NoError(t, err)

	assert.Equal(t, 2, w.count())
	c := w.get(1)
	assert.True(t, c.old.RawEquals(cty.NumberFloatVal(10)))
	assert.True(t, c.new.RawEquals(cty.NumberFloatVal(15)))
}

func TestCounterMetric_Unwatch_StopsNotifications(t *testing.T) {
	m := newTestCounterMetric(t)
	w := &testWatcher{}
	m.Watch(w)
	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Equal(t, 1, w.count())

	m.Unwatch(w)
	_, _ = m.Set(bg, []cty.Value{cty.NumberIntVal(2)})
	assert.Equal(t, 1, w.count())
}
