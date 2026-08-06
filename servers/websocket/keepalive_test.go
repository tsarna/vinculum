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
	"go.uber.org/zap"
)

// ping_interval and write_timeout were accepted by the parser, documented in the
// schema, and then dropped: the code that applied them was commented out behind
// a TODO and the builder had no methods to call. These pin the behaviour they
// were supposed to have.

func TestKeepaliveDefaults(t *testing.T) {
	c := NewServer()
	assert.Equal(t, DefaultPingInterval, c.pingInterval)
	assert.Equal(t, DefaultWriteTimeout, c.writeTimeout)
}

func TestKeepaliveSettersReachTheConfig(t *testing.T) {
	c := NewServer().
		WithPingInterval(45 * time.Second).
		WithWriteTimeout(3 * time.Second)
	assert.Equal(t, 45*time.Second, c.pingInterval)
	assert.Equal(t, 3*time.Second, c.writeTimeout)

	// Zero disables rather than falling back to the default, so a config author
	// who wants no pings can say so.
	off := NewServer().WithPingInterval(0).WithWriteTimeout(0)
	assert.Zero(t, off.pingInterval)
	assert.Zero(t, off.writeTimeout)
}

// A write must not be able to outlive the timeout, and must not be bounded at
// all when the timeout is disabled — the two halves of the same decision.
func TestWriteContextHonoursTheTimeout(t *testing.T) {
	base := context.Background()

	bounded := &Connection{ctx: base, config: NewServer().WithWriteTimeout(50 * time.Millisecond)}
	ctx, cancel := bounded.writeContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "a positive write_timeout must produce a deadline")
	assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), deadline, 20*time.Millisecond)

	unbounded := &Connection{ctx: base, config: NewServer().WithWriteTimeout(0)}
	ctx, cancel = unbounded.writeContext()
	defer cancel()
	_, ok = ctx.Deadline()
	assert.False(t, ok, "a disabled write_timeout must not bound the write")
}

// End to end: a connection with pings enabled keeps working. The ticker shares
// the outbound queue with real messages, so this is what catches a ping that
// interleaves badly with a write or wedges the queue.
func TestConnectionSurvivesPingsAndKeepsDelivering(t *testing.T) {
	eventBus, err := bus.NewEventBus().WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	listener, err := NewServer().
		WithEventBus(eventBus).
		WithLogger(zap.NewNop()).
		WithPingInterval(20 * time.Millisecond).
		WithWriteTimeout(2 * time.Second).
		WithInitialSubscriptions("out/#").
		Build()
	require.NoError(t, err)

	srv := httptest.NewServer(listener)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Outlast several ping intervals before exchanging anything, so the message
	// below has to survive a queue that has already carried ticks.
	time.Sleep(120 * time.Millisecond)

	require.NoError(t, eventBus.Publish(ctx, "out/greeting", "hello"))

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	typ, data, err := conn.Read(readCtx)
	require.NoError(t, err, "connection should still deliver after several pings")
	assert.Equal(t, websocket.MessageText, typ)
	assert.Equal(t, "hello", string(data))
}

// The point of a ping is to notice a peer that has gone away without closing.
// A client that never reads never pongs, so the server's Ping cannot complete;
// it must give up at write_timeout and drop the connection rather than holding
// it and its queue open forever. This is the assertion that fails if pings are
// configured but never actually sent.
func TestPingDropsAPeerThatStoppedResponding(t *testing.T) {
	eventBus, err := bus.NewEventBus().WithLogger(zap.NewNop()).Build()
	require.NoError(t, err)
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	listener, err := NewServer().
		WithEventBus(eventBus).
		WithLogger(zap.NewNop()).
		WithPingInterval(20 * time.Millisecond).
		WithWriteTimeout(50 * time.Millisecond).
		Build()
	require.NoError(t, err)

	srv := httptest.NewServer(listener)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusAbnormalClosure, "")

	// Deliberately never Read, so the client library never answers the ping.
	require.Eventually(t, func() bool {
		return listener.ConnectionCount() == 0
	}, 5*time.Second, 20*time.Millisecond,
		"an unanswered ping must tear the connection down")
}
