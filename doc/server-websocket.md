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
    initial_subscriptions = ["events/#"]
}
```

### Attributes

<!-- vinculum:begin block-attrs server websocket level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `bus` | expression (bus-ref) | yes |  | Bus to bridge to connected clients. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `inbound_transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied to frames from clients before publishing. |
| `initial_subscriptions` | list (topic-pattern) |  |  | Topic patterns each new connection is subscribed to on connect. |
| `outbound_transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied to messages going from the bus to clients. |
| `ping_interval` | expression (duration) |  | `30s` | How often to send WebSocket pings, to detect dead connections. |
| `queue_size` | number |  | `256` | Per-connection outbound queue depth. |
| `shutdown_timeout` | expression (duration) |  | `10s` | How long to let in-flight work finish while shutting down. |
| `write_timeout` | expression (duration) |  | `10s` | How long to wait writing to a client before closing the connection. |

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`initial_subscriptions`**

Matching messages are forwarded to the client.

**`ping_interval`**

A ping that goes unanswered within `write_timeout` means the peer is gone, and the connection is dropped along with its queue and subscriptions. Set `0` to disable pings, leaving a dead peer undetected until the OS gives up on the TCP connection.

**`queue_size`**

How far one slow client may fall behind before its messages start being dropped.

**`shutdown_timeout`**

On shutdown, connected clients are closed before the buses and clients they depend on are stopped, and this bounds the wait for them to go away. Applies whether or not the hosting `server "http"` block sets its own — an upgraded WebSocket is invisible to that block's drain. `0` waits indefinitely.

**`write_timeout`**

Bounds each individual write and each ping, so a client that has stopped reading cannot hold the connection's writer indefinitely. Set `0` to wait forever.

<!-- vinculum:end block-attrs server websocket -->

The transform pipelines are described in [transforms.md](transforms.md).

### Detecting dead connections

A client that vanishes without closing — a laptop that sleeps, a network that
drops — leaves the server holding a connection, its outbound queue, and its bus
subscriptions. `ping_interval` is what notices: every interval the server sends a
ping frame, and a pong that does not arrive within `write_timeout` means the peer
is gone, so the connection is dropped immediately rather than through a close
handshake the peer will never complete.

`write_timeout` bounds ordinary writes for the same reason. A client that stops
reading applies backpressure all the way to the connection's writer, and without
a deadline that writer waits forever while the queue behind it fills with
messages that will never be delivered.

Both default to sensible values, so nothing needs configuring for this to work.
Set either to `0` to switch it off.

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
