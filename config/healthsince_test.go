package config

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entryFor returns the report entry for one component, failing the test if the
// report does not contain it.
func entryFor(t *testing.T, statuses []ComponentStatus, component string) ComponentStatus {
	t.Helper()
	for _, s := range statuses {
		if s.Component == component {
			return s
		}
	}
	require.Failf(t, "component missing from report", "no entry for %q", component)
	return ComponentStatus{}
}

func TestSinceHoldsSteadyWhileTheVerdictDoes(t *testing.T) {
	ctx := context.Background()
	h := bootedHealth()
	h.RegisterReady("client", "mqtt", "broker", &fakeReadyable{})

	first := entryFor(t, h.Status(ctx, ProbeReady, true), "client.broker").Since
	require.False(t, first.IsZero(), "every entry of a real report is dated")

	time.Sleep(20 * time.Millisecond)

	// Re-observing an unchanged verdict must not reset the clock: the age of a
	// state is the whole reason to record it.
	second := entryFor(t, h.Status(ctx, ProbeReady, true), "client.broker").Since
	assert.Equal(t, first, second)
}

func TestSinceMovesWhenTheVerdictFlips(t *testing.T) {
	ctx := context.Background()
	h := bootedHealth()
	f := &fakeReadyable{}
	h.RegisterReady("client", "mqtt", "broker", f)

	wasReadyAt := entryFor(t, h.Status(ctx, ProbeReady, true), "client.broker").Since

	time.Sleep(20 * time.Millisecond)
	f.set(errors.New("not connected"))

	broke := entryFor(t, h.Status(ctx, ProbeReady, true), "client.broker")
	require.False(t, broke.Ready)
	assert.True(t, broke.Since.After(wasReadyAt),
		"a flip takes a new timestamp, so the report can say how long it has been broken")

	// And the new one is just as sticky as the old.
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, broke.Since,
		entryFor(t, h.Status(ctx, ProbeReady, true), "client.broker").Since)
}

func TestSinceDatesAPushRatherThanTheReadThatFollows(t *testing.T) {
	ctx := context.Background()
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)
	require.True(t, h.IsReady(ctx))

	p.drop("not connected")
	pushedBy := time.Now()

	time.Sleep(50 * time.Millisecond)

	// A push is the earliest anything can know. Dating the drop to whenever a
	// probe next arrived would understate the outage by the polling interval,
	// which on a ten-second kubelet period is most of the number.
	got := entryFor(t, h.Status(ctx, ProbeReady, true), "client.broker")
	require.False(t, got.Ready)
	assert.False(t, got.Since.After(pushedBy),
		"the drop should be dated to the push, not to the read that followed it")
}

func TestSinceDatesAComponentWhoseProbeNobodyAsked(t *testing.T) {
	ctx := context.Background()
	h := bootedHealth()
	h.RegisterProbe(ProbeLive, "check", "", "wedged", &fakeReadyable{}, 0)
	h.RegisterReady("client", "mqtt", "broker", &fakeReadyable{})

	// One evaluation covers both probes; this read filters liveness out.
	h.Status(ctx, ProbeReady, true)
	evaluatedBy := time.Now()

	time.Sleep(50 * time.Millisecond)

	// Served from the cache, so nothing has re-run — but the entry is still
	// dated to when it was observed rather than to now.
	got := entryFor(t, h.Status(ctx, ProbeLive, false), "check.wedged")
	assert.False(t, got.Since.After(evaluatedBy),
		"a component evaluated during another probe's read is dated to the evaluation")
}

func TestSinceOnTheProcessGateDatesBoot(t *testing.T) {
	ctx := context.Background()
	h := NewHealth(nil)

	starting := entryFor(t, h.Status(ctx, ProbeReady, false), "process")
	require.False(t, starting.Ready)
	require.False(t, starting.Since.IsZero(),
		"the synthesized process entry is dated like any other, though it never reaches a contributor")

	time.Sleep(20 * time.Millisecond)
	h.SetBooted()

	// So `[+]process ok (for 3h12m)` reads as how long the process has been
	// serving, which is the number an operator actually wants from it.
	serving := entryFor(t, h.Status(ctx, ProbeReady, false), "process")
	require.True(t, serving.Ready)
	assert.True(t, serving.Since.After(starting.Since))
}

func TestHeldForRendersOnlyARealTimestamp(t *testing.T) {
	assert.Equal(t, "", heldFor(time.Time{}),
		"a hand-built ComponentStatus carries no timestamp and must not grow a fake one")
	assert.Equal(t, " (for 1m30s)", heldFor(time.Now().Add(-90*time.Second)))
}
