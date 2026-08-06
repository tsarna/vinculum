package websocketserver

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/tsarna/vinculum-bus/transform"
	"go.uber.org/zap"
)

// Connection represents an individual WebSocket connection for the simple server.
// It handles reading messages from the client and sending messages to the client.
type Connection struct {
	ctx         context.Context
	conn        *websocket.Conn
	eventBus    bus.EventBus
	logger      *zap.Logger
	metrics     *WebSocketMetrics
	config      *Config
	sendBinary  bool
	textTopic   string
	binaryTopic string
	startTime   time.Time

	// Async subscriber wrapper for handling outbound messages
	AsyncSubscriber *subutils.AsyncQueueingSubscriber

	// Synchronization for cleanup
	cleanupOnce sync.Once
}

// newConnection creates a new WebSocket connection handler.
func newConnection(ctx context.Context, conn *websocket.Conn, eventBus bus.EventBus, config *Config, metrics *WebSocketMetrics) *Connection {
	// Create the base connection
	baseConnection := &Connection{
		ctx:         ctx,
		conn:        conn,
		eventBus:    eventBus,
		logger:      config.logger,
		metrics:     metrics,
		config:      config,
		sendBinary:  config.sendBinary,
		textTopic:   config.textTopic,
		binaryTopic: config.binaryTopic,
		startTime:   time.Now(),
	}

	// Create the subscriber wrapper chain:
	// 1. TransformingSubscriber (applies message transforms if any)
	// 2. AsyncQueueingSubscriber (async processing with configured queue size)

	var wrappedSubscriber bus.Subscriber = baseConnection

	// Add TransformingSubscriber if transforms are configured
	if len(config.outboundTransforms) > 0 {
		wrappedSubscriber = subutils.NewTransformingSubscriber(wrappedSubscriber, config.outboundTransforms...)
	}

	// Create AsyncQueueingSubscriber with configured queue size and start it.
	// The ticker rides the same queue as outbound messages, so a ping is written
	// by the goroutine that writes everything else — two goroutines writing to
	// one websocket.Conn concurrently is not allowed.
	asyncSubscriber := subutils.NewAsyncQueueingSubscriber(wrappedSubscriber, config.queueSize)
	if config.pingInterval > 0 {
		asyncSubscriber = asyncSubscriber.WithTicker(config.pingInterval)
	}
	asyncSubscriber = asyncSubscriber.Start()

	// Store the async subscriber for cleanup and EventBus subscriptions
	baseConnection.AsyncSubscriber = asyncSubscriber

	return baseConnection
}

// Start begins handling the WebSocket connection.
func (c *Connection) Start(listener *Listener) {
	c.logger.Debug("Starting simple WebSocket connection handler")

	c.logger.Debug("Async subscriber wrapper configured with transforms and queue")

	// Run the message reader directly in this goroutine (blocks until connection closes)
	c.messageReader(listener)

	c.logger.Debug("Simple WebSocket connection handler stopping")
	c.cleanup()
}

// messageReader handles reading messages from the WebSocket client.
func (c *Connection) messageReader(listener *Listener) {
	defer c.logger.Debug("Message reader stopped")

	// Set read limit to prevent large message attacks
	c.conn.SetReadLimit(32768) // 32KB max message size

	for {
		// Check context before each read
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		// Read message from WebSocket
		messageType, data, err := c.conn.Read(c.ctx)
		if err != nil {
			closeStatus := websocket.CloseStatus(err)
			if closeStatus != -1 {
				// WebSocket closed with a close frame
				c.logger.Debug("WebSocket connection closed by client",
					zap.Int("close_status", int(closeStatus)),
				)
			} else if c.ctx.Err() != nil {
				// Context cancelled (server shutdown, etc.) - expected
				c.logger.Debug("WebSocket connection closed due to context cancellation", zap.Error(err))
			} else {
				// Read error (network issue, etc.)
				c.logger.Error("Failed to read WebSocket message", zap.Error(err))
			}
			return
		}

		// Validate message size
		if len(data) == 0 {
			c.logger.Debug("Received empty WebSocket message, ignoring")
			continue
		}

		// Determine topic based on message type and configuration
		var topic string
		switch messageType {
		case websocket.MessageText:
			topic = c.textTopic
		case websocket.MessageBinary:
			topic = c.binaryTopic
		default:
			c.logger.Warn("Received unsupported WebSocket message type",
				zap.Int("message_type", int(messageType)),
			)
			continue
		}

		// If topic is empty, discard the message
		if topic == "" {
			c.logger.Debug("Discarding received WebSocket message (empty topic)",
				zap.String("message_type", messageType.String()),
				zap.Int("data_length", len(data)),
			)
			continue
		}

		c.logger.Debug("Received WebSocket message",
			zap.String("topic", topic),
			zap.String("message_type", messageType.String()),
			zap.Int("data_length", len(data)),
		)

		// Record received message metrics
		c.metrics.RecordMessageReceived(c.ctx, len(data), messageType.String())

		transformedMsg, _ := transform.ApplyTransforms(c.ctx, topic, data, nil, c.config.inboundTransforms)
		// Transform and publish received message to EventBus, unless transformed message is nil
		if transformedMsg != nil {
			if err := c.eventBus.Publish(c.ctx, transformedMsg.Topic, transformedMsg.Payload); err != nil {
				c.logger.Error("Failed to publish received message to EventBus",
					zap.Error(err),
					zap.String("topic", topic),
					zap.Int("data_length", len(data)),
				)
				c.metrics.RecordMessageError(c.ctx, "publish_failed", messageType.String())
			}
		}
	}
}

