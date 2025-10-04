# Simple WebSocket Server

This package provides a raw WebSocket server implementation that is much simpler than the full VWS server. It's designed for implementing other WebSocket based protocols.

## Features

- **VMS Server compatible interface**
- **Text/Binary Support**: Supports both text and binary frames and a configurable default for sending
- **Configurable Queue Size**: Supports configurable message queue size per connection
- **Logger Support**: Configurable logger for debugging and monitoring
- **Graceful Shutdown**: Proper connection cleanup and graceful shutdown

## Architecture

The simple WebSocket server consists of:

- **Listener**: Manages WebSocket connections and integrates with EventBus
- **Connection**: Handles individual WebSocket connections with async message processing
- **Config**: Builder pattern for server configuration
- **Metrics**: Optional performance monitoring

### Message Processing Architecture

```
EventBus → AsyncQueueingSubscriber → TransformingSubscriber → Connection → WebSocket Client
                    ↓
            [Async Queue Processing]
            - Configurable queue size
            - Background goroutine
            - Transform pipeline
```

Each WebSocket connection uses a layered subscriber architecture:

1. **AsyncQueueingSubscriber**: Provides async message processing with configurable queue size
2. **TransformingSubscriber**: Applies message transforms (if configured)  
3. **Connection**: Handles WebSocket-specific message formatting and sending

This architecture ensures:
- **Non-blocking**: EventBus publishing never blocks due to slow WebSocket clients
- **Efficient**: Messages are processed asynchronously in background goroutines
- **Flexible**: Transform pipeline allows message modification, filtering, and enrichment
- **Scalable**: Each connection has its own queue and processing goroutine

## Usage

### Basic Setup

```go
package main

import (
    "context"
    "log"
    "net/http"
    
    "github.com/tsarna/vinculum-bus"
    "github.com/tsarna/vinculum/pkg/vinculum/websockets/server"
    "go.uber.org/zap"
)

func main() {
    logger, _ := zap.NewProduction()
    
    // Create EventBus
    eventBus, err := bus.NewEventBus().
        WithLogger(logger).
        Build()
    if err != nil {
        log.Fatal(err)
    }
    eventBus.Start()
    defer eventBus.Stop()
    
    // Create the simple WebSocket listener
    wsListener, err := server.NewServer().
        WithEventBus(eventBus).
        WithLogger(logger).
        WithQueueSize(512).
        WithInitialSubscriptions("system/alerts", "server/status").
        WithSendBinary(false).
        WithReceivedTextTopic("messages/text").
        WithReceivedBinaryTopic("messages/binary").
        WithMetricsProvider(metricsProvider).
        WithOutboundTransforms(transform.DropTopicPrefix("debug/")).
        WithInboundTransforms(transform.AddField("source", "websocket")).
        Build()
    if err != nil {
        log.Fatal(err)
    }
    
    // Set up HTTP handler
    http.Handle("/ws", wsListener)
    
    // Start HTTP server
    log.Println("Starting server on :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

### Publishing Messages

Messages are published through the EventBus, and all connected WebSocket clients will receive them. The message type (text/binary frame) is determined by the `fields` map:

```go
// Send as text frame using fields["format"] = "text"
err := eventBus.PublishWithFields(ctx, "notifications", "Hello, World!", map[string]string{
    "format": "text",
})

// Send as binary frame using fields["format"] = "binary"  
err := eventBus.PublishWithFields(ctx, "data", []byte{0x01, 0x02, 0x03}, map[string]string{
    "format": "binary",
})

// Send using configured default (no format specified)
err := eventBus.Publish(ctx, "system/alerts", map[string]interface{}{
    "level": "warning",
    "message": "System maintenance in 5 minutes",
})

// Send with unknown format - uses configured default
err := eventBus.PublishWithFields(ctx, "server/status", statusData, map[string]string{
    "format": "json", // Unknown format, uses default
})
```

### Receiving Messages

Messages received from WebSocket clients are automatically published to the EventBus:

```go
// Configure topics for received messages
server := server.NewServer().
    WithReceivedTextTopic("client/text").      // Text frames → "client/text" topic
    WithReceivedBinaryTopic("client/binary").  // Binary frames → "client/binary" topic
    Build()

// To disable receiving certain frame types, set topic to empty string
server := server.NewServer().
    WithReceivedTextTopic("client/messages").  // Text frames → "client/messages"
    WithReceivedBinaryTopic("").               // Binary frames discarded
    Build()

