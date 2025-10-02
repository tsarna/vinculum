package server

import (
	"context"
	"fmt"

	"github.com/tsarna/vinculum-bus"
	"go.uber.org/zap"
)

// SubscriptionController controls what topic patterns clients can subscribe to
// and unsubscribe from. It is responsible for making the actual EventBus calls
// and can reject subscriptions, rewrite them, or split them into multiple subscriptions.
type SubscriptionController interface {
	// Subscribe is called when a client wants to subscribe to a topic pattern.
	// The controller is responsible for making the actual EventBus.Subscribe call(s).
	// It can:
	//   - Call eventBus.Subscribe with the original pattern to allow requests as-is
	//   - Call eventBus.Subscribe with different patterns to modify the subscription
	//   - Call eventBus.Subscribe multiple times to split into multiple subscriptions
	//   - Return an error without calling eventBus.Subscribe to deny the subscription
	Subscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error

	// Unsubscribe is called when a client wants to unsubscribe from a topic pattern.
	// The controller is responsible for making the actual EventBus.Unsubscribe call(s).
	// It can:
	//   - Call eventBus.Unsubscribe with the original pattern to allow as-is
	//   - Call eventBus.Unsubscribe with different patterns to modify the unsubscription
	//   - Call eventBus.Unsubscribe multiple times to handle multiple subscriptions
	//   - Return an error without calling eventBus.Unsubscribe to deny the unsubscription
	Unsubscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error

	// UnsubscribeAll is called when a client wants to unsubscribe from all topics.
	// The controller is responsible for making the actual EventBus.UnsubscribeAll call.
	// It can:
	//   - Call eventBus.UnsubscribeAll to allow as-is
	//   - Call specific eventBus.Unsubscribe calls to handle partial unsubscriptions
	//   - Return an error without calling eventBus methods to deny the unsubscribe all
	UnsubscribeAll(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber) error
}

// SubscriptionControllerFactory creates a SubscriptionController instance.
// It receives a logger that the controller can use for logging.
type SubscriptionControllerFactory func(logger *zap.Logger) SubscriptionController

// Predefined subscription controllers

// PassthroughSubscriptionController is the default SubscriptionController that
// allows all subscriptions and unsubscriptions without modification by directly
// calling the EventBus methods.
type PassthroughSubscriptionController struct {
	logger *zap.Logger
}

// NewPassthroughSubscriptionController creates a PassthroughSubscriptionController.
// This is the default subscription controller factory.
func NewPassthroughSubscriptionController(logger *zap.Logger) SubscriptionController {
	return &PassthroughSubscriptionController{
		logger: logger,
	}
}

// Subscribe allows the subscription as-is by calling EventBus.Subscribe directly.
func (p *PassthroughSubscriptionController) Subscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error {
	return eventBus.Subscribe(ctx, subscriber, topicPattern)
}

// Unsubscribe allows the unsubscription as-is by calling EventBus.Unsubscribe directly.
func (p *PassthroughSubscriptionController) Unsubscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error {
	return eventBus.Unsubscribe(ctx, subscriber, topicPattern)
}

// UnsubscribeAll allows the unsubscribe all as-is by calling EventBus.UnsubscribeAll directly.
func (p *PassthroughSubscriptionController) UnsubscribeAll(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber) error {
	return eventBus.UnsubscribeAll(ctx, subscriber)
}

// Example subscription controller implementations

// TopicPrefixSubscriptionController only allows subscriptions to topics with a specific prefix.
// This is useful for sandboxing client subscriptions. It embeds PassthroughSubscriptionController
// to delegate EventBus calls when subscriptions are allowed.
type TopicPrefixSubscriptionController struct {
	*PassthroughSubscriptionController
	prefix string
}

// NewTopicPrefixSubscriptionController creates a factory for TopicPrefixSubscriptionController.
func NewTopicPrefixSubscriptionController(prefix string) SubscriptionControllerFactory {
	return func(logger *zap.Logger) SubscriptionController {
		return &TopicPrefixSubscriptionController{
			PassthroughSubscriptionController: &PassthroughSubscriptionController{
				logger: logger,
			},
			prefix: prefix,
		}
	}
}

// Subscribe only allows subscriptions to topics with the configured prefix.
// If allowed, it delegates to the embedded PassthroughSubscriptionController.
func (t *TopicPrefixSubscriptionController) Subscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error {
	if len(topicPattern) >= len(t.prefix) && topicPattern[:len(t.prefix)] == t.prefix {
		// Delegate to passthrough controller to make the actual EventBus call
		return t.PassthroughSubscriptionController.Subscribe(ctx, eventBus, subscriber, topicPattern)
	}
	return fmt.Errorf("subscriptions only allowed to topics with prefix: %s", t.prefix)
}

// Unsubscribe only allows unsubscriptions from topics with the configured prefix.
// If allowed, it delegates to the embedded PassthroughSubscriptionController.
func (t *TopicPrefixSubscriptionController) Unsubscribe(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber, topicPattern string) error {
	if len(topicPattern) >= len(t.prefix) && topicPattern[:len(t.prefix)] == t.prefix {
		// Delegate to passthrough controller to make the actual EventBus call
		return t.PassthroughSubscriptionController.Unsubscribe(ctx, eventBus, subscriber, topicPattern)
	}
	return fmt.Errorf("unsubscriptions only allowed from topics with prefix: %s", t.prefix)
}

// UnsubscribeAll allows unsubscribing from all topics - no prefix restrictions apply.
// It delegates to the embedded PassthroughSubscriptionController.
func (t *TopicPrefixSubscriptionController) UnsubscribeAll(ctx context.Context, eventBus bus.EventBus, subscriber bus.Subscriber) error {
	// Delegate to passthrough controller to make the actual EventBus call
	return t.PassthroughSubscriptionController.UnsubscribeAll(ctx, eventBus, subscriber)
}
