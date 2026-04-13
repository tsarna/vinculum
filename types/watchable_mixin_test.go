package types

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

// testWatcher records every OnChange call for inspection in tests.
type testWatcher struct {
	mu      sync.Mutex
	changes []testChange
}

type testChange struct {
	ctx context.Context
	old cty.Value
	new cty.Value
}

func (w *testWatcher) OnChange(ctx context.Context, old, new cty.Value) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.changes = append(w.changes, testChange{ctx, old, new})
}

func (w *testWatcher) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.changes)
}

func (w *testWatcher) get(i int) testChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changes[i]
}

func TestWatchableMixin_WatchDedup(t *testing.T) {
	var m WatchableMixin
	w := &testWatcher{}
	m.Watch(w)
	m.Watch(w) // second registration is a no-op
	m.NotifyAll(bg, cty.True, cty.False)
	assert.Equal(t, 1, w.count(), "duplicate Watch should not result in duplicate calls")
}

func TestWatchableMixin_UnwatchNotRegistered(t *testing.T) {
	var m WatchableMixin
	w := &testWatcher{}
	assert.NotPanics(t, func() { m.Unwatch(w) })
	m.NotifyAll(bg, cty.True, cty.False)
	assert.Equal(t, 0, w.count())
}

func TestWatchableMixin_UnwatchStopsNotifications(t *testing.T) {
	var m WatchableMixin
	w := &testWatcher{}
	m.Watch(w)
	m.NotifyAll(bg, cty.True, cty.False)
	assert.Equal(t, 1, w.count())

	m.Unwatch(w)
	m.NotifyAll(bg, cty.True, cty.False)
	assert.Equal(t, 1, w.count(), "no new calls after Unwatch")
}

func TestWatchableMixin_NotifyAll_Values(t *testing.T) {
	var m WatchableMixin
	w := &testWatcher{}
	m.Watch(w)

	old := cty.NumberIntVal(1)
	new := cty.NumberIntVal(2)
	ctx := context.WithValue(bg, "key", "val") //nolint:staticcheck
	m.NotifyAll(ctx, old, new)

	assert.Equal(t, 1, w.count())
	c := w.get(0)
	assert.Equal(t, ctx, c.ctx, "context should be forwarded verbatim")
	assert.True(t, c.old.RawEquals(old))
	assert.True(t, c.new.RawEquals(new))
}

func TestWatchableMixin_NotifyAll_MultipleWatchers_InOrder(t *testing.T) {
	var m WatchableMixin
	var order []int
	var mu sync.Mutex

	makeWatcher := func(n int) Watcher {
		return &orderWatcher{id: n, order: &order, mu: &mu}
	}

	w1 := makeWatcher(1)
	w2 := makeWatcher(2)
	w3 := makeWatcher(3)
	m.Watch(w1)
	m.Watch(w2)
	m.Watch(w3)
	m.NotifyAll(bg, cty.True, cty.False)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int{1, 2, 3}, order)
}

func TestWatchableMixin_NotifyAll_Snapshot(t *testing.T) {
	// A Watcher added during a notifyAll call must not receive the current call.
	var m WatchableMixin
	w2 := &testWatcher{}

	var once sync.Once
	adder := &adderWatcher{mixin: &m, toAdd: w2, once: &once}
	m.Watch(adder)

	m.NotifyAll(bg, cty.True, cty.False)

	assert.Equal(t, 0, w2.count(), "watcher added during notifyAll should not receive that call")
	// On the next call w2 is registered and should receive it.
	m.NotifyAll(bg, cty.True, cty.False)
	assert.Equal(t, 1, w2.count())
}

// orderWatcher records call order for TestWatchableMixin_NotifyAll_MultipleWatchers_InOrder.
type orderWatcher struct {
	id    int
	order *[]int
	mu    *sync.Mutex
}

func (w *orderWatcher) OnChange(_ context.Context, _, _ cty.Value) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.order = append(*w.order, w.id)
}

// adderWatcher adds another Watcher to a mixin on its first call.
type adderWatcher struct {
	mixin *WatchableMixin
	toAdd Watcher
	once  *sync.Once
}

func (w *adderWatcher) OnChange(_ context.Context, _, _ cty.Value) {
	w.once.Do(func() { w.mixin.Watch(w.toAdd) })
}
