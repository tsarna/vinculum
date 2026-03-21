# Vinculum WebSocket Protocol (`server "vws"` / `client "vws"`)

The Vinculum WebSocket Protocol (VWS) is a simple MQTT-style publish/subscribe
protocol that exposes an event bus over WebSockets. It is implemented by the
[vinculum-vws](https://github.com/tsarna/vinculum-vws) library.

A Vinculum instance can act as a VWS server, a VWS client connecting to another
server, or both simultaneously.

---

## `server "vws"`

Starts a VWS server that exposes a bus to WebSocket clients.

```hcl
server "vws" "name" {
    bus                   = bus.main
    disabled              = false         # optional

    queue_size            = 1000          # optional
    ping_interval         = "30s"         # optional
    write_timeout         = "10s"         # optional

    allow_send            = false         # optional, see below
    initial_subscriptions = ["topic/#"]   # optional

    outbound_transforms   = [...]         # optional
    inbound_transforms    = [...]         # optional
}
```

### Attributes

- `bus` — the event bus to bridge to WebSocket clients (required)
- `disabled` — if true, the block is skipped entirely
- `queue_size` — per-connection outbound message queue depth (default: 1000)
- `ping_interval` — how often to send WebSocket ping frames to detect dead connections (e.g. `"30s"`)
- `write_timeout` — maximum time to wait when writing a message to a client before closing the connection (e.g. `"10s"`)
- `initial_subscriptions` — list of topic patterns that every new client is automatically subscribed to on connect
- `outbound_transforms` — transform pipeline applied to messages going from the bus to clients; see [transforms.md](transforms.md)
- `inbound_transforms` — transform pipeline applied to messages received from clients before publishing to the bus; see [transforms.md](transforms.md)

### `allow_send`

Controls whether clients are permitted to publish messages to the bus. By default
clients can only subscribe and receive. Three forms are supported:

```hcl
allow_send = false              # deny all inbound publishes (default)
allow_send = true               # allow all inbound publishes
allow_send = "sensors/#"        # allow publishes matching this MQTT pattern only
allow_send = ctx.topic != "..." # dynamic expression (ctx.topic and ctx.msg available)
```

When `allow_send` is a dynamic expression, it is evaluated for each inbound message.
Return `true` to allow the message, `false` to silently drop it, or a string to
reject it with an error.

### Example

```hcl
server "vws" "pubsub" {
    bus                   = bus.main
    ping_interval         = "30s"
    write_timeout         = "5s"
    initial_subscriptions = ["events/#"]
    allow_send            = "client/input/#"
}
```

---

## `client "vws"`

Connects to a remote VWS server and bridges it to a local bus.

```hcl
client "vws" "name" {
    url             = "ws://host:port/path"
    disabled        = false                 # optional

    dial_timeout    = "10s"                 # optional
    write_queue_size = 100                  # optional

    headers = {                             # optional
        Authorization = "Bearer ${env.TOKEN}"
    }

    reconnect {                             # optional
        initial_delay  = "1s"
        max_delay      = "60s"
        backoff_factor = 2.0
        max_retries    = -1                 # -1 = unlimited
    }
}
```

### Attributes

- `url` — WebSocket URL of the remote VWS server (required)
- `disabled` — if true, the block is skipped entirely
- `dial_timeout` — maximum time to wait when establishing the connection (e.g. `"10s"`)
- `write_queue_size` — depth of the outbound write queue (default: 100)
- `headers` — map of extra HTTP headers to include in the WebSocket upgrade request (useful for authentication)

### `reconnect` Block

When present, the client will automatically attempt to reconnect after a disconnect.

```hcl
reconnect {
    initial_delay  = "1s"    # optional: wait before first retry
    max_delay      = "60s"   # optional: cap on backoff delay
    backoff_factor = 2.0     # optional: multiply delay by this factor each attempt
    max_retries    = -1      # optional: -1 = retry forever, 0 = no retries
}
```

### Example

```hcl
client "vws" "upstream" {
    url          = "ws://hub.internal:9000/events"
    dial_timeout = "5s"

    headers = {
        Authorization = "Bearer ${env.HUB_TOKEN}"
    }

    reconnect {
        initial_delay  = "2s"
        max_delay      = "30s"
        backoff_factor = 1.5
        max_retries    = -1
    }
}
```

---

## Protocol Notes

The VWS protocol allows clients to:

- **Subscribe** to topic patterns (MQTT-style `+` and `#` wildcards)
- **Unsubscribe** from patterns
- **Receive** messages published on the bus that match their subscriptions
- **Publish** messages to the bus (if `allow_send` permits)

Messages are framed as JSON. The protocol is defined in
[vinculum-vws](https://github.com/tsarna/vinculum-vws).
