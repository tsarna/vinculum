package vws

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// recordingMonitor stands in for the reconnector, so a test can prove the
// health monitor delegates rather than displacing it.
type recordingMonitor struct {
	mu     sync.Mutex
	events []string
}

func (m *recordingMonitor) note(event string) {
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
}

func (m *recordingMonitor) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.events...)
}

func (m *recordingMonitor) OnConnect(context.Context, bus.Client)           { m.note("connect") }
func (m *recordingMonitor) OnDisconnect(context.Context, bus.Client, error) { m.note("disconnect") }
func (m *recordingMonitor) OnSubscribe(context.Context, bus.Client, string) { m.note("subscribe") }
func (m *recordingMonitor) OnUnsubscribe(context.Context, bus.Client, string) {
	m.note("unsubscribe")
}
func (m *recordingMonitor) OnUnsubscribeAll(context.Context, bus.Client) { m.note("unsubscribe_all") }

// reports captures what a client pushes to the health subsystem.
type reports struct {
	mu   sync.Mutex
	errs []error
}

func (r *reports) reporter() cfg.ReadyReporter {
	return func(err error) {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
	}
}

func (r *reports) all() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func buildVWS(t *testing.T, src string) *VinculumWebsocketClient {
	t.Helper()
	config, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%v", diags)
	return config.Clients["vws"]["peer"].(*VinculumWebsocketClient)
}

func TestAMonitorIsInstalledWithoutAReconnectBlock(t *testing.T) {
	// Previously no monitor was installed at all without a reconnect block, so
	// the client's connection state was invisible until something probed.
	c := buildVWS(t, `
client "vws" "peer" {
    url = "ws://127.0.0.1:1/ws"
}
`)

	var r reports
	c.SetReadyReporter(r.reporter())

	monitor := &healthMonitor{client: c}
	monitor.OnDisconnect(context.Background(), nil, errors.New("peer went away"))
	monitor.OnConnect(context.Background(), nil)

	got := r.all()
	require.Len(t, got, 2)
	assert.EqualError(t, got[0], "peer went away")
	assert.NoError(t, got[1])
}

func TestAGracefulDisconnectIsStillReported(t *testing.T) {
	c := buildVWS(t, `
client "vws" "peer" {
    url = "ws://127.0.0.1:1/ws"
}
`)

	var r reports
	c.SetReadyReporter(r.reporter())

	// A nil error means a deliberate Disconnect, which is still a disconnect as
	// far as serving traffic goes.
	(&healthMonitor{client: c}).OnDisconnect(context.Background(), nil, nil)

	got := r.all()
	require.Len(t, got, 1)
	assert.EqualError(t, got[0], "not connected")
}

func TestTheReconnectorStillReceivesItsCallbacks(t *testing.T) {
	c := buildVWS(t, `
client "vws" "peer" {
    url = "ws://127.0.0.1:1/ws"

    reconnect {
        initial_delay = "1s"
    }
}
`)

	var r reports
	c.SetReadyReporter(r.reporter())

	delegate := &recordingMonitor{}
	monitor := &healthMonitor{client: c, delegate: delegate}

	ctx := context.Background()
	monitor.OnConnect(ctx, nil)
	monitor.OnDisconnect(ctx, nil, errors.New("dropped"))
	monitor.OnSubscribe(ctx, nil, "a/b")
	monitor.OnUnsubscribe(ctx, nil, "a/b")
	monitor.OnUnsubscribeAll(ctx, nil)

	// Health reporting must not displace the reconnector: every callback still
	// reaches it, including the subscription ones health has no interest in.
	assert.Equal(t,
		[]string{"connect", "disconnect", "subscribe", "unsubscribe", "unsubscribe_all"},
		delegate.seen())
	assert.Len(t, r.all(), 2)
}

func TestReportingIsSafeBeforeRegistration(t *testing.T) {
	// The reporter arrives at registration, after the client is built. A
	// callback firing before then must be a no-op rather than a panic.
	//
	// Today that is not merely an edge case: nothing registers this client at
	// all, so the reporter is always nil and every one of these callbacks takes
	// this path. See the tests below.
	c := buildVWS(t, `
client "vws" "peer" {
    url = "ws://127.0.0.1:1/ws"
}
`)

	assert.NotPanics(t, func() {
		(&healthMonitor{client: c}).OnDisconnect(context.Background(), nil, errors.New("early"))
	})
}

// The client reports no readiness on purpose, because nothing connects it. See
// the note on init() in vws.go, and specs/VWS-CLIENT-GAP.md. These pin that,
// because the instinct on seeing a client with no Ready() is to add one — the
// method is right there, commented out, and looks like an oversight.

func TestTheVWSClientDoesNotContributeToReadiness(t *testing.T) {
	config, diags := cfg.NewConfig().WithSources([]byte(`
client "vws" "peer" {
    url = "ws://127.0.0.1:1/ws"
}
`)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%v", diags)

	peer := config.Clients["vws"]["peer"]
	require.NotNil(t, peer)

	_, implements := peer.(cfg.Readyable)
	assert.False(t, implements,
		"the vws client must not implement Readyable while nothing connects it — "+
			"read the note on Ready() in vws.go before uncommenting it")

	assert.NotContains(t, config.Health.RegisteredComponents(), "client.peer")
}

func TestDeclaringAVWSClientLeavesTheProcessReady(t *testing.T) {
	// The regression this exists to catch. Ready() consults a c.Client that
	// Build never populates, so registering it reported "not started" forever:
	// /readyz 503, and a pod that never enters service, for any deployment that
	// merely declared the block.
	config, diags := cfg.NewConfig().WithSources([]byte(`
client "vws" "peer" {
    url = "ws://127.0.0.1:1/ws"
}
`)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%v", diags)
	config.Health.SetBooted()

	assert.True(t, config.Health.IsReady(context.Background()),
		"declaring a vws client must not make the process unready")
}

func TestReadinessIsRejectedOnAVWSClient(t *testing.T) {
	// The user-visible consequence, and a second thing to revisit when the
	// client learns to connect: there is no readiness to switch off yet.
	_, diags := cfg.NewConfig().WithSources([]byte(`
client "vws" "peer" {
    url       = "ws://127.0.0.1:1/ws"
    readiness = false
}
`)).WithLogger(zap.NewNop()).Build()

	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "does not report whether it is serving")
}
