package vws

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	vwsserver "github.com/tsarna/vinculum-vws/server"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// A vws server is always mounted, so it never owns a listener — but it does own
// connections, and http.Server.Shutdown leaves hijacked connections alone. It
// therefore has to register a Drainable of its own or its clients are severed
// at process exit with no close frame.
func TestVwsServerRegistersAsDrainable(t *testing.T) {
	vcl := `
bus "main" {}

server "vws" "ws" {
    bus              = bus.main
    shutdown_timeout = "3s"
}
`
	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	t.Cleanup(func() {
		for _, b := range c.Buses {
			_ = b.Stop()
		}
	})

	require.Len(t, c.Drainables, 1, "a mounted vws server should still register a Drainable")

	srv := c.Servers["vws"]["ws"].(*VinculumWebsocketServer)
	assert.Equal(t, 3*time.Second, srv.shutdownTimeout, "shutdown_timeout should reach the server")
}

// Drain has to close the connections and wait for them to go, because the bus
// they publish into is stopped as soon as it returns.
func TestVwsDrainClosesOpenConnections(t *testing.T) {
	eventBus, err := bus.NewEventBus().WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	listener, err := vwsserver.NewListener().
		WithEventBus(eventBus).
		WithLogger(zap.NewNop()).
		WithServerName("ws").
		Build()
	require.NoError(t, err)

	ts := httptest.NewServer(listener)
	t.Cleanup(ts.Close)

	srv := &VinculumWebsocketServer{Listener: listener, shutdownTimeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+ts.URL[len("http"):], nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// A client that reads, so it answers the closing handshake the way a real
	// one does. The returned context is cancelled when the connection closes.
	closed := conn.CloseRead(ctx)

	require.Eventually(t, func() bool {
		return listener.ConnectionCount() == 1
	}, 5*time.Second, 10*time.Millisecond, "connection should be tracked")

	start := time.Now()
	require.NoError(t, srv.Drain(ctx))
	assert.Less(t, time.Since(start), 3*time.Second,
		"a responsive client should close promptly, not sit out the timeout")
	assert.Equal(t, 0, listener.ConnectionCount(),
		"Drain must not return while connections are still open")

	select {
	case <-closed.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the client's connection should be closed by the drain")
	}
}

// A client that has stopped reading cannot answer the closing handshake, and
// must not be able to hold shutdown open past shutdown_timeout.
func TestVwsDrainStopsAtTheTimeout(t *testing.T) {
	eventBus, err := bus.NewEventBus().WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	listener, err := vwsserver.NewListener().
		WithEventBus(eventBus).
		WithLogger(zap.NewNop()).
		WithServerName("ws").
		Build()
	require.NoError(t, err)

	ts := httptest.NewServer(listener)
	t.Cleanup(ts.Close)

	srv := &VinculumWebsocketServer{Listener: listener, shutdownTimeout: 300 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Deliberately never Read.
	for range 3 {
		conn, _, err := websocket.Dial(ctx, "ws"+ts.URL[len("http"):], nil)
		require.NoError(t, err)
		defer conn.Close(websocket.StatusAbnormalClosure, "")
	}
	require.Eventually(t, func() bool {
		return listener.ConnectionCount() == 3
	}, 5*time.Second, 10*time.Millisecond, "connections should be tracked")

	start := time.Now()
	err = srv.Drain(ctx)

	assert.Error(t, err, "clients that outlast shutdown_timeout should be reported")
	assert.Less(t, time.Since(start), 3*time.Second,
		"Drain must give up at shutdown_timeout rather than once per stuck client")
}
