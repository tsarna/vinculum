package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/tsarna/vinculum-bus/transform"
	"github.com/tsarna/vinculum/pkg/vinculum/vws"
	"go.uber.org/zap"
)

// Connection represents an individual WebSocket connection that integrates with the EventBus.
// It implements the Subscriber interface to receive events from the EventBus and forwards
// them to the WebSocket client. It also handles incoming messages from the WebSocket client
// and can publish them to the EventBus.
type Connection struct {
	ctx                    context.Context
	conn                   *websocket.Conn
	eventBus               bus.EventBus
	logger                 *zap.Logger
	config                 *ListenerConfig
	subscriptionController SubscriptionController
	metrics                *WebSocketMetrics
	eventMsg               map[string]any
	startTime              time.Time

	// Async subscriber wrapper for handling outbound messages and periodic tasks
	asyncSubscriber *subutils.AsyncQueueingSubscriber

	// Synchronization for cleanup
	cleanupOnce sync.Once
}

// newConnection creates a new WebSocket connection handler that integrates with the EventBus.
//
// Parameters:
//   - ctx: Context for the connection lifecycle
//   - conn: The WebSocket connection to manage
//   - config: The ListenerConfig containing EventBus and Logger
//   - subscriptionController: The SubscriptionController for managing subscriptions
//   - metrics: The WebSocketMetrics for recording connection metrics
//
// Returns a new Connection instance that implements the Subscriber interface.
func newConnection(ctx context.Context, conn *websocket.Conn, config *ListenerConfig, subscriptionController SubscriptionController, metrics *WebSocketMetrics) *Connection {
	// Create the base connection
	baseConnection := &Connection{
		ctx:                    ctx,
		conn:                   conn,
		eventBus:               config.eventBus,
		logger:                 config.logger,
		config:                 config,
		subscriptionController: subscriptionController,
		metrics:                metrics,
		eventMsg:               make(map[string]any), // precreated and reused to avoid allocations
		startTime:              time.Now(),
	}

	// Create the subscriber wrapper chain:
	// 1. TransformingSubscriber (applies new message transforms if any)
	// 2. AsyncQueueingSubscriber (async processing + periodic ticks)

	var wrappedSubscriber bus.Subscriber = baseConnection

	// Add TransformingSubscriber if new transforms are configured
	if len(config.outboundTransforms) > 0 {
		wrappedSubscriber = subutils.NewTransformingSubscriber(wrappedSubscriber, config.outboundTransforms...)
	}

	asyncSubscriber := subutils.NewAsyncQueueingSubscriber(wrappedSubscriber, config.queueSize)

	// Add ticker for ping/pong if configured and start processing
	if config.pingInterval > 0 {
		asyncSubscriber = asyncSubscriber.WithTicker(config.pingInterval).Start()
	} else {
		asyncSubscriber = asyncSubscriber.Start()
	}

	// Store the async subscriber for cleanup
	baseConnection.asyncSubscriber = asyncSubscriber

	return baseConnection
}

// Start begins handling the WebSocket connection.
// This method automatically subscribes to any configured initial subscriptions
// and handles both incoming and outbound messages for the WebSocket client.
//
// This method blocks until the connection is closed, running the message reader
// directly in the calling goroutine for efficiency.
func (c *Connection) Start() {
	c.logger.Debug("Starting WebSocket connection handler")

	// Perform initial subscriptions using the async subscriber wrapper
	// (bypass subscription controller since these are server-initiated)
	for _, topic := range c.config.initialSubscriptions {
		if err := c.eventBus.Subscribe(c.ctx, topic, c.asyncSubscriber); err != nil {
			c.logger.Warn("Failed to create initial subscription",
				zap.String("topic", topic),
				zap.Error(err),
			)
		} else {
			c.logger.Debug("Created initial subscription",
				zap.String("topic", topic),
			)
		}
	}

	// No need for messageSender goroutine - handled by AsyncQueueingSubscriber
	c.logger.Debug("Async subscriber wrapper configured with transforms and ticker")

	// Run the message reader directly in this goroutine (blocks until connection closes)
	c.messageReader()

	c.logger.Debug("WebSocket connection handler stopping")
	c.cleanup()
}

// messageReader handles reading messages from the WebSocket client.
// It parses incoming JSON messages and delegates to handleRequest for processing.
// This method blocks until the connection is closed or an error occurs.
func (c *Connection) messageReader() {
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

		// Read message from WebSocket (no timeout - pings handle connection health)
		_, data, err := c.conn.Read(c.ctx)
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

		// Parse JSON message
		var request vws.WireMessage
		if err := json.Unmarshal(data, &request); err != nil {
			c.logger.Warn("Failed to parse incoming WebSocket message",
				zap.Error(err),
				zap.String("raw_data", string(data)),
				zap.Int("data_length", len(data)),
			)
			// Record message error
			c.metrics.RecordMessageError(c.ctx, "parse_error", "unknown")
			// Send error response if message has an ID
			c.sendErrorResponse("", "Invalid JSON format")
			continue
		}

		// Record received message
		c.metrics.RecordMessageReceived(c.ctx, len(data), request.Kind)

		c.logger.Debug("Received WebSocket message",
			zap.String("kind", request.Kind),
			zap.String("topic", request.Topic),
			zap.Any("id", request.Id),
		)

		handleRequest(c, c.ctx, request)
	}
}

