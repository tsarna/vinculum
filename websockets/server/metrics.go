package server

import (
	"context"
	"time"

	"github.com/tsarna/vinculum-bus/o11y"
)

// WebSocketMetrics defines the metrics collected by the simple WebSocket server.
// This is a simplified version of the VWS server metrics, focusing on the most
// important connection and message metrics.
type WebSocketMetrics struct {
	// Connection metrics
	activeConnections  o11y.Gauge     // Current number of active WebSocket connections
	totalConnections   o11y.Counter   // Total number of connections established
	connectionDuration o11y.Histogram // Duration of WebSocket connections
	connectionErrors   o11y.Counter   // Number of connection errors (upgrade failures, etc.)

	// Message metrics
	messagesReceived o11y.Counter   // Total messages received from clients
	messagesSent     o11y.Counter   // Total messages sent to clients
	messageErrors    o11y.Counter   // Number of message processing errors
	messageSize      o11y.Histogram // Size distribution of messages (bytes)
}

// NewWebSocketMetrics creates a new WebSocketMetrics instance using the provided MetricsProvider.
// If the provider is nil, returns nil (no metrics will be collected).
func NewWebSocketMetrics(provider o11y.MetricsProvider) *WebSocketMetrics {
	if provider == nil {
		return nil
	}

	return &WebSocketMetrics{
		// Connection metrics
		activeConnections:  provider.Gauge("simple_websocket_active_connections"),
		totalConnections:   provider.Counter("simple_websocket_connections_total"),
		connectionDuration: provider.Histogram("simple_websocket_connection_duration_seconds"),
		connectionErrors:   provider.Counter("simple_websocket_connection_errors_total"),

		// Message metrics
		messagesReceived: provider.Counter("simple_websocket_messages_received_total"),
		messagesSent:     provider.Counter("simple_websocket_messages_sent_total"),
		messageErrors:    provider.Counter("simple_websocket_message_errors_total"),
		messageSize:      provider.Histogram("simple_websocket_message_size_bytes"),
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
	m.activeConnections.Set(ctx, float64(count))
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
	m.connectionErrors.Add(ctx, 1, o11y.Label{Key: "error_type", Value: errorType})
}

// Message metrics

// RecordMessageReceived records when a message is received from a client.
func (m *WebSocketMetrics) RecordMessageReceived(ctx context.Context, sizeBytes int, messageType string) {
	if m == nil {
		return
	}
	m.messagesReceived.Add(ctx, 1, o11y.Label{Key: "type", Value: messageType})
	m.messageSize.Record(ctx, float64(sizeBytes), o11y.Label{Key: "direction", Value: "received"})
}

// RecordMessageSent records when a message is sent to a client.
func (m *WebSocketMetrics) RecordMessageSent(ctx context.Context, sizeBytes int, messageType string) {
	if m == nil {
		return
	}
	m.messagesSent.Add(ctx, 1, o11y.Label{Key: "type", Value: messageType})
	m.messageSize.Record(ctx, float64(sizeBytes), o11y.Label{Key: "direction", Value: "sent"})
}

// RecordMessageError records message processing errors.
func (m *WebSocketMetrics) RecordMessageError(ctx context.Context, errorType string, messageType string) {
	if m == nil {
		return
	}
	labels := []o11y.Label{
		{Key: "error_type", Value: errorType},
		{Key: "type", Value: messageType},
	}
	m.messageErrors.Add(ctx, 1, labels...)
}
