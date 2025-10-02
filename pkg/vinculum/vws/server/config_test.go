package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/pkg/vinculum/vws"
	"go.uber.org/zap"
)

func TestListenerConfig_BuilderPattern(t *testing.T) {
	logger := zap.NewNop()
	eventBus, err := bus.NewEventBus().WithLogger(logger).Build()
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	t.Run("successful build with all parameters", func(t *testing.T) {
		listener, err := NewListener().
			WithEventBus(eventBus).
			WithLogger(logger).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, listener)
		assert.Equal(t, eventBus, listener.eventBus)
		assert.Equal(t, logger, listener.logger)
	})

	t.Run("fluent interface returns same config", func(t *testing.T) {
		config := NewListener()
		result1 := config.WithEventBus(eventBus)
		result2 := result1.WithLogger(logger)

		assert.Same(t, config, result1)
		assert.Same(t, config, result2)
	})

	t.Run("isValid returns error when missing parameters", func(t *testing.T) {
		// Empty config
		config := NewListener()
		err := config.IsValid()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "EventBus")
		assert.Contains(t, err.Error(), "Logger")

		// Only EventBus
		config.WithEventBus(eventBus)
		err = config.IsValid()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Logger")
		assert.NotContains(t, err.Error(), "EventBus")

		// Only Logger
		config = NewListener().WithLogger(logger)
		err = config.IsValid()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "EventBus")
		assert.NotContains(t, err.Error(), "Logger")

		// Both parameters
		config.WithEventBus(eventBus)
		err = config.IsValid()
		assert.NoError(t, err)
	})

	t.Run("build fails with missing EventBus", func(t *testing.T) {
		listener, err := NewListener().
			WithLogger(logger).
			Build()

		assert.Error(t, err)
		assert.Nil(t, listener)
		assert.Contains(t, err.Error(), "EventBus")
	})

	t.Run("build fails with missing Logger", func(t *testing.T) {
		listener, err := NewListener().
			WithEventBus(eventBus).
			Build()

		assert.Error(t, err)
		assert.Nil(t, listener)
		assert.Contains(t, err.Error(), "Logger")
	})

	t.Run("build fails with missing both parameters", func(t *testing.T) {
		listener, err := NewListener().Build()

		assert.Error(t, err)
		assert.Nil(t, listener)
		assert.Contains(t, err.Error(), "EventBus")
		assert.Contains(t, err.Error(), "Logger")
	})

	t.Run("queue size configuration", func(t *testing.T) {
		// Default queue size
		config := NewListener()
		assert.Equal(t, DefaultQueueSize, config.queueSize)

		// Custom queue size
		config.WithQueueSize(512)
		assert.Equal(t, 512, config.queueSize)

		// Invalid queue size (should be ignored)
		config.WithQueueSize(-10)
		assert.Equal(t, 512, config.queueSize) // Should remain unchanged

		config.WithQueueSize(0)
		assert.Equal(t, 512, config.queueSize) // Should remain unchanged

		// Valid small queue size
		config.WithQueueSize(1)
		assert.Equal(t, 1, config.queueSize)
	})

	t.Run("fluent interface with queue size", func(t *testing.T) {
		listener, err := NewListener().
			WithEventBus(eventBus).
			WithLogger(logger).
			WithQueueSize(1024).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, listener)
		assert.Equal(t, 1024, listener.config.queueSize)
	})

	t.Run("ping interval configuration", func(t *testing.T) {
		// Default ping interval
		config := NewListener()
		assert.Equal(t, DefaultPingInterval, config.pingInterval)

		// Custom ping interval
		config.WithPingInterval(45 * time.Second)
		assert.Equal(t, 45*time.Second, config.pingInterval)

		// Disable ping/pong (0 duration)
		config.WithPingInterval(0)
		assert.Equal(t, time.Duration(0), config.pingInterval)

		// Invalid ping interval (negative - should be ignored)
		config.WithPingInterval(10 * time.Second)
		config.WithPingInterval(-5 * time.Second)
		assert.Equal(t, 10*time.Second, config.pingInterval) // Should remain unchanged
	})

	t.Run("fluent interface with ping interval", func(t *testing.T) {
		listener, err := NewListener().
			WithEventBus(eventBus).
			WithLogger(logger).
			WithPingInterval(60 * time.Second).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, listener)
		assert.Equal(t, 60*time.Second, listener.config.pingInterval)
	})

	t.Run("complete fluent interface", func(t *testing.T) {
		listener, err := NewListener().
			WithEventBus(eventBus).
			WithLogger(logger).
			WithQueueSize(512).
			WithPingInterval(45 * time.Second).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, listener)
		assert.Equal(t, 512, listener.config.queueSize)
		assert.Equal(t, 45*time.Second, listener.config.pingInterval)
	})

	t.Run("event authorization configuration", func(t *testing.T) {
		// Default should be DenyAllEvents
		config := NewListener()
		testMsg := &vws.WireMessage{Topic: "test/topic", Data: "test message"}
		modifiedMsg, err := config.eventAuth(context.Background(), testMsg)
		assert.Error(t, err)
		assert.Nil(t, modifiedMsg)
		assert.Contains(t, err.Error(), "not allowed")

		// Test that we can set different auth functions
		config.WithEventAuth(AllowAllEvents)
		assert.NotNil(t, config.eventAuth)

		config.WithEventAuth(DropAllEvents)
		assert.NotNil(t, config.eventAuth)

		config.WithEventAuth(AllowTopicPrefix("client/"))
		assert.NotNil(t, config.eventAuth)
	})

	t.Run("subscription controller configuration", func(t *testing.T) {
		// Default should be PassthroughSubscriptionController
		config := NewListener()
		assert.NotNil(t, config.subscriptionController)

		// Test that we can set different subscription controller factories
		config.WithSubscriptionController(NewPassthroughSubscriptionController)
		assert.NotNil(t, config.subscriptionController)
	})

	t.Run("initial subscriptions configuration", func(t *testing.T) {
		// Default should be no initial subscriptions
		config := NewListener()
		assert.Empty(t, config.initialSubscriptions)

		// Test setting initial subscriptions
		config.WithInitialSubscriptions("system/alerts", "server/status", "notifications/+")
		assert.Len(t, config.initialSubscriptions, 3)
		assert.Equal(t, []string{"system/alerts", "server/status", "notifications/+"}, config.initialSubscriptions)

		// Test that the slice is copied (not shared)
		originalTopics := []string{"topic1", "topic2"}
		config2 := NewListener()
		config2.WithInitialSubscriptions(originalTopics...)
		originalTopics[0] = "modified"
		assert.Equal(t, []string{"topic1", "topic2"}, config2.initialSubscriptions)

		// Test empty topics (should not change existing subscriptions)
		config3 := NewListener().WithInitialSubscriptions("existing")
		config3.WithInitialSubscriptions() // Empty call
		assert.Equal(t, []string{"existing"}, config3.initialSubscriptions)

		// Test overwriting existing subscriptions
		config4 := NewListener()
		config4.WithInitialSubscriptions("first", "second")
		config4.WithInitialSubscriptions("third", "fourth")
		assert.Equal(t, []string{"third", "fourth"}, config4.initialSubscriptions)

		// Test fluent interface
		config5 := NewListener().
			WithEventBus(eventBus).
			WithLogger(logger).
			WithInitialSubscriptions("fluent/test")

		assert.Equal(t, []string{"fluent/test"}, config5.initialSubscriptions)
		assert.Equal(t, eventBus, config5.eventBus)
		assert.Equal(t, logger, config5.logger)
	})

	t.Run("message transforms configuration", func(t *testing.T) {
		// Default should be no message transforms
		config := NewListener()
		assert.Empty(t, config.messageTransforms)

		// Test setting message transforms using new transform package
		transform1 := func(msg *bus.EventBusMessage) (*bus.EventBusMessage, bool) {
			return msg, true
		}
		transform2 := func(msg *bus.EventBusMessage) (*bus.EventBusMessage, bool) {
			return msg, false
		}

		config.WithMessageTransforms(transform1, transform2)
		assert.Len(t, config.messageTransforms, 2)

		// Test fluent interface
		config2 := NewListener().
			WithEventBus(eventBus).
			WithLogger(logger).
			WithMessageTransforms(transform1)

		assert.Len(t, config2.messageTransforms, 1)
		assert.Equal(t, eventBus, config2.eventBus)
		assert.Equal(t, logger, config2.logger)
	})
}
