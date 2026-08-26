package config

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// pushingReadyable is a contributor that can report its own state changes, the
// way a client with connect and disconnect callbacks does.
type pushingReadyable struct {
	fakeReadyable

	mu     sync.Mutex
	report ReadyReporter
}

func (p *pushingReadyable) SetReadyReporter(report ReadyReporter) {
	p.mu.Lock()
	p.report = report
	p.mu.Unlock()
}

// drop simulates the component noticing it lost its connection: it records the
// state Ready would report, then pushes it.
func (p *pushingReadyable) drop(reason string) {
	p.set(errors.New(reason))
	p.push(errors.New(reason))
}

// recover simulates reconnecting.
func (p *pushingReadyable) recover() {
	p.set(nil)
	p.push(nil)
}

func (p *pushingReadyable) push(err error) {
	p.mu.Lock()
	report := p.report
	p.mu.Unlock()
	if report != nil {
		report(err)
	}
}

func (p *pushingReadyable) hasReporter() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.report != nil
}

func TestAPushingContributorIsHandedAReporter(t *testing.T) {
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)

	assert.True(t, p.hasReporter(), "a ReadyNotifier should receive a reporter at registration")

	// A contributor that cannot push is simply not given one, and nothing else
	// about it changes.
	plain := &fakeReadyable{}
	h.RegisterReady("client", "sql", "db", plain)
	assert.True(t, h.IsReady(context.Background()))
}

func TestADropIsVisibleImmediatelyToWatchers(t *testing.T) {
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)

	handle, err := readyHandleFromValue(h.ReadyValue())
	require.NoError(t, err)
	w := &countingWatcher{}
	handle.Watch(w)

	// Establish the baseline the way any first read does.
	require.True(t, h.IsReady(context.Background()))
	require.Empty(t, w.values())

	before := p.calls.Load()
	p.drop("dial tcp 10.0.0.5:1883: connection refused")

	// Fired with nothing having asked — which is the whole point. In a
	// configuration with no probe and no interval trigger, this used to be
	// invisible until something looked.
	require.Len(t, w.values(), 1)
	assert.True(t, w.values()[0].RawEquals(cty.False))

	// And decided without consulting anyone: one failing contributor makes the
	// aggregate false, so no Ready call was needed.
	assert.Equal(t, before, p.calls.Load(), "a drop must not trigger an evaluation")
}

func TestADropCorrectsTheCachedReport(t *testing.T) {
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)
	other := &fakeReadyable{}
	h.RegisterReady("server", "http", "api", other)

	require.True(t, h.IsReady(context.Background()))
	otherCalls := other.calls.Load()

	p.drop("not connected")

	// A read well inside the 5s TTL must see the failure rather than the cached
	// "everything is fine" — that staleness is what push exists to remove.
	failing := h.Failing(context.Background(), ProbeReady, false)
	require.Len(t, failing, 1)
	assert.Equal(t, "client.broker", failing[0].Component)
	assert.Equal(t, "mqtt", failing[0].Type)
	assert.Equal(t, "not connected", failing[0].Reason)

	// Still served from the cache: the other contributor was not re-probed.
	assert.Equal(t, otherCalls, other.calls.Load())
}

func TestARecoveryDropsTheCache(t *testing.T) {
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)

	p.drop("not connected")
	require.NotEmpty(t, h.Failing(context.Background(), ProbeReady, false))

	before := p.calls.Load()
	p.recover()

	// Recovery is not one component's to declare — others may still be failing
	// — so it invalidates rather than concludes. Without that, a probe inside
	// the TTL would go on serving the stale failure.
	assert.Equal(t, before, p.calls.Load(), "a recovery must not evaluate either")
	assert.Empty(t, h.Failing(context.Background(), ProbeReady, false))
	assert.Greater(t, p.calls.Load(), before, "the next read evaluates for real")
}

func TestARecoveryDoesNotDeclareTheAggregateReady(t *testing.T) {
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)
	h.RegisterReady("client", "sql", "db", &fakeReadyable{err: errors.New("not connected")})

	require.False(t, h.IsReady(context.Background()))

	handle, err := readyHandleFromValue(h.ReadyValue())
	require.NoError(t, err)
	w := &countingWatcher{}
	handle.Watch(w)

	p.drop("not connected")
	p.recover()

	// The sql client is still down, so nothing should have claimed readiness.
	assert.Empty(t, w.values())
	assert.False(t, h.IsReady(context.Background()))
}

func TestADropLogsTheTransitionNamingTheComponent(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	h := NewHealth(zap.New(core))
	h.SetBooted()

	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)

	require.True(t, h.IsReady(context.Background()))
	require.Zero(t, logs.Len())

	p.drop("dial tcp 10.0.0.5:1883: connection refused")

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, zapcore.WarnLevel, entry.Level)
	assert.Equal(t, "Process is no longer ready", entry.Message)
	assert.Equal(t, []any{"client.broker: dial tcp 10.0.0.5:1883: connection refused"},
		entry.ContextMap()["failing"])

	// A second drop with no intervening recovery is not a new transition.
	p.drop("still down")
	assert.Equal(t, 1, logs.Len())
}

func TestAReportBeforeAnyReadIsSafe(t *testing.T) {
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterReady("client", "mqtt", "broker", p)

	// No cached report exists yet, so there is no entry to correct. The report
	// must not panic, and the next read must still find the failure — from
	// Ready, which is the source of truth push never replaces.
	p.drop("not connected")

	failing := h.Failing(context.Background(), ProbeReady, false)
	require.Len(t, failing, 1)
	assert.Equal(t, "client.broker", failing[0].Component)
}

func TestAReportOnALiveProbeLeavesReadinessAlone(t *testing.T) {
	h := bootedHealth()
	p := &pushingReadyable{}
	h.RegisterProbe(ProbeLive, "check", "", "pipeline", p, 0)

	handle, err := readyHandleFromValue(h.ReadyValue())
	require.NoError(t, err)
	w := &countingWatcher{}
	handle.Watch(w)
	require.True(t, h.IsReady(context.Background()))

	p.drop("wedged")

	// sys.ready is readiness; a liveness failure must not move it.
	assert.Empty(t, w.values())
	assert.True(t, h.IsReady(context.Background()))
	assert.NotEmpty(t, h.Failing(context.Background(), ProbeLive, false))
}

func TestReportsRacingARefresh(t *testing.T) {
	h := bootedHealth()
	// A latency long enough that a report reliably lands mid-refresh.
	p := &pushingReadyable{}
	p.delay = 20 * time.Millisecond
	h.RegisterReady("client", "mqtt", "broker", p)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Failing(context.Background(), ProbeReady, true)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.push(errors.New("not connected"))
			p.push(nil)
		}()
	}
	wg.Wait()
}
