package conditions

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// fakeClock is a virtual clock that advances only when tests call Advance().
// AfterFunc callbacks registered here fire inline during Advance() when their
// scheduled fire time has been reached or passed.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*fakeTimer
}

type fakeTimer struct {
	clock   *fakeClock
	fireAt  time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) ClockTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, fireAt: c.now.Add(d), f: f}
	c.pending = append(c.pending, t)
	return t
}

// Advance moves the clock forward by d, firing any timers whose fireAt is
// reached, in order. Callbacks are invoked with c.mu UNLOCKED so they can
// schedule new timers.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	target := c.now
	c.mu.Unlock()
	for {
		c.mu.Lock()
		// Find the earliest non-stopped timer at or before target.
		sort.SliceStable(c.pending, func(i, j int) bool {
			return c.pending[i].fireAt.Before(c.pending[j].fireAt)
		})
		var next *fakeTimer
		idx := -1
		for i, t := range c.pending {
			if t.stopped {
				continue
			}
			if !t.fireAt.After(target) {
				next = t
				idx = i
			}
			break
		}
		if next == nil {
			// Purge stopped timers for hygiene.
			pruned := c.pending[:0]
			for _, t := range c.pending {
				if !t.stopped {
					pruned = append(pruned, t)
				}
			}
			c.pending = pruned
			c.mu.Unlock()
			return
		}
		c.pending = append(c.pending[:idx], c.pending[idx+1:]...)
		c.mu.Unlock()
		next.f()
	}
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

// capturingWatcher records every OnChange call for assertions.
type capturingWatcher struct {
	mu    sync.Mutex
	calls []watcherCall
}

type watcherCall struct {
	old bool
	new bool
}

func (w *capturingWatcher) OnChange(_ context.Context, old, new cty.Value) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, watcherCall{old: old.True(), new: new.True()})
}

func (w *capturingWatcher) snapshot() []watcherCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]watcherCall, len(w.calls))
	copy(out, w.calls)
	return out
}

// --- tests ---

func TestPassthroughNoDelays(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{}, clock)
	w := &capturingWatcher{}
	sm.Watch(w)

	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.False(t, sm.Output())

	sm.SetRawInput(context.Background(), true)
	assert.Equal(t, "active", stateOf(t, sm))
	assert.True(t, sm.Output())

	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.False(t, sm.Output())

	assert.Equal(t, []watcherCall{{false, true}, {true, false}}, w.snapshot())
}

func TestActivateAfterTON(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{ActivateAfter: 30 * time.Second}, clock)
	w := &capturingWatcher{}
	sm.Watch(w)

	sm.SetRawInput(context.Background(), true)
	assert.Equal(t, "pending_activation", stateOf(t, sm))
	assert.False(t, sm.Output(), "pending_activation reports false")
	assert.Empty(t, w.snapshot(), "no watcher fire on pending transitions")

	clock.Advance(29 * time.Second)
	assert.Equal(t, "pending_activation", stateOf(t, sm))

	clock.Advance(1 * time.Second)
	assert.Equal(t, "active", stateOf(t, sm))
	assert.True(t, sm.Output())
	assert.Equal(t, []watcherCall{{false, true}}, w.snapshot())
}

func TestActivateAfterCancelledByEarlyFalse(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{ActivateAfter: 30 * time.Second}, clock)

	sm.SetRawInput(context.Background(), true)
	clock.Advance(10 * time.Second)
	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "inactive", stateOf(t, sm))

	clock.Advance(30 * time.Second)
	assert.Equal(t, "inactive", stateOf(t, sm), "cancelled pending must not fire")
}

func TestDeactivateAfterTOF(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{DeactivateAfter: 5 * time.Minute}, clock)
	sm.SetRawInput(context.Background(), true)
	require.Equal(t, "active", stateOf(t, sm))

	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "pending_deactivation", stateOf(t, sm))
	assert.True(t, sm.Output(), "pending_deactivation reports true")

	clock.Advance(4 * time.Minute)
	assert.Equal(t, "pending_deactivation", stateOf(t, sm))

	clock.Advance(1 * time.Minute)
	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.False(t, sm.Output())
}

