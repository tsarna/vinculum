package conditions

import (
	"context"
	_ "embed"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

//go:embed testdata/hooks_basic.vcl
var hooksBasicVCL []byte

//go:embed testdata/hooks_start_active.vcl
var hooksStartActiveVCL []byte

//go:embed testdata/hooks_counter_unlatched.vcl
var hooksCounterUnlatchedVCL []byte

//go:embed testdata/hooks_invert.vcl
var hooksInvertVCL []byte

// recorder is a bus.Subscriber that captures every event it receives, keyed
// for ordered replay. Embeds BaseSubscriber to satisfy the OnSubscribe /
// OnUnsubscribe / PassThrough stubs.
type recorder struct {
	bus.BaseSubscriber
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	Topic   string
	Message any
}

func (r *recorder) OnEvent(_ context.Context, topic string, message any, _ map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{Topic: topic, Message: message})
	return nil
}

func (r *recorder) snapshot() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedEvent, len(r.events))
	copy(out, r.events)
	return out
}

// attachRecorder builds the config, subscribes a recorder to the given topic
// on bus.main, and returns the config + recorder + a cleanup function.
// Startables and PostStartables are NOT run; each test drives them explicitly
// to observe the PostStart timing.
func attachRecorder(t *testing.T, src []byte, topic string) (*cfg.Config, *recorder, func()) {
	t.Helper()
	c := buildConfig(t, src)
	eb, err := cfg.GetEventBusFromCapsule(c.CtyBusMap["main"])
	require.NoError(t, err)
	rec := &recorder{}
	require.NoError(t, eb.Subscribe(context.Background(), topic, rec))
	cleanup := func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
		_ = eb.UnsubscribeAll(context.Background(), rec)
	}
	return c, rec, cleanup
}

func runStart(t *testing.T, c *cfg.Config) {
	t.Helper()
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
}

func runPostStart(t *testing.T, c *cfg.Config) {
	t.Helper()
	for _, s := range c.PostStartables {
		require.NoError(t, s.PostStart())
	}
}

func waitForEvents(t *testing.T, r *recorder, n int) []recordedEvent {
	t.Helper()
	var got []recordedEvent
	require.Eventually(t, func() bool {
		got = r.snapshot()
		return len(got) >= n
	}, time.Second, 2*time.Millisecond, "waiting for %d events, got %d", n, len(got))
	return got
}

// Hook action bodies publish a cty object literal with "kind", "new", and
// optionally "old" attributes. send() preserves cty.Value as the bus message.
func hookKind(t *testing.T, ev recordedEvent) string {
	t.Helper()
	v, ok := ev.Message.(cty.Value)
	require.True(t, ok, "expected cty.Value message, got %T", ev.Message)
	require.True(t, v.Type().IsObjectType(), "expected object, got %s", v.Type().FriendlyName())
	return v.GetAttr("kind").AsString()
}

func hookNew(t *testing.T, ev recordedEvent) bool {
	t.Helper()
	v, ok := ev.Message.(cty.Value)
	require.True(t, ok)
	return v.GetAttr("new").True()
}

func hookOld(t *testing.T, ev recordedEvent) bool {
	t.Helper()
	v, ok := ev.Message.(cty.Value)
	require.True(t, ok)
	return v.GetAttr("old").True()
}

// --- tests ---

func TestHooks_OnInitFiresAtPostStart(t *testing.T) {
	c, rec, cleanup := attachRecorder(t, hooksBasicVCL, "hook")
	defer cleanup()

	runStart(t, c)
	// Give any stray dispatch a chance to arrive — none should.
	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, rec.snapshot(), "no hook should fire during Start")

	runPostStart(t, c)
	evs := waitForEvents(t, rec, 1)
	require.Len(t, evs, 1, "exactly one on_init event")
	assert.Equal(t, "on_init", hookKind(t, evs[0]))
	assert.False(t, hookNew(t, evs[0]), "default boot output is inactive")
}

func TestHooks_OnInitSeesStartActive(t *testing.T) {
	c, rec, cleanup := attachRecorder(t, hooksStartActiveVCL, "hook")
	defer cleanup()

	runStart(t, c)
	runPostStart(t, c)

	evs := waitForEvents(t, rec, 1)
	require.Len(t, evs, 1)
	assert.Equal(t, "on_init", hookKind(t, evs[0]))
	assert.True(t, hookNew(t, evs[0]), "start_active=true → on_init sees new_value=true")
}

