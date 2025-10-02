package client

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum-bus"
	"go.uber.org/zap"
)

func TestClientBuilder(t *testing.T) {
	logger := zap.NewNop()
	subscriber := &mockSubscriber{}

	t.Run("successful build with all parameters", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithDialTimeout(10 * time.Second).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, "ws://localhost:8080/ws", client.url)
		assert.Equal(t, logger, client.logger)
		assert.Equal(t, 10*time.Second, client.dialTimeout)
		assert.Equal(t, subscriber, client.subscriber)
	})

	t.Run("fluent interface returns same builder", func(t *testing.T) {
		builder := NewClient()
		assert.Same(t, builder, builder.WithURL("ws://localhost:8080/ws"))
		assert.Same(t, builder, builder.WithLogger(logger))
		assert.Same(t, builder, builder.WithDialTimeout(5*time.Second))
		assert.Same(t, builder, builder.WithSubscriber(subscriber))
		assert.Same(t, builder, builder.WithWriteChannelSize(200))
		assert.Same(t, builder, builder.WithAuthorization("Bearer token123"))
		assert.Same(t, builder, builder.WithAuthorizationProvider(func(ctx context.Context) (string, error) {
			return "Bearer dynamic-token", nil
		}))
		assert.Same(t, builder, builder.WithHeaders(map[string][]string{"X-API-Key": {"key123"}}))
		assert.Same(t, builder, builder.WithHeader("User-Agent", "MyApp/1.0"))
		assert.Same(t, builder, builder.WithMonitor(&mockMonitor{}))
	})

	t.Run("build fails with missing URL", func(t *testing.T) {
		_, err := NewClient().
			WithLogger(logger).
			WithDialTimeout(10 * time.Second).
			WithSubscriber(subscriber).
			Build()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "URL is required")
	})

	t.Run("build succeeds with default logger", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithDialTimeout(10 * time.Second).
			WithSubscriber(subscriber).
			Build()

		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.NotNil(t, client.logger) // Should have default nop logger
	})

	t.Run("build succeeds with default timeout when zero provided", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithDialTimeout(0).
			WithSubscriber(subscriber).
			Build()

		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, 30*time.Second, client.dialTimeout) // Should keep default
	})

	t.Run("default values", func(t *testing.T) {
		builder := NewClient()
		assert.Equal(t, 30*time.Second, builder.dialTimeout)
		assert.NotNil(t, builder.logger)               // Should have nop logger
		assert.Equal(t, 100, builder.writeChannelSize) // Default buffer size
	})

	t.Run("nil logger is ignored", func(t *testing.T) {
		builder := NewClient().WithLogger(nil)
		assert.NotNil(t, builder.logger) // Should keep the default nop logger
	})

	t.Run("zero timeout is ignored", func(t *testing.T) {
		builder := NewClient().WithDialTimeout(0)
		assert.Equal(t, 30*time.Second, builder.dialTimeout) // Should keep default
	})

	t.Run("negative timeout is ignored", func(t *testing.T) {
		builder := NewClient().WithDialTimeout(-5 * time.Second)
		assert.Equal(t, 30*time.Second, builder.dialTimeout) // Should keep default
	})

	t.Run("write channel size configuration", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithWriteChannelSize(500).
			Build()

		require.NoError(t, err)
		assert.Equal(t, 500, client.writeChannelSize)
	})

	t.Run("zero write channel size is ignored", func(t *testing.T) {
		builder := NewClient().WithWriteChannelSize(0)
		assert.Equal(t, 100, builder.writeChannelSize) // Should keep default
	})

	t.Run("negative write channel size is ignored", func(t *testing.T) {
		builder := NewClient().WithWriteChannelSize(-10)
		assert.Equal(t, 100, builder.writeChannelSize) // Should keep default
	})

	t.Run("static authorization configuration", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithAuthorization("Bearer static-token-123").
			Build()

		require.NoError(t, err)
		assert.NotNil(t, client.authProvider)

		// Test that the provider returns the static value
		auth, err := client.authProvider(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "Bearer static-token-123", auth)
	})

	t.Run("dynamic authorization provider configuration", func(t *testing.T) {
		provider := func(ctx context.Context) (string, error) {
			return "Bearer dynamic-token-456", nil
		}

		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithAuthorizationProvider(provider).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, client.authProvider)

		// Test that the provider works
		auth, err := client.authProvider(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "Bearer dynamic-token-456", auth)
	})

	t.Run("static authorization overrides provider", func(t *testing.T) {
		provider := func(ctx context.Context) (string, error) {
			return "Bearer provider-token", nil
		}

		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithAuthorizationProvider(provider).
			WithAuthorization("Bearer static-token"). // This should override
			Build()

		require.NoError(t, err)
		assert.NotNil(t, client.authProvider)

		// Test that the provider returns the static value (last one wins)
		auth, err := client.authProvider(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "Bearer static-token", auth)
	})

	t.Run("provider overrides static authorization", func(t *testing.T) {
		provider := func(ctx context.Context) (string, error) {
			return "Bearer provider-token", nil
		}

		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithAuthorization("Bearer static-token").
			WithAuthorizationProvider(provider). // This should override
			Build()

		require.NoError(t, err)
		assert.NotNil(t, client.authProvider)

		// Test that the provider returns the dynamic value (last one wins)
		auth, err := client.authProvider(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "Bearer provider-token", auth)
	})

	t.Run("custom headers configuration", func(t *testing.T) {
		headers := map[string][]string{
			"X-API-Key":   {"key123"},
			"User-Agent":  {"MyApp/1.0"},
			"X-Client-ID": {"client456"},
		}

		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithHeaders(headers).
			Build()

		require.NoError(t, err)
		assert.Equal(t, headers, client.headers)
	})

	t.Run("single header configuration", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithHeader("X-API-Key", "key123").
			WithHeader("User-Agent", "MyApp/1.0").
			Build()

		require.NoError(t, err)
		expected := map[string][]string{
			"X-API-Key":  {"key123"},
			"User-Agent": {"MyApp/1.0"},
		}
		assert.Equal(t, expected, client.headers)
	})

	t.Run("headers and authorization combined", func(t *testing.T) {
		headers := map[string][]string{
			"X-API-Key":  {"key123"},
			"User-Agent": {"MyApp/1.0"},
		}

		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithHeaders(headers).
			WithAuthorization("Bearer token456").
			Build()

		require.NoError(t, err)
		assert.Equal(t, headers, client.headers)
		assert.NotNil(t, client.authProvider)

		// Test that auth provider works
		auth, err := client.authProvider(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "Bearer token456", auth)
	})

	t.Run("multiple WithHeaders calls merge headers", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithHeaders(map[string][]string{"X-API-Key": {"key123"}}).
			WithHeaders(map[string][]string{"User-Agent": {"MyApp/1.0"}}).
			Build()

		require.NoError(t, err)
		expected := map[string][]string{
			"X-API-Key":  {"key123"},
			"User-Agent": {"MyApp/1.0"},
		}
		assert.Equal(t, expected, client.headers)
	})

	t.Run("monitor configuration", func(t *testing.T) {
		monitor := &mockMonitor{}
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithMonitor(monitor).
			Build()

		require.NoError(t, err)
		assert.Equal(t, monitor, client.monitor)
	})

	t.Run("nil monitor is allowed", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			WithMonitor(nil).
			Build()

		require.NoError(t, err)
		assert.Nil(t, client.monitor)
	})

	t.Run("no monitor by default", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)
		assert.Nil(t, client.monitor)
	})
}