// cleanup handles connection cleanup when the connection is closing.
func (c *Connection) cleanup() {
	c.cleanupOnce.Do(func() {
		c.logger.Debug("Cleaning up simple WebSocket connection")

		// Record connection duration
		duration := time.Since(c.startTime)
		c.metrics.RecordConnectionEnd(c.ctx, duration)

		// Close the async subscriber
		if c.AsyncSubscriber != nil {
			c.AsyncSubscriber.Close()
		}

		// Close WebSocket connection gracefully (if not already closed)
		if c.conn != nil {
			err := c.conn.Close(websocket.StatusNormalClosure, "Connection closed")
			if err != nil {
				c.logger.Debug("WebSocket close error (may be expected)", zap.Error(err))
			}
		}

		c.logger.Debug("Simple WebSocket connection cleanup completed")
	})
}

// Close closes the connection.
func (c *Connection) Close() {
	// Trigger cleanup which will stop the async subscriber and close the WebSocket
	c.cleanup()
}

// Subscriber interface implementation

func (c *Connection) OnSubscribe(ctx context.Context, topic string) error {
	return nil
}

func (c *Connection) OnUnsubscribe(ctx context.Context, topic string) error {
	return nil
}

// OnEvent is called when an event is published to a topic this connection is subscribed to.
// This method forwards the event to the WebSocket client.
// Message type is determined by fields["format"]:
//   - "text": Send as text frame
//   - "binary": Send as binary frame
//   - missing/other: Use configured default (sendBinary)
func (c *Connection) OnEvent(ctx context.Context, topic string, payload any, fields map[string]string) error {
	// Determine message type based on fields["format"]
	var messageType websocket.MessageType
	if format, exists := fields["format"]; exists {
		switch format {
		case "text":
			messageType = websocket.MessageText
		case "binary":
			messageType = websocket.MessageBinary
		default:
			// Unknown format, use configured default
			if c.sendBinary {
				messageType = websocket.MessageBinary
			} else {
				messageType = websocket.MessageText
			}
		}
	} else {
		// No format specified, use configured default
		if c.sendBinary {
			messageType = websocket.MessageBinary
		} else {
			messageType = websocket.MessageText
		}
	}

	// Convert payload to bytes
	var data []byte
	var err error

	switch v := payload.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		data, err = json.Marshal(v)
		if err != nil {
			c.logger.Error("Failed to marshal payload to JSON",
				zap.Error(err),
				zap.String("topic", topic),
				zap.Any("payload", payload),
			)
			return err
		}
	}

	c.logger.Debug("Forwarding event to WebSocket client",
		zap.String("topic", topic),
		zap.Int("data_length", len(data)),
		zap.String("message_type", messageType.String()),
		zap.Any("fields", fields),
	)

	// Send directly to WebSocket client, bounded by the write timeout so a peer
	// that has stopped reading cannot hold this goroutine indefinitely.
	writeCtx, cancel := c.writeContext()
	defer cancel()

	err = c.conn.Write(writeCtx, messageType, data)
	if err != nil {
		c.logger.Error("Failed to send WebSocket message", zap.Error(err))
		c.metrics.RecordMessageError(c.ctx, "write_failed", messageType.String())
		return err
	}

	c.logger.Debug("Sent WebSocket message",
		zap.String("message_type", messageType.String()),
		zap.Int("data_length", len(data)),
	)
	c.metrics.RecordMessageSent(c.ctx, len(data), messageType.String())

	return nil
}

// writeContext bounds a single write by the configured timeout. A zero or
// negative timeout means wait indefinitely, so the connection context is
// returned unchanged with a no-op cancel.
func (c *Connection) writeContext() (context.Context, context.CancelFunc) {
	if c.config.writeTimeout <= 0 {
		return c.ctx, func() {}
	}
	return context.WithTimeout(c.ctx, c.config.writeTimeout)
}

// PassThrough receives the queue's own messages rather than bus events. The
// only one this server acts on is the tick that drives connection pings; it
// arrives here because the ticker is attached to the outbound queue.
func (c *Connection) PassThrough(msg bus.EventBusMessage) error {
	if msg.MsgType != bus.MessageTypeTick {
		return nil
	}

	writeCtx, cancel := c.writeContext()
	defer cancel()

	if err := c.conn.Ping(writeCtx); err != nil {
		// A ping that does not come back means the peer is gone or wedged.
		// Tearing down here is what makes the ping worth sending: the
		// connection, its queue, and its subscriptions are all released.
		//
		// CloseNow rather than the graceful close cleanup() would do: a close
		// handshake waits for the peer's close frame, and a peer that just
		// failed to answer a ping is exactly the peer that will never send one.
		// Dropping the socket immediately is also what unblocks the reader, so
		// the connection leaves the listener's tracking map.
		c.logger.Debug("Ping failed, closing connection", zap.Error(err))
		c.metrics.RecordMessageError(c.ctx, "ping_failed", "ping")
		if closeErr := c.conn.CloseNow(); closeErr != nil {
			c.logger.Debug("Immediate close after failed ping", zap.Error(closeErr))
		}
		return err
	}

	c.logger.Debug("Ping sent")
	return nil
}