// Subscribe to received messages
eventBus.Subscribe(ctx, "client/text", mySubscriber)
eventBus.Subscribe(ctx, "client/binary", mySubscriber)
```

### Message Handling

- **EventBus Integration**: Each WebSocket connection is automatically subscribed to topics configured via `WithInitialSubscriptions()`
- **Frame Type Determination**: Message type is determined by `fields["format"]`:
  - `fields["format"] == "text"`: Sent as WebSocket text frame
  - `fields["format"] == "binary"`: Sent as WebSocket binary frame
  - `fields["format"]` missing or other value: Uses configured default (`WithSendBinary`)

- **Message Types**:
  - `[]byte`: Sent as-is
  - `string`: Converted to `[]byte` and sent
  - Other types: Marshalled to JSON and sent

- **Receiving**: Messages from clients are published to the EventBus
  - Text frames published to configured text topic (default: "text")
  - Binary frames published to configured binary topic (default: "binary")
  - If topic is empty, messages are discarded
  - Raw frame bytes are published as-is (no JSON parsing)

### Configuration Options

- **EventBus**: Required for integration with the pub/sub system
- **Logger**: Required for debugging and error reporting
- **Queue Size**: Buffer size for outbound messages per connection (default: 256)
- **Initial Subscriptions**: Topics to automatically subscribe connections to
- **Send Binary**: Default frame type when `fields["format"]` is missing (default: false = text frames)
- **Received Text Topic**: Topic for received text frames (default: "text", empty = discard)
- **Received Binary Topic**: Topic for received binary frames (default: "binary", empty = discard)
- **Metrics Provider**: Optional metrics collection (default: none)
- **Message Transforms**: Optional message transformation functions (default: none)

### Message Transforms

The server supports message transformation functions that are applied to outbound messages from the EventBus before sending to WebSocket clients. This uses the same transform system as the full VWS server.

#### Transform Functions

Transform functions can:
- **Modify message content** - Change the payload or topic
- **Drop messages** - Return nil to prevent sending
- **Stop processing** - Return false to halt the transform pipeline

#### Built-in Transforms

```go
import "github.com/tsarna/vinculum/pkg/vinculum/transform"

// Drop messages from debug topics
transform.DropTopicPrefix("debug/")

// Drop messages matching MQTT patterns
transform.DropTopicPattern("internal/+/secrets")

// Add metadata to all messages
transform.ModifyPayload(func(ctx context.Context, payload any, fields map[string]string) any {
    return map[string]any{
        "data": payload,
        "timestamp": time.Now().Unix(),
        "server": "simple-websocket",
    }
})

// Transform messages matching specific patterns
transform.TransformOnPattern("sensor/+device/data", func(ctx context.Context, payload any, fields map[string]string) any {
    deviceName := fields["device"]
    return map[string]any{
        "device": deviceName,
        "reading": payload,
        "processed_at": time.Now().Unix(),
    }
})
```

#### Example Usage

```go
// Configure multiple transforms
transforms := []transform.MessageTransformFunc{
    transform.DropTopicPrefix("debug/"),
    transform.DropTopicPattern("internal/+/secrets"),
    transform.ModifyPayload(func(ctx context.Context, payload any, fields map[string]string) any {
        return map[string]any{
            "original": payload,
            "timestamp": time.Now().Unix(),
        }
    }),
}

listener := server.NewServer().
    WithEventBus(eventBus).
    WithLogger(logger).
    WithOutboundTransforms(transforms...).
    WithInboundTransforms(inboundTransforms...).
    Build()
```

### Metrics Support

When a `MetricsProvider` is configured, the server automatically tracks:

#### Connection Metrics
- `simple_websocket_active_connections` - Current number of active connections (gauge)
- `simple_websocket_connections_total` - Total connections established (counter)
- `simple_websocket_connection_duration_seconds` - Connection duration (histogram)
- `simple_websocket_connection_errors_total` - Connection errors by type (counter)

#### Message Metrics
- `simple_websocket_messages_received_total` - Messages received from clients (counter)
- `simple_websocket_messages_sent_total` - Messages sent to clients (counter)
- `simple_websocket_message_errors_total` - Message processing errors (counter)
- `simple_websocket_message_size_bytes` - Message size distribution (histogram)

#### Example with Metrics

```go
// Create metrics provider
metricsProvider := o11y.NewStandaloneMetricsProvider(nil, &o11y.StandaloneMetricsConfig{
    Interval:     30 * time.Second,
    MetricsTopic: "$metrics",
    ServiceName:  "simple-websocket-server",
})

// Create server with metrics
server := server.NewServer().
    WithEventBus(eventBus).
    WithLogger(logger).
    WithMetricsProvider(metricsProvider).
    Build()
```

### Graceful Shutdown

```go
// Graceful shutdown with timeout
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

err := wsServer.Shutdown(ctx)
if err != nil {
    log.Printf("Shutdown error: %v", err)
}
```

## Limitations

This is a simplified server that intentionally omits many features of the full VWS server:

- **No Subscriptions**: Subscribe/Unsubscribe methods are no-ops
- **No Authentication**: No event authorization or access control
- **No Metrics**: No built-in metrics collection
- **No Message Transforms**: No message transformation pipeline
- **No Subscription Controllers**: No subscription management
- **No Initial Subscriptions**: No automatic subscriptions on connect
- **Limited Topics**: Only "text" and "binary" topics supported

## When to Use

Use this simple server when you need:
- Basic WebSocket broadcasting to all clients
- Simple text/binary message sending
- Minimal complexity and dependencies
- No need for subscription management or advanced features

For more advanced use cases, consider using the full VWS server instead.