func TestHooks_OnActivateOnDeactivate(t *testing.T) {
	c, rec, cleanup := attachRecorder(t, hooksBasicVCL, "hook")
	defer cleanup()
	runStart(t, c)
	runPostStart(t, c)
	// Drain the on_init event.
	waitForEvents(t, rec, 1)

	cond := c.CtyConditionMap["gate"].EncapsulatedValue().(*TimerCondition)

	_, err := cond.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	evs := waitForEvents(t, rec, 2)
	require.Equal(t, "on_activate", hookKind(t, evs[1]))
	assert.True(t, hookNew(t, evs[1]))
	assert.False(t, hookOld(t, evs[1]))

	_, err = cond.Set(context.Background(), []cty.Value{cty.False})
	require.NoError(t, err)
	evs = waitForEvents(t, rec, 3)
	require.Equal(t, "on_deactivate", hookKind(t, evs[2]))
	assert.False(t, hookNew(t, evs[2]))
	assert.True(t, hookOld(t, evs[2]))
}

func TestHooks_NoActivateAtBoot(t *testing.T) {
	c, rec, cleanup := attachRecorder(t, hooksStartActiveVCL, "hook")
	defer cleanup()

	runStart(t, c)
	runPostStart(t, c)
	// Let any stray activate dispatch surface.
	time.Sleep(30 * time.Millisecond)

	evs := rec.snapshot()
	require.Len(t, evs, 1, "only on_init fires at boot; on_activate is silent")
	assert.Equal(t, "on_init", hookKind(t, evs[0]))

	// Clear then re-activate; on_activate should now fire normally.
	cond := c.CtyConditionMap["fault"].EncapsulatedValue().(*TimerCondition)
	require.NoError(t, cond.Clear(context.Background()))
	// Clearing a latched active condition emits a deactivate transition, so
	// on_deactivate fires once.
	waitForEvents(t, rec, 2)

	_, err := cond.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	evs = waitForEvents(t, rec, 3)
	require.Equal(t, "on_activate", hookKind(t, evs[2]))
	assert.True(t, hookNew(t, evs[2]))
}

func TestHooks_InvertAppliesToHooks(t *testing.T) {
	// With invert=true, NotifyAll passes inverted values to watchers — hooks
	// therefore fire on the user-visible edges, not the internal ones.
	c, rec, cleanup := attachRecorder(t, hooksInvertVCL, "hook")
	defer cleanup()
	runStart(t, c)
	// (no on_init in this fixture; PostStartables is empty.)

	cond := c.CtyConditionMap["gate"].EncapsulatedValue().(*TimerCondition)

	// Internal active → external inactive → on_deactivate fires.
	_, err := cond.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	evs := waitForEvents(t, rec, 1)
	require.Equal(t, "on_deactivate", hookKind(t, evs[0]))
	assert.False(t, hookNew(t, evs[0]), "visible output went false")
	assert.True(t, hookOld(t, evs[0]), "visible output was previously true (inactive inverted)")
}

func TestHooks_CounterReconcileOrdering(t *testing.T) {
	// Unlatched counter with start_active + count < preset: the Start-time
	// applyDelta(0) reconcile flips the forced-active SM back to inactive,
	// firing on_deactivate before PostStart runs on_init.
	c, rec, cleanup := attachRecorder(t, hooksCounterUnlatchedVCL, "hook")
	defer cleanup()

	runStart(t, c)
	// on_deactivate fires during Start from the reconcile; wait for it.
	evs := waitForEvents(t, rec, 1)
	require.Equal(t, "on_deactivate", hookKind(t, evs[0]),
		"Start-time reconcile fires on_deactivate first")
	assert.False(t, hookNew(t, evs[0]))
	assert.True(t, hookOld(t, evs[0]))

	runPostStart(t, c)
	evs = waitForEvents(t, rec, 2)
	require.Equal(t, "on_init", hookKind(t, evs[1]),
		"on_init fires at PostStart, with the reconciled (inactive) output")
	assert.False(t, hookNew(t, evs[1]))
}

func TestHooks_FireSynchronouslyFromCaller(t *testing.T) {
	// Synchronous-dispatch contract: when set() returns, the hook has
	// completed. We verify by checking that the hook's bus publish is
	// observable immediately after set() (bus dispatch itself is async, but
	// the publish queueing happens synchronously inside the hook).
	c, rec, cleanup := attachRecorder(t, hooksBasicVCL, "hook")
	defer cleanup()
	runStart(t, c)
	runPostStart(t, c)
	waitForEvents(t, rec, 1) // drain on_init

	cond := c.CtyConditionMap["gate"].EncapsulatedValue().(*TimerCondition)
	// Before set(), event count is 1 (only on_init).
	require.Len(t, rec.snapshot(), 1)

	_, err := cond.Set(context.Background(), []cty.Value{cty.True})
	require.NoError(t, err)
	// After set() returns, the hook has enqueued its send() to the bus. The
	// bus dispatch takes microseconds; the publish itself is synchronous
	// from the hook's perspective.
	evs := waitForEvents(t, rec, 2)
	require.Equal(t, "on_activate", hookKind(t, evs[1]))
}

// --- compile-time assertions ---

var _ bus.Subscriber = (*recorder)(nil)
