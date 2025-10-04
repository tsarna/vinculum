# Vinculum WebSocket Server

The Vinculum WebSocket Server provides real-time bidirectional communication between web clients and the Vinculum EventBus. It enables web applications to subscribe to events, publish events, and receive real-time updates through WebSocket connections.

## Features

- **Real-time Event Streaming** - Subscribe to EventBus topics and receive events in real-time
- **Bidirectional Communication** - Clients can both subscribe to and publish events
- **Flexible Authentication** - Configurable event authorization policies
- **Subscription Control** - Fine-grained control over client subscriptions
- **Message Transformation** - Transform messages before sending to clients
- **Metrics Integration** - Built-in metrics for monitoring connection health and performance
- **Connection Management** - Automatic connection lifecycle management with graceful shutdown
- **Protocol Compliance** - Implements the [Vinculum WebSocket Protocol](../PROTOCOL.md)

## Quick Start

### Basic Setup

```go
package main

import (
    "log"
    "net/http"
    
    "github.com/tsarna/vinculum/pkg/vinculum"
    "github.com/tsarna/vinculum/pkg/vinculum/vws/server"
    "go.uber.org/zap"
)

func main() {
    // Create logger
    logger, _ := zap.NewDevelopment()
    
    // Create EventBus
    eventBus, err := vinculum.NewEventBus().
        WithLogger(logger).
        Build()
    if err != nil {
        log.Fatal(err)
    }
    
    // Start EventBus
    if err := eventBus.Start(); err != nil {
        log.Fatal(err)
    }
    defer eventBus.Stop()
    
    // Create WebSocket listener
    listener, err := server.NewListener().
        WithEventBus(eventBus).
        WithLogger(logger).
        Build()
    if err != nil {
        log.Fatal(err)
    }
    
    // Set up HTTP server
    http.HandleFunc("/ws", listener.ServeWebsocket)
    
    log.Println("WebSocket server starting on :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

### Advanced Configuration

```go
// Create listener with custom configuration
listener, err := server.NewListener().
    WithEventBus(eventBus).
    WithLogger(logger).
    WithMetricsProvider(metricsProvider).
    WithQueueSize(512).                                    // Custom message queue size
    WithPingInterval(30 * time.Second).                    // WebSocket ping interval
    WithWriteTimeout(10 * time.Second).                    // Write timeout
    WithEventAuth(server.AllowTopicPrefix("client/")).     // Event authorization
    WithSubscriptionController(myControllerFactory).       // Custom subscription control
    WithInitialSubscriptions("system/alerts", "status").   // Auto-subscribe new connections
    WithOutboundTransforms(filterSensitive, addTimestamp).  // Outbound message transformations
    WithInboundTransforms(validateInput, addSource).        // Inbound message transformations
    Build()
```

## Configuration Options

### Connection Settings

| Method | Description | Default |
|--------|-------------|---------|
| `WithQueueSize(size)` | Sets the message queue size per connection | 256 |
| `WithPingInterval(duration)` | Sets WebSocket ping interval | 30s |
| `WithWriteTimeout(duration)` | Sets write timeout for messages | 10s |

### Security & Authorization

#### Event Authorization

Control which events clients can publish:

```go
// Allow all events (not recommended for production)
.WithEventAuth(server.AllowAllEvents)

// Deny all events (default - secure)
.WithEventAuth(server.DenyAllEvents)

// Allow events only to specific topic prefixes
.WithEventAuth(server.AllowTopicPrefix("client/"))

// Custom authorization logic
.WithEventAuth(func(ctx context.Context, msg *vws.WireMessage) (*vws.WireMessage, error) {
    // Custom validation logic
    if !isAuthorized(ctx, msg.Topic) {
        return nil, fmt.Errorf("unauthorized topic: %s", msg.Topic)
    }
    return msg, nil
})
```

#### Subscription Control

Control which topics clients can subscribe to:

```go
// Allow all subscriptions (default)
.WithSubscriptionController(server.NewPassthroughSubscriptionController)

// Custom subscription control
.WithSubscriptionController(func(logger *zap.Logger) server.SubscriptionController {
    return &MyCustomController{logger: logger}
})
```

### Message Processing

#### Initial Subscriptions

Automatically subscribe new connections to specific topics:

```go
.WithInitialSubscriptions("system/alerts", "server/status", "user/+/notifications")
```

#### Message Transformations

Transform messages before sending to clients:

```go
// Example: Add timestamp to all messages
func addTimestamp(msg *vws.WireMessage) *vws.WireMessage {
    if msg.Data == nil {
        msg.Data = make(map[string]interface{})
    }
    msg.Data["timestamp"] = time.Now().Unix()
    return msg
}

