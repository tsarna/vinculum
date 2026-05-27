# RabbitMQ Client (`client "rabbitmq"`)

Vinculum can publish messages to and consume messages from RabbitMQ (and any
AMQP 0-9-1-compatible broker) using `client "rabbitmq"` blocks. The
implementation uses the official
[amqp091-go](https://github.com/rabbitmq/amqp091-go) library and adds its own
connection- and channel-level recovery on top.

A single `client "rabbitmq"` block manages one AMQP connection and may contain
any number of named `sender` and `receiver` sub-blocks (at least one total).
Each sender and each receiver gets its own AMQP channel on that shared
connection, matching RabbitMQ's recommendation to separate publisher and
consumer channels.

## AMQP vs MQTT/Kafka

AMQP has a richer routing model than MQTT or Kafka. Messages are published to
an **exchange** with a **routing key**, and consumed from a **queue** that is
bound to one or more exchanges. This changes how vinculum topics map to the
wire:

| Concept | MQTT / Kafka | AMQP 0-9-1 |
|---|---|---|
| Outbound destination | broker + topic | **exchange** + routing key |
| Inbound source | subscription / consumer group | **queue** (bound to exchanges) |
| Wildcards | `+` / `#` | `*` (one word) / `#` (zero or more words), dot-delimited |

By default vinculum bridges the two namespaces by translating the slash-delimited
vinculum topic to a dot-delimited AMQP routing key and back (`slash_to_dot` /
`dot_to_slash`). Both directions are fully overridable per pattern.

---

## `client "rabbitmq" "<name>"`

```hcl
client "rabbitmq" "events" {
  # Broker URLs — list multiple for failover (tried in order on connect and
  # on each reconnect). Schemes: amqp:// (plain TCP) or amqps:// (TLS).
  # The URL path is the virtual host: amqp://host:5672/myvhost
  # Omit the path (or use a bare "/") for the default vhost "/".
  # Do not embed credentials in the URL; use the auth block.
  # Default port: 5672 (amqp://) or 5671 (amqps://).
  brokers = ["amqp://rabbitmq.example.com:5672/production"]

  disabled = false   # standard for all client blocks; if true the block is skipped entirely

  # Optional credentials
  auth {
    username = "vinculum"
    password = env.AMQP_PASSWORD
  }

  # Optional TLS. If absent and any broker URL uses amqps://, TLS is enabled
  # with system defaults (as if tls { enabled = true } had been specified).
  tls {
    enabled              = true
    ca_cert              = "/etc/certs/ca.crt"
    cert                 = "/etc/certs/client.crt"  # optional, for mTLS
    key                  = "/etc/certs/client.key"  # optional, for mTLS
    insecure_skip_verify = false                    # default: false
  }

  # AMQP connection settings
  heartbeat          = "10s"   # AMQP heartbeat (default: 10s; "0s" disables — not recommended)
  connection_timeout = "30s"   # TCP dial + AMQP handshake timeout (default: 30s)

  # Optional reconnect backoff
  reconnect {
    initial_delay  = "1s"
    max_delay      = "60s"
    backoff_factor = 2.0
  }

  # Lifecycle hooks (evaluated synchronously; keep them fast)
  on_connect    = send(ctx, bus.main, "rabbitmq/connected",    {client = "events"})
  on_disconnect = send(ctx, bus.main, "rabbitmq/disconnected", {client = "events"})

  # Wire format for payload serialization/deserialization (default: "auto")
  # wire_format = "json"     # auto | json | string | bytes

  # Optional OTel wiring
  # metrics = server.metrics    # optional; auto-wired to the default if omitted
  # tracing = client.tracer     # optional; auto-wired to the default client "otlp"

  # Named sender blocks (zero or more)
  sender "main" { ... }

  # Named receiver blocks (zero or more)
  receiver "main" { ... }
}
```

### Connection attributes

| Attribute | Type | Default | Description |
|---|---|---|---|
| `brokers` | `list(string)` | required | AMQP broker URLs (`amqp://` or `amqps://`). The URL path is the virtual host; omit or use `/` for the default vhost. Multiple URLs are tried in order on connect and reconnect (failover). |
| `disabled` | bool | `false` | Standard `disabled` flag for all client blocks. When `true`, the block is skipped entirely. |
| `heartbeat` | duration | `"10s"` | AMQP heartbeat interval. `"0s"` disables it (not recommended — a silently broken TCP connection may go undetected for a long time). |
| `connection_timeout` | duration | `"30s"` | Timeout for the TCP dial plus the AMQP `OPEN` handshake. |
| `wire_format` | expression | `"auto"` | Payload serialization/deserialization format. One of `"auto"`, `"json"`, `"string"`, `"bytes"`. See [Message serialization](#message-serialization). |
| `metrics` | expression | auto | Optional [`server "metrics"`](server-metrics.md) reference. Auto-wired to the default metrics provider if omitted. |
| `tracing` | expression | auto | Optional [`client "otlp"`](client-otlp.md) reference. Auto-wired to the default OTLP client if omitted. |

### `tls`

The `tls` sub-block configures transport security. See also [TLS configuration](config.md#tls).

| Attribute | Type | Description |
|---|---|---|
| `enabled` | bool | Enable TLS. |
| `ca_cert` | string | Path to a PEM-encoded CA certificate for verifying the broker. If omitted, the system CA pool is used. |
| `cert` | string | Path to a PEM-encoded client certificate (for mutual TLS). |
| `key` | string | Path to the private key corresponding to `cert`. |
| `insecure_skip_verify` | bool | Skip broker certificate verification. Not recommended outside of testing. Default: `false`. |

The block is **optional** and interacts with the broker URL scheme:

- If any `brokers` URL uses `amqps://` and no `tls` block is present, TLS is
  enabled with system trust roots (as if `tls { enabled = true }`).
- If a `tls` block is present and any URL uses `amqps://`, `enabled = true` is
  implicit; setting `enabled = false` together with an `amqps://` URL logs a
  warning and enables TLS anyway (the URL wins).
- An `amqp://` URL combined with `tls { enabled = true }` is rejected as a
  configuration error (mismatched intent).

### `auth`

| Attribute | Type | Description |
|---|---|---|
| `username` | string | AMQP username. |
| `password` | expression | AMQP password. Use `env.VAR_NAME` to avoid hardcoding credentials. |

### `reconnect`

Controls the backoff between reconnection attempts. If omitted, a default
schedule is used (1s initial, doubling, capped at 60s). One "attempt" is a full
walk of the `brokers` list — every broker is tried before the next backoff.

| Attribute | Type | Default | Description |
|---|---|---|---|
| `initial_delay` | duration | `"1s"` | Delay before the first reconnect attempt. |
| `max_delay` | duration | `"60s"` | Maximum delay between reconnect attempts. |
| `backoff_factor` | number | `2.0` | Exponential multiplier applied on each attempt. |

### `on_connect` / `on_disconnect`

Optional HCL expressions evaluated synchronously:

- `on_connect` — fires after the connection is established, all channels are
  open, topology is declared, and consumers are registered. Fires on the
  initial connect and after each successful reconnect.
- `on_disconnect` — fires when the connection drops, **before** any reconnect
  attempt, and once on graceful shutdown.

Standard VCL context (`ctx`, `bus.*`, `send()`, `log_info()`, etc.) is
available. Message variables (`ctx.topic`, `ctx.msg`, `ctx.fields`) are not —
there is no message in flight at lifecycle hook time.

---

## `sender "<name>"`

Each `sender` sub-block creates a named AMQP sender on its own channel. Senders
are addressed in `subscription` blocks via `client.<name>.sender.<name>`
(single sender) or `client.<name>.senders` (fan-out to all senders).

```hcl
sender "main" {
  exchange     = "events"   # required: AMQP exchange to publish to
  confirm_mode = true       # wait for a publisher confirm before returning (default: true)
  mandatory    = false      # return the message if unroutable (default: false)
  persistent   = true       # delivery mode 2 = persisted to disk (default: true)

  # Topic mappings — evaluated in order, first match wins.
  topic "sensor/+deviceId/reading" {
    routing_key = "sensor.${ctx.fields.deviceId}.reading"  # HCL expression
    exchange    = "sensor-events"   # override sender-level exchange (optional)
    persistent  = true              # override sender-level persistent (optional)
  }
  topic "alerts/#" {
    routing_key = "alerts"
  }

  # What to do when no topic block matches:
  #   slash_to_dot — replace "/" with "." (sensor/abc/reading → sensor.abc.reading) — default
  #   verbatim     — use the vinculum topic as the routing key unchanged
  #   error        — return an error from OnEvent
  #   ignore       — silently drop the message
  default_topic_transform = "slash_to_dot"
}
```

### Sender attributes

| Attribute | Type | Default | Description |
|---|---|---|---|
| `exchange` | string | required | The AMQP exchange to publish to. The default exchange (`""`) routes to the queue named by the routing key — valid and useful for simple point-to-point messaging. |
| `confirm_mode` | bool | `true` | When `true`, the channel is put into [publisher confirms](https://www.rabbitmq.com/confirms.html) mode and `OnEvent` blocks until the broker acks the publish. A `Nack` surfaces as an error. When `false`, publishes are fire-and-forget. |
| `mandatory` | bool | `false` | When `true`, an unroutable message (no binding matches the routing key) is returned by the broker. With `confirm_mode = true` this surfaces as an `OnEvent` error (carrying the broker's reply code + text), in addition to being counted on the `rabbitmq.publisher.returned` metric. Requires `confirm_mode = true` to surface as an error — see [Mandatory delivery](#mandatory-delivery) below. |
| `persistent` | bool | `true` | `true` = delivery mode 2 (broker writes the message to disk; survives a restart if the queue is also durable). `false` = delivery mode 1 (in-memory only, higher throughput). |
| `default_topic_transform` | string | `"slash_to_dot"` | Fallback when no `topic` block matches. See below. |

> **Publisher confirms guarantee delivery to the *exchange*, not to a *queue*.**
> Even with `confirm_mode = true`, if no queue is bound for the routing key the
> message is silently discarded unless `mandatory = true`. Use `mandatory = true`
> on critical paths.

### Mandatory delivery

Under publisher confirms the broker `Ack`s an unroutable mandatory message
*after* sending a `Basic.Return` for it (AMQP wire order: Return precedes Ack
for the same publish). The sender correlates the two via a unique
`amqp.Publishing.MessageId` set on every mandatory publish and drains the
broker's return channel in the publish path immediately after the ack arrives.
On a match, `OnEvent` returns an error like:

```
rabbitmq sender: mandatory message returned by broker:
  exchange="alerts" routing_key="urgent" reply_code=312 reply_text="NO_ROUTE"
```

— and the publish path's tracing span records `error.type="returned"`.

`mandatory = true` only surfaces as an `OnEvent` error when `confirm_mode = true`.
With `confirm_mode = false` there is no per-publish synchronization point, so
returns are observable only via the log line and the `rabbitmq.publisher.returned`
metric.

### `topic "<pattern>"`

Each `topic` block maps a vinculum topic pattern (the block label) to AMQP
delivery settings.

| Attribute | Type | Description |
|---|---|---|
| `routing_key` | expression | AMQP routing key for matching messages. If omitted, `default_topic_transform` applies. |
| `exchange` | string | Override the sender-level exchange for this pattern. |
| `persistent` | bool | Override the sender-level `persistent` flag for this pattern. |

**`routing_key` expression context:**

| Variable | Description |
|---|---|
| `ctx.topic` | The vinculum topic string |
| `ctx.msg` | The message payload |
| `ctx.fields` | Named segments captured from the vinculum topic pattern (e.g. `ctx.fields.deviceId`) |

### `default_topic_transform`

| Value | Behavior |
|---|---|
| `slash_to_dot` (default) | Replace `/` with `.` in the vinculum topic to form the routing key. Natural for AMQP topic exchanges. |
| `verbatim` | Use the vinculum topic as the routing key unchanged. |
| `error` | Return an error from `OnEvent`. |
| `ignore` | Silently drop the message. |

### Message serialization

Controlled by the client-level `wire_format` attribute (default `"auto"`):

| Wire format | Serialize | Deserialize |
|---|---|---|
| `auto` | Strings/bytes verbatim; everything else JSON-encoded | Auto-detects JSON; falls back to string |
| `json` | All values JSON-encoded; bytes pass through | Strict JSON; errors on malformed input |
| `string` | Strings, bytes, numbers, bools to string form | Returns string |
| `bytes` | Same as string | Returns bytes |

vinculum `fields` are encoded as entries in the AMQP basic-properties `headers`
table (one entry per key). The AMQP `content-type` property is set from the
wire format used (`application/json`, `text/plain`, or
`application/octet-stream`).

---

## `receiver "<name>"`

Each `receiver` sub-block consumes from a single AMQP queue on its own channel
and dispatches each message to a vinculum bus or subscriber.

```hcl
receiver "main" {
  queue      = "vinculum-events"  # required: AMQP queue to consume from
  subscriber = bus.main           # forward to a bus or subscriber
  # OR
  # action = log_info(ctx, "rabbitmq", {topic = ctx.topic, msg = ctx.msg})

  # Optional transform pipeline and async queue (same semantics as the
  # top-level subscription block — see config.md#subscription).
  # transforms = [ jq(".payload") ]
  # queue_size = 100

  prefetch  = 10     # max unacked messages in flight (default: 10; 0 = unlimited — dangerous)
  exclusive = false  # exclusive consumer (default: false)
  auto_ack  = false  # ack before OnEvent returns (default: false = manual ack)

  # Optional queue declaration.
  # If absent: vinculum does a passive declare to verify the queue exists at startup.
  # If present: vinculum declares the queue (creating it if missing) on every connect.
  declare {
    durable     = true   # queue survives broker restart (default: true)
    auto_delete = false  # delete queue when the last consumer disconnects (default: false)
  }

  # Optional queue-exchange bindings, re-declared on every connect.
  # The block label is the AMQP routing-key pattern for the binding.
  binding "sensor.#" {
    exchange = "sensor-events"
  }

  # Routing key → vinculum topic mappings; evaluated per message, first match wins.
  # The block label is an AMQP routing-key pattern (* = one word, # = zero or more).
  # Named extraction: *deviceId captures one word into ctx.fields.deviceId;
  # #rest captures zero or more words (dot-joined) into ctx.fields.rest.
  subscription "sensor.*deviceId.reading" {
    vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"  # HCL expression
  }

  # Fallback when no subscription matches:
  #   dot_to_slash — replace "." with "/" in the routing key (default)
  #   verbatim     — use the routing key as the vinculum topic unchanged
  #   error        — log an error, nack without requeue
  #   ignore       — ack and silently discard
  default_routing_key_transform = "dot_to_slash"
}
```

### Receiver attributes

| Attribute | Type | Default | Description |
|---|---|---|---|
| `queue` | string | required | The AMQP queue to consume from. With no `declare` block, vinculum does a passive declare at startup and fails fast if the queue is missing. |
| `prefetch` | number | `10` | AMQP `basic.qos` prefetch count — unacked messages the broker may push before waiting for acks. `0` is unlimited and dangerous (the broker pushes the whole queue at once). |
| `exclusive` | bool | `false` | Exclusive consumer — only one consumer may be active on the queue at a time. Prevents horizontal scaling; leave `false` for competing consumers across instances. |
| `auto_ack` | bool | `false` | When `true`, the broker considers a message delivered as soon as it is written to the socket (lossy on crash). Default `false` = manual ack after `subscriber.OnEvent` returns without error. |
| `default_routing_key_transform` | string | `"dot_to_slash"` | Fallback when no `subscription` matches. See below. |

### `subscriber` / `action` / `transforms` / `queue_size`

Exactly one of `subscriber` or `action` must be specified. These four
attributes form the standard delivery pattern used by every block that
dispatches events (see [subscription](config.md#subscription)):

| Attribute | Type | Description |
|---|---|---|
| `subscriber` | expression | Forward each received message to a bus or subscriber (e.g. `bus.main`). Mutually exclusive with `action`. |
| `action` | expression | Evaluate an HCL expression for each message. Mutually exclusive with `subscriber`. |
| `transforms` | expression | Optional transform pipeline applied to each message before delivery. |
| `queue_size` | number | Optional async queue depth. When set, delivery runs in a background queue so a slow handler doesn't block the AMQP delivery loop; trace context flows across the async boundary. |

**Action context variables:**

| Variable | Description |
|---|---|
| `ctx.topic` | Vinculum topic of the received message |
| `ctx.msg` | Deserialized payload (per `wire_format`) |
| `ctx.fields` | `map(string)` from the AMQP headers table merged with extracted routing-key fields. W3C trace headers (`traceparent`, `tracestate`, `baggage`) are stripped. |

### `declare`

Optional. When present, vinculum calls `QueueDeclare` (creating the queue if
missing) on every connect and reconnect. When absent, vinculum does a passive
declare to verify the queue exists and fails fast otherwise.

| Attribute | Type | Default | Description |
|---|---|---|---|
| `durable` | bool | `true` | Queue survives a broker restart. |
| `auto_delete` | bool | `false` | Queue is deleted when the last consumer disconnects. |

Advanced queue arguments (dead-letter exchange, message TTL, max length) are
intentionally not exposed here — manage them with
[RabbitMQ policy](https://www.rabbitmq.com/parameters.html) or your
infrastructure tooling.

### `binding "<routing-key-pattern>"`

Optional. When present, vinculum calls `QueueBind` on every connect and
reconnect, binding the queue to the named exchange with the routing-key pattern
in the block label. Bindings are idempotent.

| Attribute | Type | Description |
|---|---|---|
| `exchange` | string | Required. Exchange to bind this queue to. |

Bindings are about **topology** (what the broker delivers to the queue);
`subscription` blocks are about **dispatch** (how delivered messages map to
vinculum topics).

### `subscription "<routing-key-pattern>"`

Each `subscription` block maps an AMQP routing-key pattern (the block label) to
a vinculum topic. The pattern uses AMQP topic-exchange syntax: `*` matches
exactly one dot-delimited word, `#` matches zero or more words.

| Attribute | Type | Description |
|---|---|---|
| `vinculum_topic` | expression | Vinculum topic to dispatch the message under. Default: `default_routing_key_transform` applied to the routing key. |

**`vinculum_topic` expression context:**

| Variable | Description |
|---|---|
| `ctx.routing_key` | The AMQP routing key the message arrived with |
| `ctx.exchange` | The exchange the message was published to |
| `ctx.topic` | Alias for `ctx.routing_key` (for consistency with other clients) |
| `ctx.msg` | Deserialized message payload |
| `ctx.fields` | `map(string)` from the AMQP headers table merged with extracted routing-key fields |

**Named wildcard field extraction.** Subscription labels may name captures by
appending a field name to either wildcard:

| Pattern segment | Captures into `ctx.fields[name]` |
|---|---|
| `*name` | One routing-key word (dot-delimited segment). |
| `#name` | Zero or more words, joined with `.` (mirrors how the MQTT-side `+name` / `#name` joins with `/`). |

For example, `alerts.#rest` against routing key `alerts.disk.full` captures
`ctx.fields.rest = "disk.full"`. The actual AMQP binding (if declared) uses the
bare wildcards (`*`, `#`); field names are stripped before being sent to the
broker, so extraction happens locally.

### `default_routing_key_transform`

| Value | Behavior |
|---|---|
| `dot_to_slash` (default) | Replace `.` with `/` in the routing key (`sensor.abc.reading` → `sensor/abc/reading`). |
| `verbatim` | Use the routing key unchanged as the vinculum topic. |
| `error` | Log an error and nack the message without requeue. |
| `ignore` | Ack and silently discard the message. |

On a processing error (subscriber error, topic-resolution error, or the
`error` fallback), the message is nacked **without requeue** — a consistently
failing message is not redelivered in a tight loop. Configure a dead-letter
exchange on the queue (via policy) if you need to capture such messages.

---

## Addressing senders in subscriptions

`client.<name>` resolves to a cty object with two attributes:

| Expression | Meaning |
|---|---|
| `client.<name>.senders` | Fan-out: dispatch `OnEvent` to **all** named senders. |
| `client.<name>.sender.<name>` | Route to a single named sender. |

```hcl
# Fan-out to all senders
subscription "all_to_rabbitmq" {
  target     = bus.main
  topics     = ["sensor/#", "alerts/#"]
  subscriber = client.events.senders
}

# Single named sender
subscription "alerts_to_rabbitmq" {
  target     = bus.main
  topics     = ["alerts/#"]
  subscriber = client.events.sender.main
}
```

---

## Connection & recovery

An AMQP 0-9-1 connection is a single TCP connection carrying multiple
lightweight channels. Vinculum uses one TCP connection per `client "rabbitmq"`
block, one channel per sender, and one channel per receiver.

Because amqp091-go does not manage reconnection, vinculum implements it at two
levels:

- **Connection-level recovery** (TCP drop, heartbeat timeout, broker restart):
  fires `on_disconnect`, then walks the `brokers` list with the configured
  backoff until a connection succeeds, re-opens all channels, re-declares
  receiver topology, re-registers consumers, re-enables confirm mode, and fires
  `on_connect`.
- **Channel-level recovery** (an AMQP protocol error closes one channel without
  dropping the connection — e.g. publishing to a non-existent exchange): the
  affected channel is re-opened on the existing connection and its topology /
  consumer re-established, without a full reconnect.

---

## Distributed Tracing

Add a `tracing` attribute to enable W3C TraceContext propagation over the AMQP
basic-properties `headers` table (the AMQP analogue of MQTT 5 user properties
and Kafka headers).

```hcl
client "rabbitmq" "events" {
  tracing = client.tracer   # optional; auto-wired to the default client "otlp"
  ...
}
```

If there is exactly one [`client "otlp"`](client-otlp.md) block (or one marked
`default = true`), the RabbitMQ client auto-wires to it when `tracing =` is
omitted.

**Sender (producer):** the current trace context is injected into the outgoing
`headers` table, and a `SpanKindProducer` span wraps the publish (and the
confirm wait, in confirm mode).

**Receiver (consumer):** for each delivery, the trace context is extracted from
`headers` and a new-root `SpanKindConsumer` span is created and **linked** to
the producer span. This follows the
[OTel messaging semantic conventions](https://opentelemetry.io/docs/specs/semconv/messaging/messaging-spans/)
recommendation for async messaging — the consumer trace is independent but
linked, so the asynchronous boundary is represented correctly. When an async
queue is configured via `queue_size`, the link is preserved across the queue
boundary.

Spans carry `messaging.system`, `messaging.destination.name`,
`messaging.rabbitmq.destination.routing_key`, `messaging.operation.type`,
`messaging.operation.name`, and `vinculum.client.name`. Failed operations set
`error.type` and record the error on the span.

**Baggage** flows automatically via the global propagator and is available to
receiver action expressions (W3C trace headers are stripped from the visible
`fields` map so business metadata stays clean).

See [client "otlp"](client-otlp.md) for tracing configuration and auto-wiring
rules.

---

## Observability

When a [`server "metrics"`](server-metrics.md) block is present (or a
`client "otlp"` exporter is configured), the RabbitMQ client emits metrics
following the
[OTel messaging semantic conventions](https://opentelemetry.io/docs/specs/semconv/messaging/messaging-metrics/),
consistent with the Kafka and MQTT clients.

```hcl
client "rabbitmq" "events" {
  metrics = server.mymetrics   # optional; uses the default provider if omitted
  ...
}
```

All instruments carry `messaging.system="rabbitmq"` and
`vinculum.client.name=<client name>`. Sender instruments add
`messaging.destination.name=<exchange>`; receiver instruments add
`messaging.destination.name=<queue>`. Failures are recorded on the standard
instruments with the `error.type` attribute (rather than via a separate error
counter) — both the message counter and the duration histogram are recorded on
success and on failure.

### Sender

| Instrument | Type | Description |
|---|---|---|
| `messaging.client.sent.messages` | counter | One per publish attempt. `error.type` set on failure. |
| `messaging.client.operation.duration` | histogram (s) | Publish duration; includes the confirm round-trip in confirm mode. Carries `messaging.operation.type="publish"`. |
| `rabbitmq.publisher.returned` | counter | Mandatory messages returned by the broker (no binding matched). |

### Receiver

| Instrument | Type | Description |
|---|---|---|
| `messaging.client.consumed.messages` | counter | One per delivery pulled from the broker. `error.type` set on failure. |
| `messaging.process.duration` | histogram (s) | Time for `subscriber.OnEvent` to return. `error.type` set on failure. |
| `rabbitmq.consumer.nacks` | counter | Messages nacked (without requeue). |

### Client

| Instrument | Type | Description |
|---|---|---|
| `rabbitmq.client.connected` | gauge | `1` when the AMQP connection is up, `0` otherwise. |
| `rabbitmq.client.reconnections` | counter | Connection-level reconnection events. |
| `rabbitmq.client.channel_reopens` | counter | Channel-level recovery events (a channel re-opened without a full reconnect). |

> When exported via [`server "metrics"`](server-metrics.md), OTel instrument
> names are rendered in Prometheus form: dots become underscores, counters gain
> a `_total` suffix, and histograms gain unit suffixes (e.g.
> `messaging_client_sent_messages_total`,
> `messaging_client_operation_duration_seconds`).

The routing key is intentionally **not** added to sender metric attributes:
routing keys can be high-cardinality (computed from message content), which
would cause metric explosion. The routing key is still recorded on trace spans.

---

## Pitfalls

**No queue depth metric.** Obtaining queue depth requires the RabbitMQ
Management HTTP API or a disruptive `QueueInspect`; it is not emitted by
default.

**`prefetch = 0` is unlimited — avoid in production.** Without a prefetch
limit the broker pushes the entire queue at once, which can exhaust memory.
Use a value calibrated to message size and processing latency (10–100).

**Auto-ack loses messages on crash.** With `auto_ack = true` a message is
considered delivered as soon as it is written to the socket. Use the default
manual ack for any non-trivial consumer.

**Topology mismatch is a hard channel error.** Declaring a queue that already
exists with different parameters (e.g. `durable = false` on an existing durable
queue) closes the channel with `406 PRECONDITION_FAILED`, surfacing as a
startup failure. Either match the broker's actual topology or omit the
`declare` block and manage topology externally.

**Exchange type matters.** The `slash_to_dot` / `dot_to_slash` defaults are
designed for AMQP *topic* exchanges. With *direct* exchanges they only work on
exact matches; with *fanout* exchanges the routing key is ignored entirely.

**Virtual host isolation.** RabbitMQ vhosts completely isolate exchanges,
queues, and users. Always specify the vhost explicitly in the broker URL path
in environments that use vhost isolation.

**Multiple broker URLs are for failover, not clustering.** They are tried in
order on connect and reconnect.

---

## Complete example

```hcl
bus "main" {}

client "rabbitmq" "events" {
  brokers = ["amqp://rabbitmq.internal:5672/production"]

  auth {
    username = "vinculum"
    password = env.AMQP_PASSWORD
  }

  reconnect {
    initial_delay  = "1s"
    max_delay      = "60s"
    backoff_factor = 2.0
  }

  on_connect    = send(ctx, bus.main, "rabbitmq/status", {status = "online"})
  on_disconnect = send(ctx, bus.main, "rabbitmq/status", {status = "offline"})

  sender "out" {
    exchange                = "alerts"
    confirm_mode            = true
    persistent              = true
    default_topic_transform = "slash_to_dot"

    topic "alerts/#" {
      routing_key = "alerts"
    }
  }

  receiver "in" {
    queue      = "vinculum-sensors"
    subscriber = bus.main
    prefetch   = 20

    declare {
      durable = true
    }

    binding "sensor.#" {
      exchange = "sensor-events"
    }

    subscription "sensor.*deviceId.reading" {
      vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"
    }
    subscription "sensor.*deviceId.status" {
      vinculum_topic = "sensor/${ctx.fields.deviceId}/status"
    }
  }
}

# Forward vinculum alerts to RabbitMQ
subscription "alerts_to_rabbitmq" {
  target     = bus.main
  topics     = ["alerts/#"]
  subscriber = client.events.sender.out
}
```