func TestClientLifecycle(t *testing.T) {
	logger := zap.NewNop()
	subscriber := &mockSubscriber{}

	t.Run("client starts disconnected", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)
		assert.Equal(t, int32(0), client.started)
		assert.Equal(t, int32(0), client.stopping)
	})

	t.Run("operations fail when not connected", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)

		ctx := context.Background()

		// These should all fail because client is not connected
		err = client.Subscribe(ctx, "test/topic")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "client is not connected")

		err = client.Unsubscribe(ctx, "test/topic")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "client is not connected")

		err = client.Publish(ctx, "test/topic", "test message")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "client is not connected")
	})

	t.Run("connect fails with invalid URL", func(t *testing.T) {
		client, err := NewClient().
			WithURL("invalid-url").
			WithLogger(logger).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)

		err = client.Connect(context.Background())
		assert.Error(t, err)
		// The error could be either "invalid URL" or "failed to connect to WebSocket"
		assert.True(t,
			strings.Contains(err.Error(), "invalid URL") ||
				strings.Contains(err.Error(), "failed to connect to WebSocket"),
			"Expected error about invalid URL or connection failure, got: %s", err.Error())
	})

	t.Run("disconnect on unconnected client is safe", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)

		err = client.Disconnect()
		assert.NoError(t, err)
	})

	t.Run("multiple disconnect calls are safe", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)

		err = client.Disconnect()
		assert.NoError(t, err)

		err = client.Disconnect()
		assert.NoError(t, err)
	})
}

