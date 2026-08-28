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
  # wire_format = "json"     # auto | auto_bytes | json | string | bytes

  # Optional OTel wiring
  # metrics = server.metrics    # optional; auto-wired to the default if omitted
  # tracing = client.tracer     # optional; auto-wired to the default client "otlp"

  # Named sender blocks (zero or more)
  sender "main" { ... }

  # Named receiver blocks (zero or more)
  receiver "main" { ... }
}
```

### Attributes

The URL path of a broker is the virtual host; omit it or use `/` for the default
vhost. See [Message serialization](#message-serialization) for what `wire_format`
does to a payload.

<!-- vinculum:begin block-attrs client rabbitmq level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `brokers` | list (url) | yes |  | Broker URLs to connect to. |
| `connection_timeout` | expression (duration) |  | `30s` | Deadline for establishing a connection. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `heartbeat` | expression (duration) |  | `10s` | Interval between AMQP heartbeats. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `on_connect` | expression (action-expression) |  |  | Evaluated after the connection is established and ready. |
| `on_disconnect` | expression (action-expression) |  |  | Evaluated when the connection is lost or closed. |
| `readiness` | bool |  | `true` | Whether this component gates the process's readiness. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

**`brokers`**

For example `["amqp://guest:guest@rabbit:5672/"]`. Several are tried in order.

**`connection_timeout`**

Covers the TCP dial and the AMQP handshake together.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`heartbeat`**

Zero disables them, which leaves a silently broken TCP connection undetected until something tries to use it.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`on_connect`**

Runs synchronously: no messages are produced or consumed until it returns. There is no message in flight, so no message variables are in scope.

Evaluated against the `connection` context.

**`on_disconnect`**

Always runs before any reconnection attempt, and on a graceful shutdown before the connection is torn down. Every `on_connect` after the first is preceded by one.

Evaluated against the `connection` context.

**`readiness`**

This block reports whether it is currently serving, and by default that gates the process: while it is down, `/readyz` fails and traffic should go elsewhere. Set this false for an integration the service can do without, so losing it does not take the whole process out of rotation.

The attribute exists only on the types that have readiness to report; see [health](health.md).

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

#### Blocks

- `auth` (optional) — Credentials presented to the broker.
- `receiver "<name>"` (0..n) — Consumes a queue and delivers what arrives.
- `reconnect` (optional) — How to retry a lost connection.
- `sender "<name>"` (0..n) — Publishes bus messages to an exchange.
- `tls` (optional) — TLS settings for this connection.

<!-- vinculum:end block-attrs client rabbitmq -->

### `tls`

The `tls` sub-block configures transport security. See also [TLS configuration](config.md#tls).

<!-- vinculum:begin block-attrs client rabbitmq tls level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `ca_cert` | string |  | PEM file of CA certificates to trust. |
| `cert` | string |  | PEM file holding this side's certificate. |
| `enabled` | bool |  | Turn TLS on. |
| `insecure_skip_verify` | bool |  | Accept any server certificate without verifying it. |
| `key` | string |  | PEM file holding the private key for `cert`. |
| `require_client_cert` | bool |  | Require clients to present a certificate. |
| `self_signed` | bool |  | Generate a self-signed certificate at startup. |

- cert and key must be specified together.
- Specify at most one of self_signed or cert.

**`ca_cert`**

On a client, verifies the server's certificate. On a server, verifies presented client certificates.

**`enabled`**

Nothing else in the block takes effect while this is false.

**`insecure_skip_verify`**

Client-side only, and unsafe outside development.

**`require_client_cert`**

Server-side only; verified against `ca_cert`.

**`self_signed`**

Server-side only, for development. Mutually exclusive with `cert`/`key`.

<!-- vinculum:end block-attrs client rabbitmq tls -->

The block is **optional** and interacts with the broker URL scheme:

- If any `brokers` URL uses `amqps://` and no `tls` block is present, TLS is
  enabled with system trust roots (as if `tls { enabled = true }`).
- If a `tls` block is present and any URL uses `amqps://`, `enabled = true` is
  implicit; setting `enabled = false` together with an `amqps://` URL logs a
  warning and enables TLS anyway (the URL wins).
