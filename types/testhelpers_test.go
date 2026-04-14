package types

import (
	"context"
	"sync"

	"github.com/zclconf/go-cty/cty"
)

type testChange struct {
	ctx context.Context
	old cty.Value
	new cty.Value
}

type testWatcher struct {
	mu      sync.Mutex
	changes []testChange
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
