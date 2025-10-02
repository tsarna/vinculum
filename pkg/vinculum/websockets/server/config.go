package server

import (
	"fmt"

	"github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/o11y"
	"github.com/tsarna/vinculum-bus/transform"
	"go.uber.org/zap"
)

// Config holds the configuration for creating a simple WebSocket server.
// Use NewServer() to create a new configuration and chain methods
// to set the required parameters before calling Build().
type Config struct {
	eventBus             bus.EventBus
	logger               *zap.Logger
	metricsProvider      o11y.MetricsProvider
	queueSize            int
	initialSubscriptions []string
	sendBinary           bool
	textTopic            string
	binaryTopic          string
	messageTransforms    []transform.MessageTransformFunc
}

const (
	// DefaultQueueSize is the default size for the WebSocket message queue.
	DefaultQueueSize = 256
)

// NewServer creates a new Config for building a simple WebSocket server.
// Use the fluent methods to set the required EventBus and Logger, then call Build().
//
// Example:
//
//	server, err := websockets.NewServer().
//	    WithEventBus(eventBus).
//	    WithLogger(logger).
//	    WithQueueSize(512).
//	    WithInitialSubscriptions("system/alerts", "server/status").
//	    WithSendBinary(true).
//	    WithReceivedTextTopic("messages/text").
//	    WithReceivedBinaryTopic("messages/binary").
//	    WithMetricsProvider(metricsProvider).
//	    WithMessageTransforms(transform.DropTopicPrefix("debug/")).
//	    Build()
func NewServer() *Config {
	return &Config{
		queueSize:   DefaultQueueSize,
		textTopic:   "text",
		binaryTopic: "binary",
	}
}

// WithEventBus sets the EventBus for the WebSocket server.
// The EventBus is required for integrating WebSocket connections with the pub/sub system.
func (c *Config) WithEventBus(eventBus bus.EventBus) *Config {
	c.eventBus = eventBus
	return c
}

// WithLogger sets the Logger for the WebSocket server.
// The Logger is required for connection events, errors, and debugging.
func (c *Config) WithLogger(logger *zap.Logger) *Config {
	c.logger = logger
	return c
}

// WithMetricsProvider sets the MetricsProvider for the WebSocket server.
// The MetricsProvider is optional and enables collection of WebSocket server metrics
// such as connection counts, message rates, error rates, and connection durations.
//
// If not provided, no metrics will be collected.
func (c *Config) WithMetricsProvider(provider o11y.MetricsProvider) *Config {
	c.metricsProvider = provider
	return c
}

// WithQueueSize sets the message queue size for WebSocket connections.
// This controls how many messages can be buffered per connection before
// messages start getting dropped. Larger values handle bursts better but
// use more memory. Must be positive.
//
// Default: 256 messages per connection
func (c *Config) WithQueueSize(size int) *Config {
	if size > 0 {
		c.queueSize = size
	}
	return c
}

// WithInitialSubscriptions sets the topic patterns that new WebSocket connections
// should be automatically subscribed to when they connect. These subscriptions
// happen automatically without client request.
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
func (c *Config) WithInitialSubscriptions(topics ...string) *Config {
	if len(topics) > 0 {
		c.initialSubscriptions = make([]string, len(topics))
		copy(c.initialSubscriptions, topics)
	}
	return c
}

// WithSendBinary sets the default message type for WebSocket frames when
// the fields map doesn't contain a "format" key.
//
// Message type is determined by:
//   - fields["format"] == "text": Send as text frame
//   - fields["format"] == "binary": Send as binary frame
//   - fields["format"] missing or other value: Use this default
//
// Default: false (send as text frames)
func (c *Config) WithSendBinary(binary bool) *Config {
	c.sendBinary = binary
	return c
}

// WithReceivedTextTopic sets the topic that received WebSocket text frames will be
// published to on the EventBus. If set to an empty string, text frames
// will be discarded.
//
// Default: "text"
func (c *Config) WithReceivedTextTopic(topic string) *Config {
	c.textTopic = topic
	return c
}

// WithReceivedBinaryTopic sets the topic that received WebSocket binary frames will be
// published to on the EventBus. If set to an empty string, binary frames
// will be discarded.
//
// Default: "binary"
func (c *Config) WithReceivedBinaryTopic(topic string) *Config {
	c.binaryTopic = topic
	return c
}

// WithMessageTransforms sets the message transformation functions that will be
// applied to outbound messages from the EventBus before sending to WebSocket clients.
// These transforms use the transform.MessageTransformFunc type and operate on
// EventBusMessage.
//
// Transform functions are called in the order they are provided and can:
//   - Modify message content
//   - Drop messages (return nil)
//   - Stop the transform pipeline (return false)
//
// Example:
//
//	transforms := []transform.MessageTransformFunc{
//	    transform.DropTopicPrefix("debug/"),
//	    transform.ModifyPayload(func(ctx context.Context, payload any, fields map[string]string) any {
//	        return map[string]any{
//	            "data": payload,
//	            "timestamp": time.Now().Unix(),
//	        }
//	    }),
//	}
//	config.WithMessageTransforms(transforms...)
//
// Default: No transforms (messages sent as-is)
func (c *Config) WithMessageTransforms(transforms ...transform.MessageTransformFunc) *Config {
	if len(transforms) > 0 {
		c.messageTransforms = make([]transform.MessageTransformFunc, len(transforms))
		copy(c.messageTransforms, transforms)
	}
	return c
}

// IsValid checks if the configuration has all required parameters set.
// Returns nil if the configuration is valid, or an error describing what's missing.
func (c *Config) IsValid() error {
	var missing []string
	if c.eventBus == nil {
		missing = append(missing, "EventBus")
	}
	if c.logger == nil {
		missing = append(missing, "Logger")
	}

	if len(missing) > 0 {
		return fmt.Errorf("invalid server configuration, missing: %v", missing)
	}

	return nil
}

// Build creates a new simple WebSocket server from the configuration.
// Returns an error if the configuration is invalid (missing EventBus or Logger).
func (c *Config) Build() (*Listener, error) {
	if err := c.IsValid(); err != nil {
		return nil, err
	}

	return newListener(c), nil
}
