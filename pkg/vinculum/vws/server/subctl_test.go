package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tsarna/vinculum-bus"
	"go.uber.org/zap"
)

func TestSubscriptionControllers(t *testing.T) {
	eventBus := &mockEventBus{}
	logger := zap.NewNop()

	t.Run("subscription controller configuration", func(t *testing.T) {
		// Default should be PassthroughSubscriptionController
		config := NewListener()
		assert.NotNil(t, config.subscriptionController)

		// Test that we can create the default controller
		controller := config.subscriptionController(logger)
		assert.NotNil(t, controller)

		// Test PassthroughSubscriptionController behavior
		ctx := context.Background()
		mockSubscriber := &mockSubscriber{}

		err := controller.Subscribe(ctx, eventBus, mockSubscriber, "test/topic")
		assert.NoError(t, err) // Should allow all subscriptions

		err = controller.Unsubscribe(ctx, eventBus, mockSubscriber, "test/topic")
		assert.NoError(t, err) // Should allow all unsubscriptions

		// Test custom subscription controller factory
		customControllerCalled := false
		customFactory := func(log *zap.Logger) SubscriptionController {
			customControllerCalled = true
			assert.Equal(t, logger, log)
			return &testSubscriptionController{}
		}

		config.WithSubscriptionController(customFactory)
		controller = config.subscriptionController(logger)
		assert.True(t, customControllerCalled)
		assert.IsType(t, &testSubscriptionController{}, controller)
	})

	t.Run("PassthroughSubscriptionController", func(t *testing.T) {
		controller := NewPassthroughSubscriptionController(logger)
		ctx := context.Background()
		mockSubscriber := &mockSubscriber{}

		// Should allow any subscription
		err := controller.Subscribe(ctx, eventBus, mockSubscriber, "any/topic/pattern")
		assert.NoError(t, err)

		err = controller.Subscribe(ctx, eventBus, mockSubscriber, "sensor/+/data")
		assert.NoError(t, err)

		err = controller.Subscribe(ctx, eventBus, mockSubscriber, "events/#")
		assert.NoError(t, err)

		// Should allow any unsubscription
		err = controller.Unsubscribe(ctx, eventBus, mockSubscriber, "any/topic/pattern")
		assert.NoError(t, err)

		err = controller.Unsubscribe(ctx, eventBus, mockSubscriber, "sensor/+/data")
		assert.NoError(t, err)

		err = controller.Unsubscribe(ctx, eventBus, mockSubscriber, "events/#")
		assert.NoError(t, err)
	})

	t.Run("testSubscriptionController", func(t *testing.T) {
		controller := &testSubscriptionController{}
		ctx := context.Background()
		mockSubscriber := &mockSubscriber{}

		// Should allow normal subscriptions
		err := controller.Subscribe(ctx, eventBus, mockSubscriber, "allowed/topic")
		assert.NoError(t, err)
		assert.True(t, controller.subscribeCalled)

		// Should deny specific topics
		controller.subscribeCalled = false
		err = controller.Subscribe(ctx, eventBus, mockSubscriber, "denied/topic")
		assert.Error(t, err)
		assert.True(t, controller.subscribeCalled)
		assert.Contains(t, err.Error(), "subscription denied")

		// Should allow normal unsubscriptions
		err = controller.Unsubscribe(ctx, eventBus, mockSubscriber, "allowed/topic")
		assert.NoError(t, err)
		assert.True(t, controller.unsubscribeCalled)

		// Should deny specific unsubscriptions
		controller.unsubscribeCalled = false
		err = controller.Unsubscribe(ctx, eventBus, mockSubscriber, "denied/topic")
		assert.Error(t, err)
		assert.True(t, controller.unsubscribeCalled)
		assert.Contains(t, err.Error(), "unsubscription denied")

		// Should allow unsubscribe all
		err = controller.UnsubscribeAll(ctx, eventBus, mockSubscriber)
		assert.NoError(t, err)
		assert.True(t, controller.unsubscribeAllCalled)
	})
}

// mockSubscriber implements bus.Subscriber for testing
type mockSubscriber struct {
	onSubscribeCalled   bool
	onUnsubscribeCalled bool
	onEventCalled       bool
}

func (m *mockSubscriber) OnSubscribe(ctx context.Context, topic string) error {
	m.onSubscribeCalled = true
	return nil
}

func (m *mockSubscriber) OnUnsubscribe(ctx context.Context, topic string) error {
	m.onUnsubscribeCalled = true
	return nil
}

func (m *mockSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	m.onEventCalled = true
	return nil
}

func (m *mockSubscriber) PassThrough(msg bus.EventBusMessage) error {
	return nil
}

// testSubscriptionController is a test implementation that demonstrates advanced features
type testSubscriptionController struct {
	subscribeCalled      bool
	unsubscribeCalled    bool
	unsubscribeAllCalled bool
}

func (t *testSubscriptionController) Subscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error {
	t.subscribeCalled = true
	if topicPattern == "denied/topic" {
		return fmt.Errorf("subscription denied")
	}
	// Make the actual EventBus call for allowed subscriptions
	return eventBus.Subscribe(ctx, subscriber, topicPattern)
}

func (t *testSubscriptionController) Unsubscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error {
	t.unsubscribeCalled = true
	if topicPattern == "denied/topic" {
		return fmt.Errorf("unsubscription denied")
	}
	// Make the actual EventBus call for allowed unsubscriptions
	return eventBus.Unsubscribe(ctx, subscriber, topicPattern)
}

func (t *testSubscriptionController) UnsubscribeAll(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber) error {
	t.unsubscribeAllCalled = true
	// Make the actual EventBus call
	return eventBus.UnsubscribeAll(ctx, subscriber)
}

// mockEventBus implements bus.EventBus for testing
type mockEventBus struct{}

// EventBus methods
func (m *mockEventBus) Subscribe(ctx context.Context, subscriber bus.Subscriber, topicPattern string) error {
	return nil
}

func (m *mockEventBus) Unsubscribe(ctx context.Context, subscriber bus.Subscriber, topicPattern string) error {
	return nil
}

func (m *mockEventBus) Publish(ctx context.Context, topic string, message any) error {
	return nil
}

func (m *mockEventBus) UnsubscribeAll(ctx context.Context, subscriber bus.Subscriber) error {
	return nil
}

func (m *mockEventBus) PublishSync(ctx context.Context, topic string, message any) error {
	return nil
}

func (m *mockEventBus) Start() error {
	return nil
}

func (m *mockEventBus) Stop() error {
	return nil
}

// Subscriber methods (EventBus embeds Subscriber)
func (m *mockEventBus) OnSubscribe(ctx context.Context, topic string) error {
	return nil
}

func (m *mockEventBus) OnUnsubscribe(ctx context.Context, topic string) error {
	return nil
}

func (m *mockEventBus) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	return nil
}

func (m *mockEventBus) PassThrough(msg bus.EventBusMessage) error {
	return nil
}
