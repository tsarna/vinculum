package websocketserver

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// A websocket server is always mounted, so it never owns a listener — but it
// does own connections, and http.Server.Shutdown leaves hijacked connections
// alone. It therefore has to register a Drainable of its own or its clients are
// simply severed at process exit.
func TestWebsocketServerRegistersAsDrainable(t *testing.T) {
	vcl := `
bus "main" {}

server "websocket" "ws" {
    bus              = bus.main
    shutdown_timeout = "2s"
}
`
	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	t.Cleanup(func() {
		for _, b := range c.Buses {
			_ = b.Stop()
		}
	})

	require.Len(t, c.Drainables, 1, "a mounted websocket server should still register a Drainable")

	srv := c.Servers["websocket"]["ws"].(*WebsocketServer)
	assert.Equal(t, 2*time.Second, srv.shutdownTimeout, "shutdown_timeout should reach the server")
}

// Drain must actually close the connections, and must not return while they are
// still open — the buses those connections publish into are stopped as soon as
// it does.
func TestDrainClosesOpenConnections(t *testing.T) {
	eventBus, err := bus.NewEventBus().WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	listener, err := NewServer().
		WithEventBus(eventBus).
		WithLogger(zap.NewNop()).
		WithInitialSubscriptions("out/#").
		Build()
	require.NoError(t, err)

	ts := httptest.NewServer(listener)
	t.Cleanup(ts.Close)

	srv := &WebsocketServer{Listener: listener, shutdownTimeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	// The peer sees a closed connection rather than a silently dead socket.
	select {
	case <-closed.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the client's connection should be closed by the drain")
	}
}

// Clients that have stopped reading cannot answer the closing handshake, so
// each close sits out the WebSocket library's own handshake timeout. Those
// waits have to overlap and be bounded by shutdown_timeout — closing
// connections one after another would multiply that wait by the number of
// stuck clients and leave shutdown_timeout governing nothing.
func TestDrainStopsAtTheTimeoutWithUnresponsiveClients(t *testing.T) {
	eventBus, err := bus.NewEventBus().WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	listener, err := NewServer().WithEventBus(eventBus).WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)

	ts := httptest.NewServer(listener)
	t.Cleanup(ts.Close)

	srv := &WebsocketServer{Listener: listener, shutdownTimeout: 300 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Deliberately never Read on either connection.
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
	elapsed := time.Since(start)

	assert.Error(t, err, "clients that outlast shutdown_timeout should be reported")
	assert.Less(t, elapsed, 3*time.Second,
		"Drain must give up at shutdown_timeout rather than once per stuck client")
}

// Once drained, the server refuses new upgrades — a client that reconnects
// during shutdown must not get a connection onto a bus that is going away.
func TestDrainedServerRefusesNewConnections(t *testing.T) {
	eventBus, err := bus.NewEventBus().WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	listener, err := NewServer().WithEventBus(eventBus).WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)

	ts := httptest.NewServer(listener)
	t.Cleanup(ts.Close)

	srv := &WebsocketServer{Listener: listener, shutdownTimeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, srv.Drain(ctx))

	_, _, err = websocket.Dial(ctx, "ws"+ts.URL[len("http"):], nil)
	assert.Error(t, err, "a drained server should not accept new connections")
}