- An `amqp://` URL combined with `tls { enabled = true }` is rejected as a
  configuration error (mismatched intent).

### `auth`

<!-- vinculum:begin block-attrs client rabbitmq auth level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `password` | expression |  | Password to authenticate with. |
| `username` | string |  | Username to authenticate with. |

**`password`**

Supply it from the environment rather than a literal.

<!-- vinculum:end block-attrs client rabbitmq auth -->

### `reconnect`

One "attempt" is a full walk of the `brokers` list — every broker is tried before
the next backoff.

<!-- vinculum:begin block-attrs client rabbitmq reconnect level=4 -->

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

Retries forever when omitted, and also when set to zero or a negative number. Counts attempts to recover a *lost* connection; the initial connection is retried regardless, since a dependency that has not finished starting is the ordinary case at boot and giving up on it would be the wrong default. Giving up is quiet and final — the client logs an error and stays down, the process keeps running, and readiness reports the client as not ready for as long as that lasts.

<!-- vinculum:end block-attrs client rabbitmq reconnect -->

### `on_connect` / `on_disconnect`

Optional HCL expressions evaluated synchronously:

- `on_connect` — fires after the connection is established, all channels are
  open, topology is declared, and consumers are registered. Fires on the
  initial connect and after each successful reconnect.
- `on_disconnect` — fires when the connection drops, **before** any reconnect
  attempt, and once on graceful shutdown.

Standard VCL context (`ctx`, `bus.*`, `send()`, `log::info()`, etc.) is
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

