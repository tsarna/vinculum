package client

import (
	"context"
	"fmt"
	"time"

	"github.com/tsarna/vinculum-bus"
	"go.uber.org/zap"
)

// AuthorizationProvider is a function that returns an authorization header value.
// It receives a context and should return the authorization value (e.g., "Bearer token123")
// or an error if authorization cannot be obtained.
type AuthorizationProvider func(ctx context.Context) (string, error)

// ClientBuilder provides a fluent interface for building WebSocket clients.
type ClientBuilder struct {
	url              string
	logger           *zap.Logger
	dialTimeout      time.Duration
	subscriber       bus.Subscriber
	writeChannelSize int
	authProvider     AuthorizationProvider // Authorization provider
	headers          map[string][]string   // Custom HTTP headers for WebSocket handshake
	monitor          bus.ClientMonitor     // Optional monitor for client events
}

// NewClient creates a new WebSocket client builder.
func NewClient() *ClientBuilder {
	return &ClientBuilder{
		dialTimeout:      30 * time.Second,
		logger:           zap.NewNop(),
		writeChannelSize: 100, // Default buffer size
	}
}

// WithURL sets the WebSocket URL to connect to.
func (b *ClientBuilder) WithURL(url string) *ClientBuilder {
	b.url = url
	return b
}

// WithLogger sets the logger for the client.
func (b *ClientBuilder) WithLogger(logger *zap.Logger) *ClientBuilder {
	if logger != nil {
		b.logger = logger
	}
	return b
}

// WithDialTimeout sets the timeout for establishing the WebSocket connection.
func (b *ClientBuilder) WithDialTimeout(timeout time.Duration) *ClientBuilder {
	if timeout > 0 {
		b.dialTimeout = timeout
	}
	return b
}

// WithSubscriber sets the subscriber that will receive events from the client.
func (b *ClientBuilder) WithSubscriber(subscriber bus.Subscriber) *ClientBuilder {
	b.subscriber = subscriber
	return b
}

// WithWriteChannelSize sets the buffer size for the internal write channel.
// A larger buffer allows more messages to be queued for writing, which can improve
// performance under high load but uses more memory. Default is 100.
func (b *ClientBuilder) WithWriteChannelSize(size int) *ClientBuilder {
	if size > 0 {
		b.writeChannelSize = size
	}
	return b
}

// WithAuthorization sets a static Authorization header value.
// This will be sent with the WebSocket handshake request.
// Example: WithAuthorization("Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...")
func (b *ClientBuilder) WithAuthorization(authHeader string) *ClientBuilder {
	b.authProvider = func(ctx context.Context) (string, error) {
		return authHeader, nil
	}
	return b
}

// WithAuthorizationProvider sets an authorization provider function.
// This function will be called during connection to obtain the authorization header.
func (b *ClientBuilder) WithAuthorizationProvider(provider AuthorizationProvider) *ClientBuilder {
	b.authProvider = provider
	return b
}

// WithHeaders sets custom HTTP headers for the WebSocket handshake.
// These headers will be sent along with the WebSocket upgrade request.
// Note: This will override any existing headers. Use multiple calls to add headers incrementally.
// Example: WithHeaders(map[string][]string{"X-API-Key": {"key123"}, "User-Agent": {"MyApp/1.0"}})
func (b *ClientBuilder) WithHeaders(headers map[string][]string) *ClientBuilder {
	if b.headers == nil {
		b.headers = make(map[string][]string)
	}
	for key, values := range headers {
		b.headers[key] = values
	}
	return b
}

// WithHeader sets a single HTTP header for the WebSocket handshake.
// This is a convenience method for setting individual headers.
// Example: WithHeader("X-API-Key", "key123")
func (b *ClientBuilder) WithHeader(key, value string) *ClientBuilder {
	if b.headers == nil {
		b.headers = make(map[string][]string)
	}
	b.headers[key] = []string{value}
	return b
}

// WithMonitor sets an optional monitor that will receive client lifecycle events.
// The monitor will be called for connect, disconnect, subscribe, unsubscribe, and unsubscribe all events.
func (b *ClientBuilder) WithMonitor(monitor bus.ClientMonitor) *ClientBuilder {
	b.monitor = monitor
	return b
}

// Build creates and returns a new WebSocket client with the configured options.
func (b *ClientBuilder) Build() (*Client, error) {
	if err := b.IsValid(); err != nil {
		return nil, err
	}

	if b.subscriber == nil {
		b.subscriber = &bus.BaseSubscriber{}
	}

	client := &Client{
		url:              b.url,
		logger:           b.logger,
		dialTimeout:      b.dialTimeout,
		subscriber:       b.subscriber,
		writeChannelSize: b.writeChannelSize,
		authProvider:     b.authProvider,
		headers:          b.headers,
		monitor:          b.monitor,
	}

	return client, nil
}

// IsValid checks that all required configuration is present.
func (b *ClientBuilder) IsValid() error {
	if b.url == "" {
		return fmt.Errorf("URL is required")
	}

	// Logger is optional - we provide a default nop logger
	if b.logger == nil {
		b.logger = zap.NewNop()
	}

	// DialTimeout is optional - we provide a sensible default
	if b.dialTimeout <= 0 {
		b.dialTimeout = 30 * time.Second
	}

	// WriteChannelSize is optional - we provide a sensible default
	if b.writeChannelSize <= 0 {
		b.writeChannelSize = 100
	}

	return nil
}
