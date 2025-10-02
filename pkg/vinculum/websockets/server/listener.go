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

// Listener implements a simplified WebSocket server similar to the VWS server interface.
// Unlike the full VWS server, this server just deals in raw messages, and has no protocol for subscribe/unsubscribe.
type Listener struct {
	eventBus bus.EventBus
	config   *Config
	metrics  *WebSocketMetrics

	// Connection tracking
	connections  map[*Connection]struct{}
	connMutex    sync.RWMutex
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// newListener creates a new simple WebSocket server from the provided configuration.
func newListener(config *Config) *Listener {
	return &Listener{
		eventBus:    config.eventBus,
		config:      config,
		metrics:     NewWebSocketMetrics(config.metricsProvider),
		connections: make(map[*Connection]struct{}),
		shutdown:    make(chan struct{}),
	}
}

// ServeHTTP handles incoming HTTP requests and upgrades them to WebSocket connections.
func (s *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if we're shutting down
	select {
	case <-s.shutdown:
		http.Error(w, "Server shutting down", http.StatusServiceUnavailable)
		return
	default:
	}

	// Accept the WebSocket connection
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		s.config.logger.Error("Failed to accept WebSocket connection",
			zap.Error(err),
			zap.String("remote_addr", r.RemoteAddr),
		)
		s.metrics.RecordConnectionError(r.Context(), "upgrade_failed")
		return
	}

	s.config.logger.Debug("WebSocket connection established",
		zap.String("remote_addr", r.RemoteAddr),
	)

	// Create and track the connection
	connection := newConnection(r.Context(), conn, s.eventBus, s.config, s.metrics)

	// Add connection to tracking map
	s.connMutex.Lock()
	s.connections[connection] = struct{}{}
	connCount := len(s.connections)
	s.connMutex.Unlock()

	// Record metrics for new connection
	s.metrics.RecordConnectionStart(r.Context())
	s.metrics.RecordConnectionActive(r.Context(), connCount)

	s.config.logger.Debug("WebSocket connection tracked",
		zap.String("remote_addr", r.RemoteAddr),
		zap.Int("active_connections", connCount),
	)

	// Subscribe to initial subscriptions using the connection's async subscriber
	// (which already has transforms and queueing configured)
	for _, topic := range s.config.initialSubscriptions {
		if err := s.eventBus.Subscribe(r.Context(), connection.AsyncSubscriber, topic); err != nil {
			s.config.logger.Warn("Failed to create initial subscription",
				zap.String("topic", topic),
				zap.Error(err),
				zap.String("remote_addr", r.RemoteAddr),
			)
		} else {
			s.config.logger.Debug("Created initial subscription",
				zap.String("topic", topic),
				zap.String("remote_addr", r.RemoteAddr),
			)
		}
	}

	// Start the connection handler
	connection.Start(s)

	// Unsubscribe from EventBus when connection is done
	if err := s.eventBus.UnsubscribeAll(r.Context(), connection.AsyncSubscriber); err != nil {
		s.config.logger.Warn("Failed to unsubscribe connection from EventBus",
			zap.Error(err),
			zap.String("remote_addr", r.RemoteAddr),
		)
	}

	// Remove connection from tracking when it's done
	s.connMutex.Lock()
	delete(s.connections, connection)
	connCount = len(s.connections)
	s.connMutex.Unlock()

	// Update active connection count
	s.metrics.RecordConnectionActive(r.Context(), connCount)

	s.config.logger.Debug("WebSocket connection removed from tracking",
		zap.String("remote_addr", r.RemoteAddr),
		zap.Int("active_connections", connCount),
	)
}

// Shutdown gracefully closes all active WebSocket connections and stops accepting new ones.
// This method should be called when the server is shutting down to ensure proper cleanup.
func (s *Listener) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.config.logger.Info("Starting graceful WebSocket shutdown")

		// Signal no new connections
		close(s.shutdown)

		// Get snapshot of active connections
		s.connMutex.RLock()
		connections := make([]*Connection, 0, len(s.connections))
		for conn := range s.connections {
			connections = append(connections, conn)
		}
		connCount := len(connections)
		s.connMutex.RUnlock()

		if connCount == 0 {
			s.config.logger.Info("No active connections to close")
			return
		}

		s.config.logger.Info("Closing active WebSocket connections",
			zap.Int("connection_count", connCount),
		)

		// Close all connections
		for _, conn := range connections {
			conn.Close()
		}
	})

	// Wait for all connections to be removed from tracking
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.connMutex.RLock()
			remaining := len(s.connections)
			s.connMutex.RUnlock()

			if remaining > 0 {
				s.config.logger.Warn("Shutdown timeout reached with active connections",
					zap.Int("remaining_connections", remaining),
				)
			}
			return ctx.Err()

		case <-ticker.C:
			s.connMutex.RLock()
			remaining := len(s.connections)
			s.connMutex.RUnlock()

			if remaining == 0 {
				s.config.logger.Info("All WebSocket connections closed successfully")
				return nil
			}
		}
	}
}

// ConnectionCount returns the current number of active WebSocket connections.
func (s *Listener) ConnectionCount() int {
	s.connMutex.RLock()
	defer s.connMutex.RUnlock()
	return len(s.connections)
}
