package server

import (
	"fmt"
	"time"

	"github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/o11y"
	"github.com/tsarna/vinculum-bus/transform"
	"go.uber.org/zap"
)

// ListenerConfig holds the configuration for creating a WebSocket Listener.
// Use NewListener() to create a new configuration and chain methods
// to set the required parameters before calling Build().
type ListenerConfig struct {
	eventBus               bus.EventBus
	logger                 *zap.Logger
	metricsProvider        o11y.MetricsProvider
	queueSize              int
	pingInterval           time.Duration
	writeTimeout           time.Duration
	eventAuth              EventAuthFunc
	subscriptionController SubscriptionControllerFactory
	initialSubscriptions   []string
	messageTransforms      []transform.MessageTransformFunc
}

const (
	// DefaultQueueSize is the default size for the WebSocket message queue.
	// This provides a good balance between memory usage and burst handling capacity.
	DefaultQueueSize = 256

	// DefaultPingInterval is the default interval for sending WebSocket ping frames.
	// This helps detect dead connections and maintain connection health.
	DefaultPingInterval = 30 * time.Second

	// DefaultReadTimeout is the default timeout for reading messages from clients.
	// Should be longer than ping interval to allow for pong responses.
	DefaultReadTimeout = 60 * time.Second

	// DefaultWriteTimeout is the default timeout for writing messages to clients.
	// Should be short enough to detect slow/dead clients quickly.
	DefaultWriteTimeout = 10 * time.Second
)

// NewListener creates a new ListenerConfig for building a WebSocket Listener.
// Use the fluent methods to set the required EventBus and Logger, then call Build().
//
// Example:
//
//	listener, err := vws.NewListener().
//	    WithEventBus(eventBus).
//	    WithLogger(logger).
//	    WithMetricsProvider(metricsProvider).
//	    WithQueueSize(512).
//	    WithPingInterval(45 * time.Second).
//	    WithEventAuth(AllowTopicPrefix("client/")).
//	    WithSubscriptionController(myControllerFactory).
//	    WithInitialSubscriptions("system/alerts", "server/status").
//	    WithMessageTransforms(filterSensitive, addTimestamp).
//	    Build()
func NewListener() *ListenerConfig {
	return &ListenerConfig{
		queueSize:              DefaultQueueSize,
		pingInterval:           DefaultPingInterval,
		writeTimeout:           DefaultWriteTimeout,
		eventAuth:              DenyAllEvents,                        // Secure default: deny all client events
		subscriptionController: NewPassthroughSubscriptionController, // Default: allow all subscriptions
	}
}

// WithEventBus sets the EventBus for the WebSocket Listener.
// The EventBus is required for integrating WebSocket connections with the pub/sub system.
func (c *ListenerConfig) WithEventBus(eventBus bus.EventBus) *ListenerConfig {
	c.eventBus = eventBus
	return c
}

// WithLogger sets the Logger for the WebSocket Listener.
// The Logger is required for connection events, errors, and debugging.
func (c *ListenerConfig) WithLogger(logger *zap.Logger) *ListenerConfig {
	c.logger = logger
	return c
}

// WithMetricsProvider sets the MetricsProvider for the WebSocket Listener.
// The MetricsProvider is optional and enables collection of WebSocket server metrics
// such as connection counts, message rates, error rates, and connection durations.
//
// If not provided, no metrics will be collected.
func (c *ListenerConfig) WithMetricsProvider(provider o11y.MetricsProvider) *ListenerConfig {
	c.metricsProvider = provider
	return c
}

// WithQueueSize sets the message queue size for WebSocket connections.
// This controls how many messages can be buffered per connection before
// messages start getting dropped. Larger values handle bursts better but
// use more memory. Must be positive.
//
// Default: 256 messages per connection
func (c *ListenerConfig) WithQueueSize(size int) *ListenerConfig {
	if size > 0 {
		c.queueSize = size
	}
	return c
}

// WithPingInterval sets the interval for sending WebSocket ping frames.
// This helps detect dead connections and maintain connection health.
// Must be positive. Set to 0 to disable ping/pong health monitoring.
//
// Default: 30 seconds
func (c *ListenerConfig) WithPingInterval(interval time.Duration) *ListenerConfig {
	if interval >= 0 {
		c.pingInterval = interval
	}
	return c
}

