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
    initial_subscriptions = ["topic/#"]
    allow_send            = "sensors/#"
}
```

### Attributes

<!-- vinculum:begin block-attrs server vws level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `bus` | expression (bus-ref) | yes |  | Bus that connected clients subscribe to and publish into. |
| `allow_send` | expression (predicate-expression) |  | `false` | Whether clients may publish onto the bus. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `inbound_transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied to messages from clients before publishing. |
| `initial_subscriptions` | list (topic-pattern) |  |  | Topic patterns every new client is subscribed to on connect. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `outbound_transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied to messages going from the bus to clients. |
| `ping_interval` | expression (duration) |  |  | How often to send WebSocket pings, to detect dead connections. |
| `queue_size` | number |  | `256` | Per-connection outbound queue depth. |
| `write_timeout` | expression (duration) |  |  | How long to wait writing to a client before closing the connection. |

**`allow_send`**

`true` allows any topic, a string allows topics matching that MQTT pattern, and an expression is evaluated per inbound message with `ctx.topic` and `ctx.msg` in scope — returning false drops the message silently, a string rejects it with that error.

Evaluated against the `message` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`queue_size`**

How far one slow client may fall behind before its messages start being dropped.

<!-- vinculum:end block-attrs server vws -->

The transform pipelines are described in [transforms.md](transforms.md).

### `allow_send`

Clients can only subscribe and receive until this says otherwise. It takes four
forms:

```hcl
allow_send = false              # deny all inbound publishes (default)
allow_send = true               # allow all inbound publishes
allow_send = "sensors/#"        # allow publishes matching this MQTT pattern only
allow_send = ctx.topic != "..." # evaluated per message
```

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
    url = "ws://host:port/path"

    headers = {
        Authorization = "Bearer ${env.TOKEN}"
    }

    reconnect {}
}
```

### Attributes

<!-- vinculum:begin block-attrs client vws level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `url` | string (url) | yes |  | WebSocket URL of the VWS server. |
| `auth` | expression (action-expression) |  |  | Expression producing credentials for the connection. |
| `dial_timeout` | expression (duration) |  | `30s` | Deadline for establishing the connection. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `headers` | map |  |  | Extra headers sent with the WebSocket handshake. |
| `write_queue_size` | number |  | `100` | Outbound message queue depth. |

**`url`**

For example `"wss://events.example.com/ws"`.

**`auth`**

Evaluated when connecting, so a token can be refreshed on each reconnect.

Evaluated against the `connection` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

### Blocks

- `reconnect` (optional) — How to retry a lost connection.

<!-- vinculum:end block-attrs client vws -->

### `reconnect` Block

When present, the client automatically attempts to reconnect after a disconnect.

<!-- vinculum:begin block-attrs client vws reconnect level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `backoff_factor` | number |  | `2.0` | Multiplier applied to the wait after each failed attempt. |
| `initial_delay` | expression (duration) |  | `1s` | Wait before the first retry. |
| `max_delay` | expression (duration) |  | `60s` | Ceiling on the wait between retries. |
| `max_retries` | number |  |  | Give up after this many attempts. |

**`backoff_factor`**

Must be at least 1. Use `1` for a constant delay between retries; a factor below 1 would shorten the wait after every failure rather than lengthen it, reaching zero and retrying continuously, so it is rejected.

**`initial_delay`**

Must be greater than zero: every later wait is this one multiplied by `backoff_factor`, so a zero here stays zero however many times it is multiplied, and the client retries continuously.

**`max_retries`**

Retries forever when omitted, and also when set to zero or a negative number. Counts attempts to recover a *lost* connection; the initial connection is retried regardless. Giving up is quiet and final — the client logs an error and stays down, and the process keeps running.

<!-- vinculum:end block-attrs client vws reconnect -->

There is no way to ask for no retries at all — omit the whole `reconnect` block
for that.

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