`confirm_mode` uses AMQP [publisher confirms](https://www.rabbitmq.com/confirms.html).
A returned message is also counted on the `rabbitmq.publisher.returned` metric —
see [Mandatory delivery](#mandatory-delivery) below.

<!-- vinculum:begin block-attrs client rabbitmq sender level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `exchange` | string | yes |  | Exchange to publish to. |
| `confirm_mode` | bool |  | `true` | Wait for the broker to confirm each publish. |
| `default_topic_transform` | string |  | `slash_to_dot` | How to derive a routing key from a bus topic with no `topic` block. |
| `mandatory` | bool |  | `false` | Return messages the exchange cannot route to any queue. |
| `persistent` | bool |  | `true` | Mark messages persistent so they survive a broker restart. |

**`exchange`**

The default exchange (`""`) routes to the queue named by the routing key, which is enough for point-to-point messaging.

**`confirm_mode`**

A publish blocks until acknowledged, and a nack surfaces as an error. Confirms guarantee delivery to the *exchange*, not to a queue: with no binding for the routing key the message is still discarded unless `mandatory` is set.

**`default_topic_transform`**

`slash_to_dot` rewrites `a/b/c` as `a.b.c`, matching AMQP's convention; `verbatim` publishes the bus topic unchanged; `error` fails the publish; `ignore` drops the message.

One of: `slash_to_dot`, `verbatim`, `error`, `ignore`.

**`mandatory`**

Without this, an unroutable message is silently discarded. Requires `confirm_mode` for the return to surface as an error rather than only as a metric.

**`persistent`**

Delivery mode 2, which the broker writes to disk — and which survives a restart only if the queue is durable too. Turning it off trades that for throughput.

#### Blocks

- `topic "<pattern>"` (0..n) — Maps bus topics matching a pattern to a routing key.

<!-- vinculum:end block-attrs client rabbitmq sender -->

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

<!-- vinculum:begin block-attrs client rabbitmq sender topic level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `exchange` | string |  | Exchange for this mapping. |
| `persistent` | bool |  | Persistence for this mapping. |
| `routing_key` | expression |  | Routing key to publish with. |

**`exchange`**

Overrides the sender's exchange.

**`persistent`**

Overrides the sender's default.

**`routing_key`**

`default_topic_transform` applies when omitted. Evaluated per message, so it can interpolate the fields the pattern captured.

Evaluated against the `message` context.

<!-- vinculum:end block-attrs client rabbitmq sender topic -->

**`routing_key` expression context:**

<!-- vinculum:begin block-ctx client rabbitmq sender topic routing_key level=4 -->

Fields readable as `ctx.<name>` (shape `message`):

| Field | Type | Description |
|---|---|:---|
| `ctx.topic` | string | Topic the message was delivered on. |
| `ctx.msg` | dynamic | The message payload. |
| `ctx.fields` | object | String metadata attached to the message. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.msg`**

Already decoded by the client's `wire_format`, so its type follows the data rather than the transport.

**`ctx.fields`**

Always present; an empty object when the message carries no metadata.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx client rabbitmq sender topic routing_key -->

Named segments captured from the vinculum topic pattern arrive in `ctx.fields` —
`+deviceId` in the label becomes `ctx.fields.deviceId`.

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
| `auto_bytes` | Same as `auto` | Auto-detects JSON; falls back to [`bytes`](functions.md) |
| `json` | All values JSON-encoded; bytes pass through | Strict JSON; errors on malformed input |
| `string` | Strings, bytes, numbers, bools to string form | Returns string |
| `bytes` | Same as string | Returns [`bytes`](functions.md) |

vinculum `fields` are encoded as entries in the AMQP basic-properties `headers`
table (one entry per key). The AMQP `content-type` property is set from the
wire format used (`application/json`, `text/plain`, or
`application/octet-stream`).

#### Decode failures

The configured `wire_format` is a **contract**. When an inbound body fails to
deserialize, the message is nacked without requeue — it is *not* delivered to
the subscriber as raw bytes.

For rabbitmq this is safe: the message is dropped, or routed to a dead-letter
exchange if one is bound to the queue.

Use `wire_format = "auto_bytes"` if you want best-effort decoding instead: it
decodes JSON like `auto` and yields a [`bytes`](functions.md) value for anything
it can't parse. `auto` behaves the same but yields a string — pick whichever
type your handler wants. Neither ever fails to decode.

> **Changed in 0.44.0.** Earlier releases logged a warning and delivered the
> raw bytes. See [deprecations](deprecations.md#tolerant-wire-format-decoding).

---

## `receiver "<name>"`

Each `receiver` sub-block consumes from a single AMQP queue on its own channel
and dispatches each message to a vinculum bus or subscriber.

```hcl
receiver "main" {
  queue      = "vinculum-events"  # required: AMQP queue to consume from
  subscriber = bus.main           # forward to a bus or subscriber
  # OR
  # action = log::info(ctx, "rabbitmq", {topic = ctx.topic, msg = ctx.msg})

  # Optional transform pipeline and async queue (same semantics as the
  # top-level subscription block — see config.md#subscription).
  # transforms = [ jq(".payload") ]
  # queue_size = 100

  prefetch  = 10     # max unacked messages in flight (default: 10; 0 = unlimited — dangerous)
  exclusive = false  # exclusive consumer (default: false)
  auto_ack  = false  # ack before OnEvent returns (default: false = manual ack)

  # Optional; inbound baggage is stripped by default. See doc/baggage.md.
  baggage {
    allow = ["tenant_id"]
  }

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

The `baggage` block is a [baggage](baggage.md) trust filter. Inbound baggage is
**stripped by default** before it reaches the action; opt in with
`passthrough`/`allow`/`deny`, per receiver. See
[Server-side trust filtering](baggage.md#server-side-trust-filtering).

<!-- vinculum:begin block-attrs client rabbitmq receiver level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `queue` | string | yes |  | Queue to consume from. |
| `action` | expression (action-expression) |  |  | Expression evaluated once per message. |
| `auto_ack` | bool |  | `false` | Acknowledge on delivery rather than after handling. |
| `default_routing_key_transform` | string |  | `dot_to_slash` | How to derive a bus topic from a routing key with no `subscription` block. |
| `exclusive` | bool |  | `false` | Claim the queue exclusively for this connection. |
| `on_decode_error` | expression (action-expression) |  |  | Evaluated when an inbound message cannot be decoded. |
| `prefetch` | number |  | `10` | Unacknowledged messages the broker may have in flight. |
| `queue_size` | number |  |  | Depth of an async queue wrapping the subscriber. |
| `subscriber` | expression (subscriber-ref) |  |  | Subscriber to forward messages to, instead of evaluating an action. |
| `transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied before the action or subscriber. |

- Specify at most one of action or subscriber.
- Specify either an action to evaluate or a subscriber to forward to.

**`queue`**

With no `declare` block the queue is declared passively when the client connects, so a missing queue is reported at once rather than on the first message. The connection is retried, so a queue that has not been provisioned yet leaves the client not ready with the broker's own error, and it recovers when the queue appears.

**`action`**

`ctx.topic` is the message topic and `ctx.msg` the payload; a protocol that extracts metadata also provides `ctx.fields`.

Evaluated against the `message` context.

**`auto_ack`**

Faster, but a message is lost if handling fails or the process dies holding it.

**`default_routing_key_transform`**

`dot_to_slash` rewrites `a.b.c` as `a/b/c`, matching the bus's convention; `verbatim` uses the routing key unchanged; `error` fails the delivery; `ignore` drops the message.

One of: `dot_to_slash`, `verbatim`, `error`, `ignore`.

**`exclusive`**

Only one consumer may be active on the queue, so this rules out running more than one instance against it.

**`on_decode_error`**

The message is dropped rather than delivered. Use this to publish to a dead-letter destination or record the failure.

Evaluated against the `decode-error` context.

**`prefetch`**

Bounds how much work is outstanding at once. Zero is unlimited, which lets the broker push an entire queue at once.

**`queue_size`**

When set, delivery is handed to a background goroutine so slow work does not block the source. The queue is bounded: a message that arrives when it is full is dropped. Delivery is reported successful as soon as the message is queued, so a source that acknowledges on successful delivery acknowledges before the work is done.

**`subscriber`**

Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.

**`transforms`**

A list of transform functions applied in order to each message. Only transform functions are in scope here.

#### Blocks

- `baggage` (optional) — Which inbound baggage keys to trust.
- `binding "<routing_key>"` (0..n) — Bind the queue to an exchange with a routing key.
- `declare` (optional) — Declare the queue if it does not already exist.
- `subscription "<routing_key_pattern>"` (0..n) — Maps arriving routing keys to a bus topic.

<!-- vinculum:end block-attrs client rabbitmq receiver -->

### `subscriber` / `action` / `transforms` / `queue_size`

Exactly one of `subscriber` or `action` must be specified. These four attributes
form the standard delivery pattern used by every block that dispatches events
(see [subscription](config.md#subscription)). `queue_size` runs delivery in a
background queue so a slow handler doesn't block the AMQP delivery loop; trace
context flows across the async boundary.

`queue_size` also changes when the message is acknowledged. Delivery counts as
successful the moment the message is queued, so the receiver acks then rather
than when the handler finishes: a handler error no longer nacks the message to
the dead-letter exchange, and a full queue drops a message that has already
been acked. Set it only if at-most-once delivery is acceptable, and prefer
`prefetch` for throughput. See [delivery model](config.md#delivery-model).

**Action context variables:**

<!-- vinculum:begin block-ctx client rabbitmq receiver action level=4 -->

Fields readable as `ctx.<name>` (shape `message`):

| Field | Type | Description |
|---|---|:---|
| `ctx.topic` | string | Topic the message was delivered on. |
| `ctx.msg` | dynamic | The message payload. |
| `ctx.fields` | object | String metadata attached to the message. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.msg`**

Already decoded by the client's `wire_format`, so its type follows the data rather than the transport.

**`ctx.fields`**

Always present; an empty object when the message carries no metadata.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx client rabbitmq receiver action -->

`ctx.fields` carries the AMQP headers table merged with the extracted
routing-key fields. W3C trace headers (`traceparent`, `tracestate`, `baggage`)
are stripped.

### `on_decode_error`

Optional. Evaluated when an inbound body fails to deserialize, *before* the
message is nacked. It is an observer: it cannot suppress the failure or cause
the message to be delivered. Errors inside the hook are logged and otherwise
ignored, so a broken hook can never change the outcome.

```hcl
receiver "in" {
  queue       = "events"
  action      = send(ctx, bus.main, ctx.topic, ctx.msg)

  on_decode_error = send(ctx, bus.dlq, "decode-error/${ctx.routing_key}", {
    raw   = tostring(ctx.raw),
    error = ctx.error,
  })
}
```

**Hook context variables:**

<!-- vinculum:begin block-ctx client rabbitmq receiver on_decode_error level=4 -->

Fields readable as `ctx.<name>` (shape `decode-error`):

| Field | Type | Description |
|---|---|:---|
| `ctx.raw` | object | The undecoded body, as a bytes object. |
| `ctx.error` | string | The deserialize error message. |
| `ctx.wire_format` | string | Name of the configured wire format that rejected it. |
| `ctx.topic` | string | Best-effort vinculum topic for the message. |
| `ctx.fields` | object | Metadata extracted before the failure. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.routing_key` | string | Routing key the message was delivered with. *(added here)* |
| `ctx.exchange` | string | Exchange the message was published to. *(added here)* |
| `ctx.queue` | string | Queue this receiver consumes from. *(added here)* |

*This shape is open: a particular site may carry fields beyond these.*

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx client rabbitmq receiver on_decode_error -->

`ctx.topic` is best-effort here: the routing-key transform is applied, but a
`subscription`'s `vinculum_topic` expression is not — that needs `msg`, which is
exactly what failed to decode.

### `declare`

Optional. When present, vinculum calls `QueueDeclare` (creating the queue if
missing) on every connect and reconnect. When absent, vinculum does a passive
declare to verify the queue exists and fails fast otherwise.

<!-- vinculum:begin block-attrs client rabbitmq receiver declare level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `auto_delete` | bool |  | `false` | Delete the queue once its last consumer disconnects. |
| `durable` | bool |  | `true` | Keep the queue across broker restarts. |

<!-- vinculum:end block-attrs client rabbitmq receiver declare -->

Manage the advanced queue arguments with a
[RabbitMQ policy](https://www.rabbitmq.com/parameters.html) or your
infrastructure tooling.

### `binding "<routing-key-pattern>"`

Optional. When present, vinculum calls `QueueBind` on every connect and
reconnect, binding the queue to the named exchange with the routing-key pattern
in the block label. Bindings are idempotent.

<!-- vinculum:begin block-attrs client rabbitmq receiver binding level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `exchange` | string | yes | Exchange to bind to. |

<!-- vinculum:end block-attrs client rabbitmq receiver binding -->

Bindings are about **topology** (what the broker delivers to the queue);
`subscription` blocks are about **dispatch** (how delivered messages map to
vinculum topics).

### `subscription "<routing-key-pattern>"`

Each `subscription` block maps an AMQP routing-key pattern (the block label) to
a vinculum topic. The pattern uses AMQP topic-exchange syntax: `*` matches
exactly one dot-delimited word, `#` matches zero or more words.

<!-- vinculum:begin block-attrs client rabbitmq receiver subscription level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `vinculum_topic` | expression (topic-pattern) |  | Bus topic to publish arriving messages to. |

**`vinculum_topic`**

`default_routing_key_transform` applies when omitted. Evaluated per delivery, so it can interpolate the fields the routing-key pattern captured. `ctx.fields` is the AMQP headers table merged with those captures.

Evaluated against the `inbound-message` context.

<!-- vinculum:end block-attrs client rabbitmq receiver subscription -->

**`vinculum_topic` expression context:**

<!-- vinculum:begin block-ctx client rabbitmq receiver subscription vinculum_topic level=4 -->

Fields readable as `ctx.<name>` (shape `inbound-message`):

| Field | Type | Description |
|---|---|:---|
| `ctx.msg` | dynamic | The message payload. |
| `ctx.fields` | object | String metadata attached to the message. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.routing_key` | string | Routing key the message was delivered with. *(added here)* |
| `ctx.exchange` | string | Exchange the message was published to. *(added here)* |

*This shape is open: a particular site may carry fields beyond these.*

**`ctx.msg`**

Already decoded by the client's `wire_format`, so its type follows the data rather than the transport — except on `client "sqs_receiver"`, which picks a topic before decoding and so passes the raw body here.

**`ctx.fields`**

Always present; an empty object when the message carries no metadata. What lands here is the transport's own metadata plus whatever the subscription's pattern captured.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx client rabbitmq receiver subscription vinculum_topic -->

> **Changed in 0.46.0.** `ctx.topic` was an alias for `ctx.routing_key` here,
> named for consistency with the receivers that had no better name to offer.
> They have one now, so the alias is gone; use `ctx.routing_key`, which every
> fixture and example already did.

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
