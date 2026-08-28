# Kafka Client (`client "kafka"`)

Vinculum can produce messages to and consume messages from Apache Kafka using
`client "kafka"` blocks. The implementation uses
[franz-go](https://github.com/twmb/franz-go) and supports TLS, SASL
authentication, consumer groups, and dead-letter queues.

A single `client "kafka"` block may contain any number of named `sender` and
`receiver` sub-blocks (at least one total). All senders and receivers within
a block share the same underlying broker connections, TLS configuration, and
SASL credentials.

---

## `client "kafka" "<name>"`

```hcl
client "kafka" "events" {
  # Bootstrap brokers — list 2-3 for redundancy.
  # The client discovers the full cluster topology after first contact.
  brokers = ["broker1:9092", "broker2:9092"]

  # Optional TLS
  tls {
    enabled              = true
    ca_cert              = "/etc/certs/ca.crt"
    cert                 = "/etc/certs/client.crt"  # optional, for mTLS
    key                  = "/etc/certs/client.key"  # optional, for mTLS
    insecure_skip_verify = false                     # default: false
  }

  # Optional SASL authentication
  sasl {
    mechanism = "SCRAM-SHA-256"  # PLAIN | SCRAM-SHA-256 | SCRAM-SHA-512
    username  = "vinculum"
    password  = env.KAFKA_PASSWORD
  }

  # Producer delivery settings (apply to the shared connection)
  acks        = "all"      # all | leader | none — default: all
  compression = "snappy"   # none | gzip | snappy | lz4 | zstd — default: none
  idempotent  = true       # default: true when acks = "all"
  linger      = "5ms"      # max wait to fill a batch — default: 10ms
  max_records = 10000      # max buffered records before a produce blocks — default: 10000

  # Wire format for payload serialization/deserialization (default: "auto")
  # wire_format = "json"     # auto | auto_bytes | json | string | bytes

  # Connection timeouts
  dial_timeout     = "10s"   # default: 10s
  request_timeout  = "30s"   # added to the request's own timeout — default: 10s
  metadata_max_age = "300s"  # how often to refresh broker/partition metadata — default: 5m

  # Named sender blocks (zero or more)
  sender "main" { ... }

  # Named receiver blocks (zero or more)
  receiver "main" { ... }
}
```

### Attributes

The delivery settings (`acks`, `compression`, `idempotent`, `linger`,
`max_records`) are connection-level and apply to every sender in the block. They
correspond directly to franz-go client options, and take effect only when the
block declares at least one `sender`.

<!-- vinculum:begin block-attrs client kafka level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `brokers` | list | yes |  | Bootstrap broker addresses. |
| `acks` | string |  | `all` | How many replicas must acknowledge a produced record. |
| `compression` | string |  | `none` | Compression applied to produced records. |
| `dial_timeout` | expression (duration) |  | `10s` | Deadline for establishing a connection to a broker. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `idempotent` | bool |  | `true` | Enable idempotent production, so retries cannot duplicate a record. |
| `linger` | expression (duration) |  | `10ms` | How long to wait for more records before sending a batch. |
| `max_records` | number |  | `10000` | Records that may be buffered awaiting production. |
| `metadata_max_age` | expression (duration) |  | `5m` | How long cluster metadata may be reused before it is refreshed. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `readiness` | bool |  | `true` | Whether this component gates the process's readiness. |
| `request_timeout` | expression (duration) |  | `10s` | Deadline for a single broker request. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

**`brokers`**

For example `["kafka-1:9092", "kafka-2:9092"]`.

**`acks`**

`all` waits for every in-sync replica, `leader` for the partition leader alone, and `none` does not wait at all. Idempotent production requires `all`.

One of: `none`, `leader`, `all`.

**`compression`**

One of: `none`, `gzip`, `snappy`, `lz4`, `zstd`.

**`dial_timeout`**

The default is franz-go's.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`idempotent`**

Requires `acks = "all"`.

**`linger`**

Trades latency for throughput. The default is franz-go's; zero sends each batch as soon as it is ready.

**`max_records`**

A produce blocks once this many records are outstanding, which is what bounds memory when the brokers cannot keep up. The default is franz-go's.

**`metadata_max_age`**

The default is franz-go's.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`readiness`**

This block reports whether it is currently serving, and by default that gates the process: while it is down, `/readyz` fails and traffic should go elsewhere. Set this false for an integration the service can do without, so losing it does not take the whole process out of rotation.

The attribute exists only on the types that have readiness to report; see [health](health.md).

**`request_timeout`**

Added to the timeout the request itself asks for, rather than replacing it. The default is franz-go's.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

#### Blocks

- `receiver "<name>"` (0..n) — Consumes Kafka topics as part of a consumer group.
- `sasl` (optional) — SASL credentials presented to the brokers.
- `sender "<name>"` (0..n) — Produces bus messages to Kafka topics.
- `tls` (optional) — TLS settings for this connection.

<!-- vinculum:end block-attrs client kafka -->

### TLS

The `tls` sub-block configures transport security.

<!-- vinculum:begin block-attrs client kafka tls level=4 -->

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

<!-- vinculum:end block-attrs client kafka tls -->

### SASL

The `sasl` sub-block configures authentication.

<!-- vinculum:begin block-attrs client kafka sasl level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `mechanism` | string | yes | SASL mechanism to authenticate with. |
| `password` | expression |  | Password to authenticate with. |
| `username` | string |  | Username to authenticate with. |

**`mechanism`**

Spelled as Kafka spells it, in upper case.

One of: `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`.

**`password`**

Supply it from the environment rather than a literal.

<!-- vinculum:end block-attrs client kafka sasl -->

---

## `sender "<name>"`

Each `sender` sub-block creates a named Kafka sender. Senders are
addressed in `subscription` blocks via `client.<client-name>.sender.<name>`
(single sender) or `client.<client-name>.senders` (fan-out to all senders).

```hcl
sender "main" {
  produce_mode = "sync"  # sync | async — default: sync

  # Topic mappings — evaluated in order, first match wins.
  topic "sensor/+deviceId/reading" {
    kafka_topic = "sensor.readings"
    key         = ctx.fields.deviceId   # HCL expression evaluated per message
  }
  topic "alerts/#" {
    kafka_topic = "alerts"
    key         = null              # null = no key (Kafka round-robins partitions)
  }

  # What to do when no topic matches:
  #   slash_to_dot — replace "/" with "." in the vinculum topic (e.g. a/b/c → a.b.c)
  #   error        — return an error (default)
  #   ignore       — silently drop the message
  default_topic_transform = "slash_to_dot"
}
```

### Sender attributes

<!-- vinculum:begin block-attrs client kafka sender level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `default_topic_transform` | string |  | `error` | How to derive a Kafka topic from a bus topic with no `topic` block. |
| `produce_mode` | string |  | `sync` | Whether to wait for the broker to acknowledge each record. |

**`default_topic_transform`**

`error` refuses the message, which is the default because a bus topic is rarely a valid Kafka topic by accident; `slash_to_dot` rewrites `a/b/c` as `a.b.c`; `ignore` drops it.

One of: `error`, `slash_to_dot`, `ignore`.

**`produce_mode`**

`sync` surfaces failures to the caller and applies backpressure; `async` returns as soon as the record is queued and logs any failure instead of returning it.

One of: `sync`, `async`.

#### Blocks

- `topic "<pattern>"` (0..n) — Maps bus topics matching a pattern to a Kafka topic.

<!-- vinculum:end block-attrs client kafka sender -->

### `topic "<pattern>"`

Each `topic` block maps a vinculum topic pattern to a Kafka topic and optional
record key. The pattern is the block label.

<!-- vinculum:begin block-attrs client kafka sender topic level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `kafka_topic` | string | yes | Kafka topic to produce to. |
| `key` | expression |  | Expression producing the record key. |

**`key`**

Records sharing a key land on the same partition, so a key is what preserves per-entity ordering. Evaluated per message; null produces a record with no key.

Evaluated against the `message` context.

<!-- vinculum:end block-attrs client kafka sender topic -->

**Key expression context** (all accessed via `ctx`):

<!-- vinculum:begin block-ctx client kafka sender topic key level=4 -->

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

<!-- vinculum:end block-ctx client kafka sender topic key -->

Named segments captured from the topic pattern arrive in `ctx.fields` —
`+deviceId` in the label becomes `ctx.fields.deviceId`. Common key expressions:
`ctx.fields.deviceId`, `ctx.msg.id`, `ctx.topic`, `null`.

---

## `receiver "<name>"`

Each `receiver` sub-block creates a named Kafka receiver that runs an
independent poll loop. Received messages are published to the configured
`subscriber`.

```hcl
receiver "main" {
  group_id     = "vinculum-prod"   # required: Kafka consumer group ID
  subscriber   = bus.main          # forward messages to a subscriber
  # OR
  # action     = expression        # evaluate an expression per message

  # Optional transform pipeline and async queue (same semantics as the
  # top-level `subscription` block — see config.md#subscription).
  # transforms = [ jq(".payload") ]
  # queue_size = 100

  start_offset = "stored"          # stored | earliest | latest — default: stored
  commit_mode  = "after_process"   # after_process | periodic | manual — default: after_process
  dlq_topic    = "vinculum.dlq"    # optional: dead-letter queue topic

  baggage {                        # optional; inbound baggage stripped by default
    allow = ["tenant_id"]
  }

  subscription "sensor.readings" {
    vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"
  }
  subscription "alerts" {
    vinculum_topic = "alerts/kafka"
  }
}
```

### `group_id`

Required. The Kafka consumer group ID. Multiple vinculum instances sharing the
same `group_id` will each process a subset of partitions — standard Kafka
consumer group semantics.

### Receiver attributes

<!-- vinculum:begin block-attrs client kafka receiver level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `group_id` | string | yes |  | Consumer group this receiver joins. |
| `action` | expression (action-expression) |  |  | Expression evaluated once per message. |
| `commit_mode` | string |  | `after_process` | When to commit consumed offsets. |
| `dlq_topic` | string |  |  | Kafka topic to publish messages that could not be handled. |
| `on_decode_error` | expression (action-expression) |  |  | Evaluated when an inbound message cannot be decoded. |
| `queue_size` | number |  |  | Depth of an async queue wrapping the subscriber. |
| `start_offset` | string |  | `stored` | Where to start when the group has no committed offset. |
| `subscriber` | expression (subscriber-ref) |  |  | Subscriber to forward messages to, instead of evaluating an action. |
| `transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied before the action or subscriber. |

- Specify at most one of action or subscriber.
- Specify either an action to evaluate or a subscriber to forward to.

**`group_id`**

Kafka distributes each topic's partitions across the members of a group.

**`action`**

`ctx.topic` is the message topic and `ctx.msg` the payload; a protocol that extracts metadata also provides `ctx.fields`.

Evaluated against the `message` context.

**`commit_mode`**

`after_process` commits once delivery succeeds, giving at-least-once delivery; `periodic` commits on a timer, which can lose or duplicate messages across a crash; `manual` never commits automatically and is reserved for transactional use.

One of: `after_process`, `periodic`, `manual`.

**`dlq_topic`**

The record keeps its key and value and gains `vinculum-error`, `vinculum-original-topic`, and `vinculum-timestamp` headers. The offset is committed only once the dead-letter send succeeds, so a failure there redelivers rather than drops.

**`on_decode_error`**

The message is dropped rather than delivered. Use this to publish to a dead-letter destination or record the failure.

Evaluated against the `decode-error` context.

**`queue_size`**

When set, decouples delivery from the action so a slow action does not block the source.

**`start_offset`**

`stored` resumes from the group's committed offset, which is what production wants; `earliest` replays the whole topic; `latest` skips everything already there.

One of: `stored`, `earliest`, `latest`.

**`subscriber`**

Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.

**`transforms`**

A list of transform functions applied in order to each message. Only transform functions are in scope here.

#### Blocks

- `baggage` (optional) — Which inbound baggage keys to trust.
- `subscription "<kafka_topic>"` (1..n) — One Kafka topic to consume.

<!-- vinculum:end block-attrs client kafka receiver -->

### `subscriber` / `action`

Exactly one must be specified.

- `subscriber` — forward each received message to a bus or subscriber (e.g. `bus.main`).
- `action` — evaluate an HCL expression for each message. See context variables below.

Optionally, `transforms = [...]` applies a transform pipeline to each message
before delivery, and `queue_size = N` wraps delivery in an async background
queue of depth `N` so slow handlers don't block the Kafka poll loop. Same
semantics as the top-level [subscription](config.md#subscription) block.

#### Action context variables

When `action` is used, `ctx` provides:

<!-- vinculum:begin block-ctx client kafka receiver action level=5 -->

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

<!-- vinculum:end block-ctx client kafka receiver action -->

`ctx.fields` is populated from the record's Kafka headers.

### `dlq_topic`

A dead-letter record preserves the original key and value and adds these
headers:

| Header | Contents |
|---|---|
| `vinculum-error` | The error message from the failed handler |
| `vinculum-original-topic` | The original Kafka topic the record came from |
| `vinculum-timestamp` | ISO 8601 timestamp of when the failure occurred |

### `baggage`

Inbound Kafka message [baggage](baggage.md) is untrusted input, so each receiver
**strips it by default** before it reaches the action or downstream
re-propagation. Add an optional `baggage {}` block to opt into trusting upstream
baggage — `passthrough = true`, `allow = [...]`, or `deny = [...]`. See
[Server-side trust filtering](baggage.md#server-side-trust-filtering) for the
full attribute set. The block is per-receiver. (Trace continuity is unaffected;
only baggage key/value pairs are filtered.)

### `subscription "<kafka-topic>"`

Each `subscription` block maps one Kafka topic to a vinculum topic. The Kafka
topic is the block label. Multiple blocks may be declared within a single
receiver.

<!-- vinculum:begin block-attrs client kafka receiver subscription level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `vinculum_topic` | expression (topic-pattern) | yes | Bus topic to publish arriving records to. |

**`vinculum_topic`**

Evaluated per record, so it can interpolate the record's own identity and headers. `ctx.fields` is populated from the record's Kafka headers.

Evaluated against the `inbound-message` context.

<!-- vinculum:end block-attrs client kafka receiver subscription -->

**`vinculum_topic` expression context** (all accessed via `ctx`):

<!-- vinculum:begin block-ctx client kafka receiver subscription vinculum_topic level=4 -->

Fields readable as `ctx.<name>` (shape `inbound-message`):

| Field | Type | Description |
|---|---|:---|
| `ctx.msg` | dynamic | The message payload. |
| `ctx.fields` | object | String metadata attached to the message. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.kafka_topic` | string | Kafka topic the record was read from. *(added here)* |
| `ctx.key` | string | The record's key. *(added here)* *(not always present)* |

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

**`ctx.key`**

Null when the record was produced without one.

<!-- vinculum:end block-ctx client kafka receiver subscription vinculum_topic -->

**Deserialization** is controlled by the client-level `wire_format` attribute
(default `"auto"`). In auto mode, values that look like JSON are decoded;
everything else becomes a string. Use `wire_format = "json"` for strict JSON
decoding, or `"string"`/`"bytes"` for raw passthrough. Kafka record headers
become the `fields` map.

#### Decode failures

The configured `wire_format` is a **contract**. When an inbound payload fails
to deserialize, the record is treated as failed — it is *not* delivered to the subscriber as raw
bytes.

> **Configure `dlq_topic`.** A failed record's offset is not committed. With
> a `dlq_topic` set, the record is routed there and the offset advances. Without
> one, the consumer re-fetches the same record forever and the partition never
> makes progress — every later message is starved. Vinculum emits a config-time
> warning when a receiver combines a strict `wire_format` with no `dlq_topic`.

Set `on_decode_error` to observe the failure (log it, publish it to a
dead-letter topic, increment a counter). The hook cannot suppress the failure;
errors inside it are logged and otherwise ignored.

```hcl
receiver "in" {
  group_id  = "g1"
  dlq_topic = "events-dlq"
  subscription "events" { vinculum_topic = "events" }
  action    = send(ctx, bus.main, ctx.topic, ctx.msg)

  on_decode_error = log::error("bad record", {
    topic     = ctx.topic,
    partition = ctx.partition,
    offset    = ctx.offset,
    error     = ctx.error,
  })
}
```

**Hook context variables:**

<!-- vinculum:begin block-ctx client kafka receiver on_decode_error level=5 -->

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
| `ctx.kafka_topic` | string | The Kafka topic the record was read from. *(added here)* |
| `ctx.partition` | string | Partition the record was read from. *(added here)* |
| `ctx.offset` | string | Offset of the record within its partition. *(added here)* |
| `ctx.key` | string | The record's key. *(added here)* *(not always present)* |

*This shape is open: a particular site may carry fields beyond these.*

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

**`ctx.kafka_topic`**

Equal to `ctx.topic` here: a vinculum topic derived from the payload cannot be computed once the payload has failed to decode, so `ctx.topic` falls back to this.

**`ctx.key`**

Absent when the record was produced without one.

<!-- vinculum:end block-ctx client kafka receiver on_decode_error -->

`ctx.topic` and `ctx.kafka_topic` carry the same string here, because a vinculum
topic that depends on the payload cannot be computed once the payload has failed
to decode, so `ctx.topic` falls back to the Kafka topic.

> **Changed in 0.45.0.** `ctx.kafka_topic` was previously offered under the name
> `topic`, which collides with `ctx.topic` above; the collision was resolved in
> favour of the fixed field, so it was dropped and never reached a config at all.


Use `wire_format = "auto_bytes"` if you want best-effort decoding instead: it
decodes JSON like `auto` and yields a [`bytes`](functions.md) value for anything
it can't parse. `auto` behaves the same but yields a string — pick whichever
type your handler wants. Neither ever fails to decode.

> **Changed in 0.44.0.** Earlier releases logged a warning and delivered the
> raw bytes. See [deprecations](deprecations.md#tolerant-wire-format-decoding).


**Static topic:**
```hcl
subscription "alerts" {
  vinculum_topic = "alerts/kafka"
}
```

**Dynamic topic from a record header:**
```hcl
subscription "sensor.readings" {
  vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"
}
```

**Dynamic topic from the record key:**
```hcl
subscription "sensor.readings" {
  vinculum_topic = "sensor/${ctx.key}/reading"
}
```

---

## Addressing Senders in Subscriptions

`client.<name>` resolves to a cty object with two attributes for routing
messages to Kafka senders:

| Expression | Meaning |
|---|---|
| `client.<name>.senders` | Fan-out: dispatch `OnEvent` to **all** named senders. |
| `client.<name>.sender.<name>` | Route to a single named sender. |

```hcl
# Fan-out to all senders
subscription "all_to_kafka" {
  target     = bus.main
  topics     = ["sensor/#", "alerts/#"]
  subscriber = client.events.senders
}

# Single named sender
subscription "sensors_only" {
  target     = bus.main
  topics     = ["sensor/#"]
  subscriber = client.events.sender.main
}
```

---

## Distributed Tracing

Add a `tracing` attribute to enable W3C TraceContext propagation across Kafka
records. The consumer extracts incoming `traceparent`/`tracestate` headers and
creates a child span; the producer injects trace context into outgoing record
headers.

```hcl
client "kafka" "events" {
    tracing = client.tracer   # optional; auto-wired to the default client "otlp"
    ...
}
```

If there is exactly one `client "otlp"` block (or one marked `default = true`),
the Kafka client auto-wires to it when `tracing =` is omitted.

**Consumer:** for each record received, a new root `SpanKindConsumer` span is
created and linked to the producer span (extracted from the `traceparent` header).
This follows the [OTel messaging semantic conventions](https://opentelemetry.io/docs/specs/otel/trace/semantic_conventions/messaging/)
recommendation for async pub/sub: the consumer trace is independent but linked,
so the async boundary is correctly represented. Spans carry `messaging.system`,
`messaging.destination.name`, `messaging.operation.type`, and
`messaging.operation.name` attributes automatically.

**Producer:** the current trace context is injected into outgoing record headers
as `traceparent` / `tracestate`, and a `SpanKindProducer` span wraps the produce call.

**Header filtering:** W3C trace headers (`traceparent`, `tracestate`, `baggage`)
are stripped from the `fields` map visible in VCL action expressions so business
metadata stays clean.

See [client "otlp"](client-otlp.md) for tracing configuration and auto-wiring rules.

---

## Observability

When a [`server "metrics"`](server-metrics.md) block is present, the Kafka
client automatically exposes sender and receiver metrics. The default metrics
server is used implicitly, or you can wire a specific one explicitly:

```hcl
client "kafka" "events" {
  metrics = server.mymetrics   # optional; uses default server if omitted
  ...
}
```

### Sender metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kafka_producer_records_sent_total` | counter | `topic` | Records successfully produced |
| `kafka_producer_errors_total` | counter | `topic` | Production errors |
| `kafka_producer_produce_duration_seconds` | histogram | `topic` | Time for `ProduceSync` to return (sync mode only) |

### Receiver metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kafka_consumer_records_received_total` | counter | `topic` | Records successfully processed |
| `kafka_consumer_errors_total` | counter | `topic` | Processing errors (includes DLQ failures) |
| `kafka_consumer_lag` | gauge | `topic`, `partition` | Records behind the high-water mark |
| `kafka_consumer_process_duration_seconds` | histogram | `topic` | Time for `subscriber.OnEvent` to return |
| `kafka_consumer_commits_total` | counter | — | Successful offset commits |

`kafka_consumer_lag` is updated at the end of every poll cycle. A value of 0
means the receiver is caught up on that partition.

### Example

```hcl
server "metrics" "metrics" {
  listen = ":9090"
}

client "kafka" "events" {
  brokers = ["kafka.internal:9093"]
  # ... (metrics provider is wired automatically from server.metrics above)
  receiver "main" { ... }
  sender "main" { ... }
}
```

After a few poll cycles, scraping `:9090/metrics` will show entries like:

```
kafka_consumer_lag{partition="0",topic="sensor.readings"} 0
kafka_consumer_records_received_total{topic="sensor.readings"} 42
kafka_producer_records_sent_total{topic="sensor.readings"} 42
kafka_producer_produce_duration_seconds_sum{topic="sensor.readings"} 0.087
```

---

## Complete Example

```hcl
bus "main" {}

client "kafka" "events" {
  brokers = ["kafka.internal:9093"]

  tls {
    enabled = true
  }

  sasl {
    mechanism = "SCRAM-SHA-256"
    username  = "vinculum"
    password  = env.KAFKA_PASSWORD
  }

  acks        = "all"
  compression = "snappy"
  linger      = "5ms"

  sender "main" {
    produce_mode            = "sync"
    default_topic_transform = "slash_to_dot"

    topic "sensor/+deviceId/reading" {
      kafka_topic = "sensor.readings"
      key         = ctx.fields.deviceId
    }
  }

  receiver "main" {
    group_id   = "vinculum-prod"
    subscriber = bus.main
    dlq_topic  = "vinculum.dlq"

    subscription "sensor.readings" {
      vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"
    }
    subscription "alerts" {
      vinculum_topic = "alerts/kafka"
    }
  }
}

# Forward internal bus events to Kafka
subscription "to_kafka" {
  target     = bus.main
  topics     = ["vinculum/#"]
  subscriber = client.events.senders
}

# Log everything that arrives from Kafka (and anything else on the bus)
subscription "debug" {
  target = bus.main
  topics = ["#"]
  action = log::info("event", {topic = ctx.topic, msg = ctx.msg})
}
```

Receiver with `action` instead of `subscriber` — log each Kafka message directly:

```hcl
client "kafka" "events" {
  brokers = ["kafka:9092"]

  receiver "logger" {
    group_id = "vinculum-logger"
    action   = log::info("kafka", {topic = ctx.topic, msg = ctx.msg})

    subscription "sensor.readings" {
      vinculum_topic = "sensor/readings"
    }
  }
}
```