// sendErrorResponse sends a NACK response for malformed requests
func (c *Connection) sendErrorResponse(id any, errorMsg string) {
	response := vws.WireMessage{
		Kind:  vws.MessageKindNack,
		Id:    id,
		Error: errorMsg,
	}

	// Create EventBusMessage for error response and send via PassThrough
	responseMsg := bus.EventBusMessage{
		Ctx:     c.ctx,
		MsgType: bus.MessageTypePassThrough,
		Topic:   "", // Error responses don't have topics
		Payload: response,
	}

	// Send error response through PassThrough method
	sendErr := c.PassThrough(responseMsg)
	if sendErr != nil {
		c.logger.Warn("Failed to send error response",
			zap.Any("request_id", id),
			zap.String("error_message", errorMsg),
			zap.Error(sendErr),
		)
	}
}

// cleanup handles connection cleanup when the connection is closing.
// Uses sync.Once to ensure cleanup only happens once, even if called multiple times.
func (c *Connection) cleanup() {
	c.cleanupOnce.Do(func() {
		c.logger.Debug("Cleaning up WebSocket connection")

		// Record connection duration
		duration := time.Since(c.startTime)
		c.metrics.RecordConnectionEnd(c.ctx, duration)

		// Unsubscribe from EventBus using the async subscriber wrapper
		err := c.eventBus.UnsubscribeAll(c.ctx, c.asyncSubscriber)
		if err != nil {
			c.logger.Warn("Failed to unsubscribe from EventBus during cleanup", zap.Error(err))
		}

		// Close the async subscriber (this will stop the background goroutine and process remaining messages)
		if c.asyncSubscriber != nil {
			err = c.asyncSubscriber.Close()
			if err != nil {
				c.logger.Warn("Failed to close async subscriber during cleanup", zap.Error(err))
			}
		}

		// Close WebSocket connection gracefully (if not already closed)
		if c.conn != nil {
			err = c.conn.Close(websocket.StatusNormalClosure, "Connection closed")
			if err != nil {
				// This is expected if the connection was already closed (e.g., by shutdownClose)
				c.logger.Debug("WebSocket close error (may be expected)", zap.Error(err))
			}
		}

		c.logger.Debug("WebSocket connection cleanup completed")
	})
}

// shutdownClose closes the WebSocket connection with a specific close code and reason.
// This is used during graceful shutdown to properly inform clients of the shutdown reason.
func (c *Connection) shutdownClose(code websocket.StatusCode, reason string) {
	c.logger.Debug("Closing connection for shutdown",
		zap.Int("close_code", int(code)),
		zap.String("reason", reason),
	)

	// Close the WebSocket with the specified code and reason
	// This will cause the messageReader to exit with an error, which will
	// trigger cleanup through the normal Start() -> messageReader() -> cleanup() flow
	err := c.conn.Close(code, reason)
	if err != nil {
		c.logger.Debug("Error closing WebSocket during shutdown", zap.Error(err))
	}
}

func (c *Connection) OnSubscribe(ctx context.Context, topic string) error {
	return nil
}

func (c *Connection) OnUnsubscribe(ctx context.Context, topic string) error {
	return nil
}

// PassThrough handles messages that should be processed directly without going through
// the normal event processing pipeline. This includes:
// - MessageTypeTick: Periodic ping messages for connection health monitoring
// - Other message types: Direct WebSocket message sending (e.g., responses)
func (c *Connection) PassThrough(msg bus.EventBusMessage) error {
	var err error
	switch msg.MsgType {
	case bus.MessageTypeTick:
		err = c.handleTick(msg.Ctx)
	default:
		err = c.sendPacket(msg.Ctx, msg.Payload)
	}

	if err != nil {
		// If we can't send to the client, the connection is likely broken
		// Trigger cleanup to remove this connection and its subscriptions
		c.logger.Info("Connection write failed in PassThrough, cleaning up connection",
			zap.Int("msgType", int(msg.MsgType)),
			zap.Error(err),
		)
		go c.cleanup() // Run cleanup in goroutine to avoid blocking
	}

	return err
}

// handleTick processes a tick message by sending a WebSocket ping for connection health monitoring
func (c *Connection) handleTick(ctx context.Context) error {
	if ctx == nil {
		ctx = c.ctx
	}

	c.logger.Debug("Sending ping to client")

	// Create ping context with write timeout
	pingCtx, cancel := context.WithTimeout(ctx, c.config.writeTimeout)
	defer cancel()

	err := c.conn.Ping(pingCtx)
	if err != nil {
		c.logger.Error("Failed to send ping", zap.Error(err))
		c.metrics.RecordPongTimeout(ctx)
		return err
	}

	c.metrics.RecordPingSent(ctx)
	c.logger.Debug("Ping sent successfully")
	return nil
}