func TestClientReconnection(t *testing.T) {
	logger := zap.NewNop()
	subscriber := &mockSubscriber{}
	monitor := &mockMonitor{}

	t.Run("state reset after failed connect allows reconnection", func(t *testing.T) {
		// Use invalid URL scheme for fast failure
		client, err := NewClient().
			WithURL("invalid-scheme://test").
			WithLogger(logger).
			WithSubscriber(subscriber).
			WithMonitor(monitor).
			Build()

		require.NoError(t, err)

		// Initial state - should be disconnected
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.started))
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping))

		ctx := context.Background()

		// First connect attempt should fail quickly
		err = client.Connect(ctx)
		assert.Error(t, err)

		// After failed connect, state should be reset to allow reconnection
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.started))
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping))

		// Should be able to try connecting again (will also fail)
		err = client.Connect(ctx)
		assert.Error(t, err)

		// State should still be reset
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.started))
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping))
	})

	t.Run("disconnect before connect is safe", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)

		// Disconnect before ever connecting should be safe
		err = client.Disconnect()
		assert.NoError(t, err)

		// State should remain clean
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.started))
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping))
	})

	t.Run("successful connect then disconnect then reconnect", func(t *testing.T) {
		// This test requires a real WebSocket server running on localhost:8080
		// Skip if no server is available
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			WithMonitor(monitor).
			WithDialTimeout(500 * time.Millisecond).
			Build()

		require.NoError(t, err)

		// Reset monitor state
		monitor.connects = nil
		monitor.disconnects = nil

		ctx := context.Background()

		// Try to connect - this might succeed if server is running
		err = client.Connect(ctx)
		if err != nil {
			// No server running, skip this test
			t.Skip("Skipping test - no WebSocket server available on localhost:8080")
			return
		}

		// If we get here, connection succeeded
		assert.Equal(t, int32(1), atomic.LoadInt32(&client.started))
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping))

		// Monitor should have been called for successful connect
		assert.Len(t, monitor.connects, 1)
		assert.Len(t, monitor.disconnects, 0)

		// Disconnect
		err = client.Disconnect()
		assert.NoError(t, err)

		// State should be reset
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.started))
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping))

		// Monitor should have been called for disconnect
		assert.Len(t, monitor.disconnects, 1)
		assert.Nil(t, monitor.disconnects[0].err) // Graceful disconnect

		// Should be able to reconnect
		err = client.Connect(ctx)
		assert.NoError(t, err)

		// State should be connected again
		assert.Equal(t, int32(1), atomic.LoadInt32(&client.started))
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping))

		// Monitor should have been called for second connect
		assert.Len(t, monitor.connects, 2)

		// Clean up
		client.Disconnect()
	})

	t.Run("error disconnect resets state for reconnection", func(t *testing.T) {
		// Create a client that will fail to connect
		client, err := NewClient().
			WithURL("ws://127.0.0.1:65432/ws").
			WithSubscriber(subscriber).
			WithDialTimeout(50 * time.Millisecond).
			Build()
		require.NoError(t, err)

		// Simulate the scenario where client connects but then has an error
		// First, manually set the started flag to simulate a connected state
		atomic.StoreInt32(&client.started, 1)

		// Simulate an error disconnect by calling notifyDisconnectError
		testErr := fmt.Errorf("simulated connection error")
		client.notifyDisconnectError(testErr)

		// Wait for async cleanup to complete by polling the state
		// The cleanup is now asynchronous, so we need to wait for it
		maxWait := 100 * time.Millisecond
		pollInterval := 5 * time.Millisecond
		deadline := time.Now().Add(maxWait)

		for time.Now().Before(deadline) {
			if atomic.LoadInt32(&client.started) == 0 && atomic.LoadInt32(&client.stopping) == 0 {
				break // Cleanup completed
			}
			time.Sleep(pollInterval)
		}

		// Verify cleanup completed
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.started), "started flag should be reset")
		assert.Equal(t, int32(0), atomic.LoadInt32(&client.stopping), "stopping flag should be reset")

		// Now try to connect again - this should not fail with "client is already started"
		err = client.Connect(context.Background())
		assert.Error(t, err) // Should fail due to no server, but NOT due to "already started"
		assert.Contains(t, err.Error(), "failed to connect to WebSocket")
		assert.NotContains(t, err.Error(), "client is already started")
	})
}