func TestDeactivateReassertBeforeExpiry(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{DeactivateAfter: 5 * time.Minute}, clock)
	sm.SetRawInput(context.Background(), true)
	sm.SetRawInput(context.Background(), false)
	clock.Advance(2 * time.Minute)
	sm.SetRawInput(context.Background(), true)
	assert.Equal(t, "active", stateOf(t, sm))

	clock.Advance(10 * time.Minute)
	assert.Equal(t, "active", stateOf(t, sm), "re-assertion cancels pending_deactivation")
}

func TestTimeoutTP(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Timeout: 10 * time.Minute}, clock)
	sm.SetRawInput(context.Background(), true)
	require.Equal(t, "active", stateOf(t, sm))

	clock.Advance(10 * time.Minute)
	assert.Equal(t, "inactive", stateOf(t, sm))
}

func TestTimeoutRestartedOnReassert(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Timeout: 10 * time.Minute}, clock)
	sm.SetRawInput(context.Background(), true)

	clock.Advance(9 * time.Minute)
	// Toggle input off/on while active by driving directly (no deactivate_after).
	// With no deactivate_after, off would go straight to inactive, so use a
	// separate harness: set input true again while still active (idempotent
	// real-input edge). Simulate "re-assertion" via explicit re-toggle:
	sm.SetRawInput(context.Background(), false)
	// Immediately re-assert (no deactivate_after so we re-enter active).
	sm.SetRawInput(context.Background(), true)
	require.Equal(t, "active", stateOf(t, sm))

	clock.Advance(9 * time.Minute)
	assert.Equal(t, "active", stateOf(t, sm), "timeout restarted")

	clock.Advance(1 * time.Minute)
	assert.Equal(t, "inactive", stateOf(t, sm))
}

func TestLatchHoldsThroughInputFalse(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Latch: true}, clock)
	sm.SetRawInput(context.Background(), true)
	require.Equal(t, "active", stateOf(t, sm))

	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "active", stateOf(t, sm), "latch holds through input=false")
	assert.True(t, sm.Output())

	require.NoError(t, sm.Clear(context.Background()))
	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.False(t, sm.Output())
}

func TestLatchIgnoresTimeout(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Latch: true, Timeout: 1 * time.Minute}, clock)
	sm.SetRawInput(context.Background(), true)
	clock.Advance(10 * time.Minute)
	assert.Equal(t, "active", stateOf(t, sm), "timeout ignored under latch")
}

func TestInvertFlipsGetAndWatchers(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Invert: true}, clock)
	w := &capturingWatcher{}
	sm.Watch(w)

	assert.True(t, sm.Output(), "inverted inactive = true")
	sm.SetRawInput(context.Background(), true)
	assert.False(t, sm.Output())
	sm.SetRawInput(context.Background(), false)
	assert.True(t, sm.Output())

	assert.Equal(t, []watcherCall{{true, false}, {false, true}}, w.snapshot(),
		"watcher receives inverted values")
}

func TestCooldownGatesReactivation(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Cooldown: 5 * time.Minute}, clock)

	sm.SetRawInput(context.Background(), true)
	require.Equal(t, "active", stateOf(t, sm))
	sm.SetRawInput(context.Background(), false)
	require.Equal(t, "inactive", stateOf(t, sm))

	// Attempt to re-activate during cooldown — should stay inactive.
	sm.SetRawInput(context.Background(), true)
	assert.Equal(t, "inactive", stateOf(t, sm), "cooldown gates re-activation")

	clock.Advance(5 * time.Minute)
	// Cooldown expires and input is still asserted — activation resumes.
	assert.Equal(t, "active", stateOf(t, sm))
}

func TestCooldownExpiryWithInputLow(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{Cooldown: 5 * time.Minute}, clock)
	sm.SetRawInput(context.Background(), true)
	sm.SetRawInput(context.Background(), false)

	clock.Advance(5 * time.Minute)
	assert.Equal(t, "inactive", stateOf(t, sm))

	// After cooldown, input assertion should activate normally.
	sm.SetRawInput(context.Background(), true)
	assert.Equal(t, "active", stateOf(t, sm))
}