func (c *Connection) sendPacket(ctx context.Context, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		c.logger.Error("Failed to marshal WebSocket message payload",
			zap.Error(err),
			zap.Any("payload", msg),
		)
		c.metrics.RecordMessageError(ctx, "marshal_error", "outbound")
		return err
	}

	// Create write context with timeout
	writeCtx, cancel := context.WithTimeout(ctx, c.config.writeTimeout)
	defer cancel()

	// Send over WebSocket with write deadline
	err = c.conn.Write(writeCtx, websocket.MessageText, data)
	if err != nil {
		c.logger.Error("Failed to send WebSocket message", zap.Error(err))
		// Check if it's a timeout error
		if writeCtx.Err() == context.DeadlineExceeded {
			c.metrics.RecordWriteTimeout(ctx)
		}
		c.metrics.RecordMessageError(ctx, "write_error", "outbound")
		return err
	}

	// Record successful message send
	c.metrics.RecordMessageSent(ctx, len(data), "outbound")
	return nil
}

// OnEvent is called when an event is published to a topic this connection is subscribed to.
// This method forwards the event to the WebSocket client.
// Note: This method is called directly by the AsyncQueueingSubscriber wrapper,
// so it sends messages directly without additional queuing.
func (c *Connection) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	c.logger.Debug("Forwarding event to WebSocket client",
		zap.String("topic", topic),
		zap.Any("message", message),
		zap.Any("fields", fields),
	)

	c.eventMsg["t"] = topic
	c.eventMsg["d"] = message

	err := c.sendPacket(ctx, c.eventMsg)
	if err != nil {
		// If we can't send to the client, the connection is likely broken
		// Trigger cleanup to remove this connection and its subscriptions
		c.logger.Info("Connection write failed, cleaning up connection",
			zap.String("topic", topic),
			zap.Error(err),
		)
		go c.cleanup() // Run cleanup in goroutine to avoid blocking
		return err
	}

	return nil
}

func (c *Connection) respondToRequest(ctx context.Context, request vws.WireMessage, err error) {
	response := vws.WireMessage{
		Kind: vws.MessageKindAck,
		Id:   request.Id,
	}
	if err != nil {
		response.Kind = vws.MessageKindNack
		response.Error = err.Error()
	}

	// Create EventBusMessage for response and send via PassThrough
	responseMsg := bus.EventBusMessage{
		Ctx:     ctx,
		MsgType: bus.MessageTypePassThrough,
		Topic:   "",       // Responses don't have topics
		Payload: response, // vws.WireMessage will be JSON marshaled directly
	}

	// Send response through PassThrough method
	sendErr := c.PassThrough(responseMsg)
	if sendErr != nil {
		c.logger.Warn("Failed to send response message",
			zap.String("response_kind", response.Kind),
			zap.Any("request_id", request.Id),
			zap.Error(sendErr),
		)
	}
}

func handleRequest(c *Connection, ctx context.Context, request vws.WireMessage) {
	var err error

	// Record request metrics
	recordCompletion := c.metrics.RecordRequest(ctx, request.Kind)
	defer func() {
		recordCompletion(err)
	}()

	switch request.Kind {
	case vws.MessageKindEvent:
		if request.Topic == "" {
			err = fmt.Errorf("topic is required")
		} else if request.Data == nil {
			err = fmt.Errorf("data is required")
		} else {
			// Apply event authorization
			modifiedMsg, authErr := c.config.eventAuth(ctx, &request)

			if authErr != nil {
				// Event denied by authorization function
				err = authErr
			} else if modifiedMsg == nil {
				// Event silently dropped by authorization function (nil return with no error)
				// Don't publish anything, but still send ACK response (err remains nil)
			} else {
				// Apply inbound transforms
				transformedMsg, _ := transform.ApplyTransforms(ctx, modifiedMsg.Topic, modifiedMsg.Data, c.config.inboundTransforms)
				if transformedMsg != nil {
					err = c.eventBus.Publish(ctx, transformedMsg.Topic, transformedMsg.Payload)
				}
			}
		}

		if request.Id == nil {
			return
		}
	case vws.MessageKindAck:
		err = nil
	case vws.MessageKindSubscribe:
		// Subscription controller handles validation and EventBus calls
		err = c.subscriptionController.Subscribe(ctx, c.eventBus, c.asyncSubscriber, request.Topic)
	case vws.MessageKindUnsubscribe:
		// Subscription controller handles validation and EventBus calls
		err = c.subscriptionController.Unsubscribe(ctx, c.eventBus, c.asyncSubscriber, request.Topic)
	case vws.MessageKindUnsubscribeAll:
		// Subscription controller handles validation and EventBus calls
		err = c.subscriptionController.UnsubscribeAll(ctx, c.eventBus, c.asyncSubscriber)
	default:
		err = fmt.Errorf("unsupported request type: %s", request.Kind)
	}

	c.respondToRequest(ctx, request, err)
}