func TestClientMonitor(t *testing.T) {
	logger := zap.NewNop()
	subscriber := &mockSubscriber{}
	monitor := &mockMonitor{}

	t.Run("monitor receives all lifecycle events", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			WithMonitor(monitor).
			Build()

		require.NoError(t, err)

		// Test that monitor methods are called (we can't test actual network operations in unit tests)
		// But we can test that the monitor is properly configured and would be called

		// Verify monitor is set
		assert.Equal(t, monitor, client.monitor)

		// Test that operations would call monitor (these will fail due to no connection, but that's expected)
		ctx := context.Background()

		// These operations will fail because there's no actual connection, but we can verify
		// the monitor integration is in place by checking the client has the monitor
		assert.NotNil(t, client.monitor)

		// Test Subscribe would call monitor (fails due to no connection)
		err = client.Subscribe(ctx, "test/topic")
		assert.Error(t, err) // Expected to fail - not connected
		assert.Contains(t, err.Error(), "not connected")

		// Test Unsubscribe would call monitor (fails due to no connection)
		err = client.Unsubscribe(ctx, "test/topic")
		assert.Error(t, err) // Expected to fail - not connected
		assert.Contains(t, err.Error(), "not connected")

		// Test UnsubscribeAll would call monitor (fails due to no connection)
		err = client.UnsubscribeAll(ctx)
		assert.Error(t, err) // Expected to fail - not connected
		assert.Contains(t, err.Error(), "not connected")
	})

	t.Run("client works without monitor", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			Build()

		require.NoError(t, err)
		assert.Nil(t, client.monitor)

		// Operations should work fine without monitor (though they'll fail due to no connection)
		ctx := context.Background()
		err = client.Subscribe(ctx, "test/topic")
		assert.Error(t, err) // Expected to fail - not connected
		assert.Contains(t, err.Error(), "not connected")
	})

	t.Run("disconnect calls monitor with nil error for graceful disconnect", func(t *testing.T) {
		client, err := NewClient().
			WithURL("ws://localhost:8080/ws").
			WithLogger(logger).
			WithSubscriber(subscriber).
			WithMonitor(monitor).
			Build()

		require.NoError(t, err)

		// Reset monitor state
		monitor.disconnects = nil

		// Call disconnect (should be safe even when not connected)
		err = client.Disconnect()
		assert.NoError(t, err)

		// Verify monitor was called with nil error (graceful disconnect)
		assert.Len(t, monitor.disconnects, 1)
		assert.Nil(t, monitor.disconnects[0].err)
	})
}

// mockSubscriber is a test helper that implements the Subscriber interface
type mockSubscriber struct {
	subscriptions   []string
	unsubscriptions []string
	events          []eventData
	passThrough     []bus.EventBusMessage
}

type eventData struct {
	topic   string
	message interface{}
	fields  map[string]string
}

func (m *mockSubscriber) OnSubscribe(ctx context.Context, topic string) error {
	m.subscriptions = append(m.subscriptions, topic)
	return nil
}

func (m *mockSubscriber) OnUnsubscribe(ctx context.Context, topic string) error {
	m.unsubscriptions = append(m.unsubscriptions, topic)
	return nil
}

func (m *mockSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	m.events = append(m.events, eventData{
		topic:   topic,
		message: message,
		fields:  fields,
	})
	return nil
}

func (m *mockSubscriber) PassThrough(msg bus.EventBusMessage) error {
	m.passThrough = append(m.passThrough, msg)
	return nil
}

// mockMonitor is a test helper that implements the ClientMonitor interface
type mockMonitor struct {
	connects       []context.Context
	disconnects    []disconnectData
	subscribes     []string
	unsubscribes   []string
	unsubscribeAll []context.Context
}

type disconnectData struct {
	ctx context.Context
	err error
}

func (m *mockMonitor) OnConnect(ctx context.Context, client bus.Client) {
	m.connects = append(m.connects, ctx)
}

func (m *mockMonitor) OnDisconnect(ctx context.Context, client bus.Client, err error) {
	m.disconnects = append(m.disconnects, disconnectData{ctx: ctx, err: err})
}

func (m *mockMonitor) OnSubscribe(ctx context.Context, client bus.Client, topic string) {
	m.subscribes = append(m.subscribes, topic)
}

func (m *mockMonitor) OnUnsubscribe(ctx context.Context, client bus.Client, topic string) {
	m.unsubscribes = append(m.unsubscribes, topic)
}

func (m *mockMonitor) OnUnsubscribeAll(ctx context.Context, client bus.Client) {
	m.unsubscribeAll = append(m.unsubscribeAll, ctx)
}
