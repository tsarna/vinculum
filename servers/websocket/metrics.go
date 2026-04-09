package websocketserver

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// WebSocketMetrics defines the metrics collected by the simple WebSocket server.
// This is a simplified version of the VWS server metrics, focusing on the most
// important connection and message metrics.
type WebSocketMetrics struct {
	// Connection metrics
	activeConnections  metric.Float64Gauge    // websocket.active_connections
	totalConnections   metric.Int64Counter    // websocket.connections
	connectionDuration metric.Float64Histogram // websocket.connection.duration
	connectionErrors   metric.Int64Counter    // websocket.connection.errors

	// Message metrics
	messagesReceived metric.Int64Counter    // websocket.received.messages
	messagesSent     metric.Int64Counter    // websocket.sent.messages
	messageErrors    metric.Int64Counter    // websocket.message.errors
	messageSize      metric.Float64Histogram // websocket.message.size
}

// NewWebSocketMetrics creates a new WebSocketMetrics instance using the provided MeterProvider.
// If the provider is nil, returns nil (no metrics will be collected).
func NewWebSocketMetrics(mp metric.MeterProvider) *WebSocketMetrics {
	if mp == nil {
		return nil
	}

	meter := mp.Meter("github.com/tsarna/vinculum/servers/websocket")

	ac, _ := meter.Float64Gauge("websocket.active_connections",
		metric.WithUnit("{connection}"),
		metric.WithDescription("Current number of active WebSocket connections"),
	)
	tc, _ := meter.Int64Counter("websocket.connections",
		metric.WithUnit("{connection}"),
		metric.WithDescription("Total WebSocket connections established"),
	)
	cd, _ := meter.Float64Histogram("websocket.connection.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of WebSocket connections"),
	)
	ce, _ := meter.Int64Counter("websocket.connection.errors",
		metric.WithUnit("{error}"),
		metric.WithDescription("WebSocket connection errors"),
	)

	mr, _ := meter.Int64Counter("websocket.received.messages",
		metric.WithUnit("{message}"),
		metric.WithDescription("Messages received from WebSocket clients"),
	)
	ms, _ := meter.Int64Counter("websocket.sent.messages",
		metric.WithUnit("{message}"),
		metric.WithDescription("Messages sent to WebSocket clients"),
	)
	me, _ := meter.Int64Counter("websocket.message.errors",
		metric.WithUnit("{error}"),
		metric.WithDescription("WebSocket message processing errors"),
	)
	msz, _ := meter.Float64Histogram("websocket.message.size",
		metric.WithUnit("By"),
		metric.WithDescription("Size of WebSocket messages"),
	)

	return &WebSocketMetrics{
		activeConnections:  ac,
		totalConnections:   tc,
		connectionDuration: cd,
		connectionErrors:   ce,
		messagesReceived:   mr,
		messagesSent:       ms,
		messageErrors:      me,
		messageSize:        msz,
	}
}

// Connection lifecycle metrics

// RecordConnectionStart records when a new WebSocket connection is established.
func (m *WebSocketMetrics) RecordConnectionStart(ctx context.Context) {
	if m == nil {
		return
	}
	m.totalConnections.Add(ctx, 1)
}

// RecordConnectionActive updates the active connection count.
func (m *WebSocketMetrics) RecordConnectionActive(ctx context.Context, count int) {
	if m == nil {
		return
	}
	m.activeConnections.Record(ctx, float64(count))
}

// RecordConnectionEnd records when a WebSocket connection ends and its duration.
func (m *WebSocketMetrics) RecordConnectionEnd(ctx context.Context, duration time.Duration) {
	if m == nil {
		return
	}
	m.connectionDuration.Record(ctx, duration.Seconds())
}

// RecordConnectionError records connection-level errors (upgrade failures, etc.).
func (m *WebSocketMetrics) RecordConnectionError(ctx context.Context, errorType string) {
	if m == nil {
		return
	}
	m.connectionErrors.Add(ctx, 1, metric.WithAttributes(attribute.String("error.type", errorType)))
}

// Message metrics

// RecordMessageReceived records when a message is received from a client.
func (m *WebSocketMetrics) RecordMessageReceived(ctx context.Context, sizeBytes int, messageType string) {
	if m == nil {
		return
	}
	m.messagesReceived.Add(ctx, 1, metric.WithAttributes(attribute.String("websocket.message.type", messageType)))
	m.messageSize.Record(ctx, float64(sizeBytes), metric.WithAttributes(attribute.String("websocket.message.direction", "received")))
}

// RecordMessageSent records when a message is sent to a client.
func (m *WebSocketMetrics) RecordMessageSent(ctx context.Context, sizeBytes int, messageType string) {
	if m == nil {
		return
	}
	m.messagesSent.Add(ctx, 1, metric.WithAttributes(attribute.String("websocket.message.type", messageType)))
	m.messageSize.Record(ctx, float64(sizeBytes), metric.WithAttributes(attribute.String("websocket.message.direction", "sent")))
}

// RecordMessageError records message processing errors.
func (m *WebSocketMetrics) RecordMessageError(ctx context.Context, errorType string, messageType string) {
	if m == nil {
		return
	}
	m.messageErrors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("error.type", errorType),
		attribute.String("websocket.message.type", messageType),
	))
}