func TestClearInAllStatesIsSafe(t *testing.T) {
	clock := newFakeClock()
	for _, tc := range []struct {
		name  string
		setup func(sm *StateMachine)
	}{
		{"inactive", func(sm *StateMachine) {}},
		{"pending_activation", func(sm *StateMachine) {
			sm.SetRawInput(context.Background(), true)
		}},
		{"active", func(sm *StateMachine) {
			sm.SetRawInput(context.Background(), true)
			clock.Advance(2 * time.Second)
		}},
		{"pending_deactivation", func(sm *StateMachine) {
			sm.SetRawInput(context.Background(), true)
			clock.Advance(2 * time.Second)
			sm.SetRawInput(context.Background(), false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewStateMachine(Behavior{
				ActivateAfter:   1 * time.Second,
				DeactivateAfter: 1 * time.Second,
			}, clock)
			tc.setup(sm)
			require.NoError(t, sm.Clear(context.Background()))
			assert.Equal(t, "inactive", stateOf(t, sm))
			assert.False(t, sm.Output())
		})
	}
}

func TestFullAlarmExample(t *testing.T) {
	// Spec §Examples "Temperature alarm with asymmetric delays":
	//   activate_after = 30s, deactivate_after = 5m, timeout = 1h.
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{
		ActivateAfter:   30 * time.Second,
		DeactivateAfter: 5 * time.Minute,
		Timeout:         1 * time.Hour,
	}, clock)

	sm.SetRawInput(context.Background(), true)
	clock.Advance(30 * time.Second)
	require.Equal(t, "active", stateOf(t, sm))

	// Hot for 59 min, then cools.
	clock.Advance(59 * time.Minute)
	require.Equal(t, "active", stateOf(t, sm))

	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "pending_deactivation", stateOf(t, sm))
	clock.Advance(5 * time.Minute)
	assert.Equal(t, "inactive", stateOf(t, sm))
}

func TestRetentiveAccumulatesAcrossIntervals(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{
		ActivateAfter: 10 * time.Minute,
		Retentive:     true,
	}, clock)

	// Accumulate 4 minutes.
	sm.SetRawInput(context.Background(), true)
	clock.Advance(4 * time.Minute)
	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "pending_activation", stateOf(t, sm), "paused, not reset")

	// No progress while input is low.
	clock.Advance(10 * time.Minute)
	assert.Equal(t, "pending_activation", stateOf(t, sm))

	// Accumulate another 4 minutes — still not enough.
	sm.SetRawInput(context.Background(), true)
	clock.Advance(4 * time.Minute)
	assert.Equal(t, "pending_activation", stateOf(t, sm))

	// Pause again, then the final 2 minutes.
	sm.SetRawInput(context.Background(), false)
	sm.SetRawInput(context.Background(), true)
	clock.Advance(2 * time.Minute)
	assert.Equal(t, "active", stateOf(t, sm))
}

func TestRetentiveClearDiscardsAccumulator(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{
		ActivateAfter: 10 * time.Minute,
		Retentive:     true,
	}, clock)
	sm.SetRawInput(context.Background(), true)
	clock.Advance(8 * time.Minute)
	require.NoError(t, sm.Clear(context.Background()))

	sm.SetRawInput(context.Background(), true)
	clock.Advance(5 * time.Minute)
	assert.Equal(t, "pending_activation", stateOf(t, sm), "accumulator was discarded")

	clock.Advance(5 * time.Minute)
	assert.Equal(t, "active", stateOf(t, sm))
}

func TestInhibitGatesActivationFromInactive(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{}, clock)
	sm.SetInhibited(context.Background(), true)
	sm.SetRawInput(context.Background(), true)
	assert.Equal(t, "inactive", stateOf(t, sm), "inhibit suppresses activation")

	sm.SetInhibited(context.Background(), false)
	assert.Equal(t, "active", stateOf(t, sm), "clearing inhibit with asserted input activates")
}

func TestInhibitCancelsPendingActivation(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{ActivateAfter: 30 * time.Second}, clock)
	sm.SetRawInput(context.Background(), true)
	require.Equal(t, "pending_activation", stateOf(t, sm))

	clock.Advance(10 * time.Second)
	sm.SetInhibited(context.Background(), true)
	assert.Equal(t, "inactive", stateOf(t, sm), "inhibit cancels pending_activation")

	sm.SetInhibited(context.Background(), false)
	assert.Equal(t, "pending_activation", stateOf(t, sm), "activation resumes")

	clock.Advance(30 * time.Second)
	assert.Equal(t, "active", stateOf(t, sm), "new activate_after clock, not a partial one")
}

