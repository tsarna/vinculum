package types

import (
	"context"
	"sync"

	"github.com/zclconf/go-cty/cty"
)

// WatchableMixin encapsulates watcher-list management for Watchable types.
// Embedding types must use a separate mutex for their own value; watchMu is
// used only for the watcher slice.
type WatchableMixin struct {
	watchMu  sync.RWMutex
	watchers []Watcher
}

// Watch registers w to receive OnChange notifications. Registering the same
// Watcher twice is a no-op.
func (m *WatchableMixin) Watch(w Watcher) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	for _, existing := range m.watchers {
		if existing == w {
			return
		}
	}
	m.watchers = append(m.watchers, w)
}

// Unwatch removes a previously registered Watcher. Removing an unregistered
// Watcher is a no-op.
func (m *WatchableMixin) Unwatch(w Watcher) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	for i, existing := range m.watchers {
		if existing == w {
			m.watchers = append(m.watchers[:i], m.watchers[i+1:]...)
			return
		}
	}
}

// NotifyAll snapshots the watcher list and calls OnChange on each entry.
// Must be called after the embedding type's value mutex has been released.
func (m *WatchableMixin) NotifyAll(ctx context.Context, old, new cty.Value) {
	m.watchMu.RLock()
	snapshot := append([]Watcher(nil), m.watchers...)
	m.watchMu.RUnlock()
	for _, w := range snapshot {
		w.OnChange(ctx, old, new)
	}
}