// WithWriteTimeout sets the timeout for writing messages to WebSocket clients.
// This prevents the server from hanging when clients are slow to receive data.
// Should be short enough to detect slow/dead clients quickly.
//
// Default: 10 seconds
func (c *ListenerConfig) WithWriteTimeout(timeout time.Duration) *ListenerConfig {
	if timeout > 0 {
		c.writeTimeout = timeout
	}
	return c
}

// WithEventAuth sets the event authorization function for client-published events.
// This function determines whether clients are allowed to publish events to the EventBus
// and can modify events before publishing.
//
// The function receives the original message and returns:
//   - (*WireMessage, nil): Use the returned modified message
//   - (nil, nil): Use the original message unchanged
//   - (nil, error): Deny the event with the given error
//
// Predefined options:
//   - DenyAllEvents: Blocks all client events (secure default)
//   - AllowAllEvents: Allows all client events (development/trusted environments)
//   - AllowTopicPrefix("prefix/"): Only allows events to topics with specific prefix
//
// Default: DenyAllEvents (secure)
func (c *ListenerConfig) WithEventAuth(authFunc EventAuthFunc) *ListenerConfig {
	if authFunc != nil {
		c.eventAuth = authFunc
	}
	return c
}

// WithSubscriptionController sets the subscription controller factory for managing
// client subscriptions and unsubscriptions. The factory function receives the
// EventBus and logger and should return a SubscriptionController instance.
//
// The controller can:
//   - Allow/deny subscription requests
//   - Rewrite subscriptions to different topic patterns
//   - Split one subscription into multiple subscriptions
//   - Maintain state for complex subscription policies
//
// Default: NewPassthroughSubscriptionController (allows all subscriptions)
func (c *ListenerConfig) WithSubscriptionController(factory SubscriptionControllerFactory) *ListenerConfig {
	if factory != nil {
		c.subscriptionController = factory
	}
	return c
}

// WithInitialSubscriptions sets the topic patterns that new WebSocket connections
// should be automatically subscribed to when they connect. These subscriptions
// happen automatically without client request and bypass the subscription controller.
//
// This is useful for:
//   - Pushing server-side events to all clients
//   - Providing default subscriptions for convenience
//   - Broadcasting system notifications
//
// Example:
//
//	config.WithInitialSubscriptions("system/alerts", "server/status")
//
// Default: No initial subscriptions
func (c *ListenerConfig) WithInitialSubscriptions(topics ...string) *ListenerConfig {
	if len(topics) > 0 {
		c.initialSubscriptions = make([]string, len(topics))
		copy(c.initialSubscriptions, topics)
	}
	return c
}

// WithMessageTransforms sets the message transformation functions that will be
// applied to outbound messages from the EventBus before sending to WebSocket clients.
// These transforms use the new transform.MessageTransformFunc type and operate on
// EventBusMessage rather than WebSocketMessage.
//
// Transform functions are called in the order they are provided and work with the
// subscriber wrapper pattern for better separation of concerns.
//
// Example:
//
//	transforms := []transform.MessageTransformFunc{
//	    transform.DropByTopicPattern("internal/*"),
//	    transform.AddTimestamp(),
//	    transform.FilterByTopicPrefix("public/"),
//	}
//	config.WithMessageTransforms(transforms...)
//
// Default: No transforms (messages sent as-is)
func (c *ListenerConfig) WithMessageTransforms(transforms ...transform.MessageTransformFunc) *ListenerConfig {
	if len(transforms) > 0 {
		c.messageTransforms = make([]transform.MessageTransformFunc, len(transforms))
		copy(c.messageTransforms, transforms)
	}
	return c
}

// IsValid checks if the configuration has all required parameters set.
// Returns nil if the configuration is valid, or an error describing what's missing.
func (c *ListenerConfig) IsValid() error {
	var missing []string
	if c.eventBus == nil {
		missing = append(missing, "EventBus")
	}
	if c.logger == nil {
		missing = append(missing, "Logger")
	}

	if len(missing) > 0 {
		return fmt.Errorf("invalid listener configuration, missing: %v", missing)
	}

	return nil
}

// Build creates a new WebSocket Listener from the configuration.
// Returns an error if the configuration is invalid (missing EventBus or Logger).
//
// The returned Listener is ready to accept WebSocket connections and integrate
// them with the EventBus for real-time message streaming.
func (c *ListenerConfig) Build() (*Listener, error) {
	if err := c.IsValid(); err != nil {
		return nil, err
	}

	return newListener(c), nil
}