func TestInhibitDiscardsRetentiveAccumulator(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{
		ActivateAfter: 10 * time.Minute,
		Retentive:     true,
	}, clock)
	sm.SetRawInput(context.Background(), true)
	clock.Advance(8 * time.Minute)

	sm.SetInhibited(context.Background(), true)
	sm.SetInhibited(context.Background(), false)

	clock.Advance(5 * time.Minute)
	assert.Equal(t, "pending_activation", stateOf(t, sm), "accumulator discarded by inhibit")

	clock.Advance(5 * time.Minute)
	assert.Equal(t, "active", stateOf(t, sm))
}

func TestInhibitDoesNotAffectActive(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{}, clock)
	sm.SetRawInput(context.Background(), true)
	require.Equal(t, "active", stateOf(t, sm))

	sm.SetInhibited(context.Background(), true)
	assert.Equal(t, "active", stateOf(t, sm), "inhibit does not deactivate an active condition")
}

func stateOf(t *testing.T, sm *StateMachine) string {
	t.Helper()
	s, err := sm.State(context.Background())
	require.NoError(t, err)
	return s
}

func TestBootstrap_NoopWhenDisabled(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{}, clock)
	sm.Bootstrap()
	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.False(t, sm.Output())
}

func TestBootstrap_ActiveLatched(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{StartActive: true, Latch: true}, clock)
	sm.Bootstrap()

	assert.Equal(t, "active", stateOf(t, sm))
	assert.True(t, sm.Output())

	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "active", stateOf(t, sm), "latch ignores deassertion")

	require.NoError(t, sm.Clear(context.Background()))
	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.False(t, sm.Output())
}

func TestBootstrap_ActiveUnlatched(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{StartActive: true}, clock)
	sm.Bootstrap()

	assert.Equal(t, "active", stateOf(t, sm))
	assert.True(t, sm.Output())

	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "inactive", stateOf(t, sm), "unlatched deasserts normally")
}

func TestBootstrap_ActiveUnlatchedWithTimeout(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{StartActive: true, Timeout: 10 * time.Second}, clock)
	w := &capturingWatcher{}
	sm.Watch(w)
	sm.Bootstrap()

	require.Equal(t, "active", stateOf(t, sm))
	require.Empty(t, w.snapshot(), "bootstrap itself must not notify")

	clock.Advance(10 * time.Second)
	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.Equal(t, []watcherCall{{true, false}}, w.snapshot(),
		"only the timeout-driven deactivation fires; no synthetic rising edge")
}

func TestBootstrap_NoSyntheticNotify(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{StartActive: true, Latch: true}, clock)
	w := &capturingWatcher{}
	sm.Watch(w)
	sm.Bootstrap()
	assert.Empty(t, w.snapshot(), "silent boot-active contract")
}

func TestBootstrap_InvertTransformsOutput(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{StartActive: true, Latch: true, Invert: true}, clock)
	sm.Bootstrap()

	assert.Equal(t, "active", stateOf(t, sm), "internal state is active")
	assert.False(t, sm.Output(), "invert flips external output")

	sm.SetRawInput(context.Background(), false)
	assert.Equal(t, "active", stateOf(t, sm), "latch still holds")
	assert.False(t, sm.Output())

	require.NoError(t, sm.Clear(context.Background()))
	assert.Equal(t, "inactive", stateOf(t, sm))
	assert.True(t, sm.Output(), "inactive inverted reads true")
}

func TestBootstrap_InhibitIgnored(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{StartActive: true, Latch: true}, clock)
	sm.SetInhibited(context.Background(), true)
	sm.Bootstrap()

	assert.Equal(t, "active", stateOf(t, sm),
		"inhibit does not suppress a configured initial state")
	assert.True(t, sm.Output())
}

func TestBootstrap_ClearGoesToInactive(t *testing.T) {
	clock := newFakeClock()
	sm := NewStateMachine(Behavior{StartActive: true, Latch: true}, clock)
	sm.Bootstrap()

	require.NoError(t, sm.Clear(context.Background()))
	assert.Equal(t, "inactive", stateOf(t, sm), "clear returns to inactive, not start_active")
	assert.False(t, sm.Output())

	sm.SetRawInput(context.Background(), true)
	assert.Equal(t, "active", stateOf(t, sm), "fresh cycle activates normally")
}
