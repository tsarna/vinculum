package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/tsarna/vinculum-bus"
	"go.uber.org/zap"
)

// Listener handles incoming WebSocket connections and integrates them with the EventBus.
// It manages the lifecycle of WebSocket connections, authentication, and message routing
// between WebSocket clients and the EventBus.
type Listener struct {
	eventBus               bus.EventBus
	logger                 *zap.Logger
	config                 *ListenerConfig
	subscriptionController SubscriptionController
	metrics                *WebSocketMetrics

	// Connection tracking for graceful shutdown
	connections  map[*Connection]struct{}
	connMutex    sync.RWMutex
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// newListener creates a new WebSocket listener from the provided configuration.
// This is a private constructor - use NewListener().Build() instead.
//
// Parameters:
//   - config: The validated ListenerConfig containing EventBus and Logger
//
// Returns a new Listener instance ready to accept WebSocket connections.
func newListener(config *ListenerConfig) *Listener {
	return &Listener{
		eventBus:               config.eventBus,
		logger:                 config.logger,
		config:                 config,
		subscriptionController: config.subscriptionController(config.logger),
		metrics:                NewWebSocketMetrics(config.metricsProvider),
		connections:            make(map[*Connection]struct{}),
		shutdown:               make(chan struct{}),
	}
}

// ServeHTTP handles incoming HTTP requests and upgrades them to WebSocket connections.
// This method can be plugged directly into HTTP routers (e.g., chi, gorilla/mux, net/http).
//
// The method will:
//   - Upgrade the HTTP connection to WebSocket
//   - Handle the WebSocket connection lifecycle
//   - Integrate the connection with the EventBus for pub/sub functionality
//
// Usage example:
//
//	listener := NewListener(eventBus, logger)
//	http.Handle("/ws", listener.ServeWebsocket)
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Accept the WebSocket connection
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		l.logger.Error("Failed to accept WebSocket connection",
			zap.Error(err),
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("user_agent", r.UserAgent()),
		)
		l.metrics.RecordConnectionError(r.Context(), "upgrade_failed")
		return
	}

	l.logger.Debug("WebSocket connection established",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("user_agent", r.UserAgent()),
	)

	// Check if we're shutting down
	select {
	case <-l.shutdown:
		l.logger.Debug("Rejecting new connection due to shutdown")
		conn.Close(websocket.StatusServiceRestart, "Server shutting down")
		return
	default:
	}

	// Create and track the connection
	connection := newConnection(r.Context(), conn, l.config, l.subscriptionController, l.metrics)

	// Add connection to tracking map
	l.connMutex.Lock()
	l.connections[connection] = struct{}{}
	connCount := len(l.connections)
	l.connMutex.Unlock()

	// Record metrics for new connection
	l.metrics.RecordConnectionStart(r.Context())
	l.metrics.RecordConnectionActive(r.Context(), connCount)

	l.logger.Debug("WebSocket connection tracked",
		zap.String("remote_addr", r.RemoteAddr),
		zap.Int("active_connections", connCount),
	)

	// Start the connection handler
	connection.Start()

	// Remove connection from tracking when it's done
	l.connMutex.Lock()
	delete(l.connections, connection)
	connCount = len(l.connections)
	l.connMutex.Unlock()

	// Update active connection count
	l.metrics.RecordConnectionActive(r.Context(), connCount)

	l.logger.Debug("WebSocket connection removed from tracking",
		zap.String("remote_addr", r.RemoteAddr),
		zap.Int("active_connections", connCount),
	)
}

// Shutdown gracefully closes all active WebSocket connections and stops accepting new ones.
// This method should be called when the server is shutting down to ensure proper cleanup.
//
// The shutdown process:
//  1. Stop accepting new connections (returns StatusServiceRestart)
//  2. Close all active connections with StatusGoingAway
//  3. Wait for all connections to finish cleanup
//
// This method blocks until all connections are closed or the context is cancelled.
func (l *Listener) Shutdown(ctx context.Context) error {
	l.shutdownOnce.Do(func() {
		l.logger.Info("Starting graceful WebSocket shutdown")

		// Signal no new connections
		close(l.shutdown)

		// Get snapshot of active connections
		l.connMutex.RLock()
		connections := make([]*Connection, 0, len(l.connections))
		for conn := range l.connections {
			connections = append(connections, conn)
		}
		connCount := len(connections)
		l.connMutex.RUnlock()

		if connCount == 0 {
			l.logger.Info("No active connections to close")
			return
		}

		l.logger.Info("Closing active WebSocket connections",
			zap.Int("connection_count", connCount),
		)

		// Close all connections with proper close code
		for _, conn := range connections {
			go conn.shutdownClose(websocket.StatusGoingAway, "Server shutting down")
		}
	})

	// Wait for all connections to be removed from tracking
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.connMutex.RLock()
			remaining := len(l.connections)
			l.connMutex.RUnlock()

			if remaining > 0 {
				l.logger.Warn("Shutdown timeout reached with active connections",
					zap.Int("remaining_connections", remaining),
				)
			}
			return ctx.Err()

		case <-ticker.C:
			l.connMutex.RLock()
			remaining := len(l.connections)
			l.connMutex.RUnlock()

			if remaining == 0 {
				l.logger.Info("All WebSocket connections closed successfully")
				return nil
			}
		}
	}
}

// ConnectionCount returns the current number of active WebSocket connections.
// This is useful for monitoring and health checks.
func (l *Listener) ConnectionCount() int {
	l.connMutex.RLock()
	defer l.connMutex.RUnlock()
	return len(l.connections)
}