// Example: Filter sensitive data
func filterSensitive(msg *vws.WireMessage) *vws.WireMessage {
    if data, ok := msg.Data["password"]; ok {
        delete(msg.Data, "password")
    }
    return msg
}

.WithOutboundTransforms(addTimestamp, filterSensitive)
.WithInboundTransforms(validatePayload, addClientInfo)
```

## Monitoring & Metrics

### Built-in Metrics

When a `MetricsProvider` is configured, the server automatically tracks:

- **Connection Metrics**
  - `websocket_connections_total` - Total active connections
  - `websocket_connections_created_total` - Connections created (counter)
  - `websocket_connections_closed_total` - Connections closed (counter)

- **Message Metrics**
  - `websocket_messages_sent_total` - Messages sent to clients (counter)
  - `websocket_messages_received_total` - Messages received from clients (counter)
  - `websocket_message_send_duration` - Time to send messages (histogram)

- **Error Metrics**
  - `websocket_errors_total` - WebSocket errors (counter)

### Example with Metrics

```go
// Create standalone metrics provider
metricsProvider := o11y.NewStandaloneMetricsProvider(nil, &o11y.StandaloneMetricsConfig{
    Interval:     30 * time.Second,
    MetricsTopic: "$metrics",
    ServiceName:  "websocket-server",
})

// Create listener with metrics
listener, err := server.NewListener().
    WithEventBus(eventBus).
    WithLogger(logger).
    WithMetricsProvider(metricsProvider).
    Build()

// Start metrics provider
metricsProvider.SetEventBus(eventBus)
metricsProvider.Start()
```

## Protocol

The WebSocket server implements the [Vinculum WebSocket Protocol](../PROTOCOL.md), a JSON-based protocol with short keys for efficiency.

### Message Types

- **Subscribe** (`"s"`) - Subscribe to topic patterns
- **Unsubscribe** (`"u"`) - Unsubscribe from topics  
- **Unsubscribe All** (`"ua"`) - Unsubscribe from all topics
- **Publish** (`"p"`) - Publish events to topics
- **Event** (`"e"`) - Event notifications from server
- **ACK** (`"a"`) - Acknowledgment responses
- **Error** (`"err"`) - Error responses

### Example Client Messages

```javascript
// Subscribe to user events
ws.send(JSON.stringify({
    "k": "s",           // kind: subscribe
    "t": "user/+/events", // topic pattern
    "i": "req-123"      // request ID
}));

// Publish an event
ws.send(JSON.stringify({
    "k": "p",           // kind: publish
    "t": "client/user123/action",
    "d": {              // data
        "action": "click",
        "element": "button"
    },
    "i": "pub-456"
}));
```

## Error Handling

The server provides detailed error responses for various scenarios:

- **Invalid JSON** - Malformed message format
- **Missing Required Fields** - Missing `k` (kind) or other required fields
- **Authorization Errors** - Event publishing denied by authorization policy
- **Subscription Errors** - Subscription denied by subscription controller
- **Connection Errors** - WebSocket protocol violations

## Best Practices

### Security

1. **Use Event Authorization** - Always configure appropriate event authorization policies
2. **Validate Topic Patterns** - Implement subscription controllers to validate topic access
3. **Rate Limiting** - Consider implementing rate limiting for event publishing
4. **Authentication** - Implement authentication at the HTTP layer before WebSocket upgrade

### Performance

1. **Queue Size** - Adjust queue size based on expected message volume
2. **Timeouts** - Configure appropriate timeouts for your use case
3. **Message Transforms** - Keep transformations lightweight to avoid blocking
4. **Metrics** - Monitor connection and message metrics to identify bottlenecks

### Reliability

1. **Graceful Shutdown** - The server handles graceful shutdown automatically
2. **Connection Monitoring** - Use ping intervals to detect dead connections
3. **Error Handling** - Implement proper error handling in client applications
4. **Reconnection Logic** - Implement client-side reconnection with exponential backoff

## Examples

See the `example_metrics_test.go` file for complete working examples including:

- Basic WebSocket server setup
- Metrics integration
- Custom event authorization
- Message transformations
- Production-ready configuration

## API Reference

### Types

- **`Listener`** - Main WebSocket server that handles connections
- **`ListenerConfig`** - Builder for configuring the listener
- **`EventAuthFunc`** - Function type for event authorization
- **`SubscriptionController`** - Interface for controlling subscriptions
- **`SubscriptionControllerFactory`** - Factory function for creating controllers

### Key Methods

- **`NewListener()`** - Creates a new listener configuration builder
- **`Build()`** - Builds the configured listener
- **`ServeWebsocket(w, r)`** - HTTP handler for WebSocket upgrades
- **`Shutdown()`** - Gracefully shuts down the listener

For detailed API documentation, see the Go package documentation.
