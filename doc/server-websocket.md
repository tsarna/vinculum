# Simple WebSocket Server (`server "websocket"`)

The simple WebSocket server bridges an event bus to WebSocket clients using raw
WebSocket frames. Unlike [`server "vws"`](server-vws.md), it does not implement a
publish/subscribe protocol — the server pushes messages from the bus to all connected
clients, and any frame received from a client is published to the bus on a fixed topic.

Use `server "websocket"` when clients are simple (e.g. browsers receiving a stream of
events) and do not need to send subscribe/unsubscribe commands. Use
[`server "vws"`](server-vws.md) when clients need to control their own subscriptions.

```hcl
server "websocket" "name" {
    bus                   = bus.main
    disabled              = false         # optional

    queue_size            = 256           # optional
    initial_subscriptions = ["events/#"]  # optional

    outbound_transforms   = [...]         # optional
    inbound_transforms    = [...]         # optional
}
```

### Attributes

- `bus` — the event bus to bridge (required)
- `disabled` — if true, the block is skipped entirely
- `queue_size` — per-connection outbound message queue depth (default: 256)
- `initial_subscriptions` — topic patterns that each new connection is automatically
  subscribed to on connect; messages matching these patterns are forwarded to the client
- `outbound_transforms` — transform pipeline applied to messages going from the bus to
  clients; see [transforms.md](transforms.md)
- `inbound_transforms` — transform pipeline applied to frames received from clients
  before publishing to the bus; see [transforms.md](transforms.md)

### Inbound Messages

Frames received from a WebSocket client are published to the bus. Text frames are
published to the topic `"text"` and binary frames to `"binary"`.

### Example

```hcl
server "websocket" "dashboard" {
    bus                   = bus.main
    queue_size            = 512
    initial_subscriptions = ["metrics/#", "alerts/#"]

    outbound_transforms = [
        drop_topic_prefix("internal/"),
    ]
}
```

### Mounting under an HTTP Server

The WebSocket server implements the standard HTTP handler interface, so it can be
mounted under a path on a `server "http"` block:

```hcl
server "websocket" "events" {
    bus                   = bus.main
    initial_subscriptions = ["events/#"]
}

server "http" "main" {
    listen = ":8080"

    handle "/ws" {
        handler = server.events
    }

    files "/" {
        directory = "./web"
    }
}
```
