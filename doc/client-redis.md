# Redis/Valkey Clients

Vinculum talks to Redis and Valkey through four composed block types. One
passive `client "redis"` holds the connection (address, auth, TLS, pool),
and up to three child clients reference it for different usage modes:

| Block | Purpose | Redis commands |
| --- | --- | --- |
| `client "redis"` | Connection manager | (none — passive) |
| `client "redis_pubsub"` | Channel messaging | `PUBLISH`, `SUBSCRIBE`, `PSUBSCRIBE` |
| `client "redis_stream"` | Persistent log | `XADD`, `XREADGROUP`, `XACK`, `XCLAIM` |
| `client "redis_kv"` | Key-value / hash store | `GET`, `SET`, `INCR`, `HGET`, `HSET` |

Valkey is a drop-in for Redis; the same `address` works for both. The
implementation uses [go-redis/v9](https://github.com/redis/go-redis), which
speaks RESP3 and routes to `NewClient`, `NewFailoverClient`, or
`NewClusterClient` based on the `mode` attribute.

---

## `client "redis" "<name>"`

The base block is passive — it manages the connection pool but performs no
messaging itself. Child blocks attach via `connection = client.<name>`; the
config dependency graph ensures the base is built first.

### Standalone (default)

```hcl
client "redis" "myredis" {
  address  = "localhost:6379"
  username = "default"            # optional; Redis 6+ / Valkey ACL user
  password = env.REDIS_PASSWORD
  database = 0                    # optional; 0–15

  tls {
    enabled              = true
    ca_cert              = "/etc/certs/ca.crt"
    cert                 = "/etc/certs/client.crt"   # optional, mTLS
    key                  = "/etc/certs/client.key"   # optional, mTLS
    insecure_skip_verify = false
  }

  pool_size      = 10
  min_idle_conns = 2
  dial_timeout   = "5s"
}
```

On `Start`, the client issues a `PING` to fail fast on bad credentials or a
bad address; go-redis otherwise opens connections lazily from the pool.

### Cluster

```hcl
client "redis" "cluster" {
  mode      = "cluster"
  addresses = ["redis1:6379", "redis2:6379", "redis3:6379"]
  password  = env.REDIS_PASSWORD
}
```

`database` is not supported in cluster mode (all nodes use database 0).

### Sentinel

```hcl
client "redis" "ha" {
  mode              = "sentinel"
  addresses         = ["sent1:26379", "sent2:26379", "sent3:26379"]
  master_name       = "mymaster"
  sentinel_username = "default"              # optional
  sentinel_password = env.SENTINEL_PASSWORD  # optional
  username          = "default"              # Redis-side ACL user
  password          = env.REDIS_PASSWORD
}
```

### Attributes

Which of `address`, `addresses`, and `master_name` is required — and which is
rejected — depends on `mode`; see the sections above. The `tls` block is the
shared one, documented under [TLS configuration](config.md#tls).

<!-- vinculum:begin block-attrs client redis level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `address` | string |  |  | Address of the server, for standalone mode. |
| `addresses` | list |  |  | Addresses of the cluster or sentinel nodes. |
| `database` | number |  | `0` | Database number to select. |
| `dial_timeout` | expression (duration) |  | `5s` | Deadline for establishing a connection. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `master_name` | string |  |  | Name of the master the sentinels monitor. |
| `min_idle_conns` | number |  | `0` | Idle connections kept ready in the pool. |
| `mode` | string |  | `standalone` | Topology to connect to. |
| `password` | expression |  |  | Password to authenticate with. |
| `pool_size` | number |  | `10 × GOMAXPROCS` | Maximum number of connections in the pool, per node. |
| `readiness` | bool |  | `true` | Whether this component gates the process's readiness. |
| `sentinel_password` | expression |  |  | Password to authenticate to the sentinels with. |
| `sentinel_username` | string |  |  | Username to authenticate to the sentinels with. |
| `username` | string |  |  | Username to authenticate with. |

- Specify at most one of address or addresses.
- master_name requires addresses.

**`address`**

For example `"localhost:6379"`.

**`addresses`**

Used by `cluster` and `sentinel` mode instead of `address`.

**`database`**

Not available in `cluster` mode, which has a single keyspace.

**`dial_timeout`**

The default is go-redis's.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`master_name`**

Required in `sentinel` mode.

**`min_idle_conns`**

Keeping a few warm trades memory for latency on a burst.

**`mode`**

`standalone` takes a single `address`; `cluster` and `sentinel` take `addresses`, and `sentinel` also needs `master_name`.

One of: `standalone`, `cluster`, `sentinel`.

**`password`**

Supply it from the environment rather than a literal.

**`pool_size`**

The default is go-redis's, which scales with the machine.

**`readiness`**

This block reports whether it is currently serving, and by default that gates the process: while it is down, `/readyz` fails and traffic should go elsewhere. Set this false for an integration the service can do without, so losing it does not take the whole process out of rotation.

The attribute exists only on the types that have readiness to report; see [health](health.md).

**`sentinel_password`**

`sentinel` mode only, and separate from the credentials used for the master.

**`sentinel_username`**

`sentinel` mode only, and separate from the credentials used for the master.

**`username`**

Redis 6 and later, with ACLs configured. A server using plain `requirepass` wants `password` alone.

#### Blocks

- `tls` (optional) — TLS settings for this connection.

<!-- vinculum:end block-attrs client redis -->

Reconnection is handled by the go-redis pool transparently — there is no
`reconnect` block or `on_connect` / `on_disconnect` hook. When a lifecycle
event matters, use [`trigger "start"`](trigger.md) / `trigger "shutdown"`.

---

## `client "redis_pubsub" "<name>"`

Pub/sub has no persistence, no acknowledgements, and no broker-side
queuing — if a subscriber is disconnected when a message is published, the
message is lost. The channel namespace (`.`-separated by convention) maps
cleanly onto vinculum's topic namespace (`/`-separated).

```hcl
client "redis_pubsub" "rps" {
  connection  = client.myredis
  wire_format = "auto"               # auto | auto_bytes | json | string | bytes (default: auto)
  metrics     = server.metrics.main  # optional
  tracing     = server.metrics.main  # optional

  publisher "main" {
    # Optional block-level fallback expression when no channel_mapping
    # matches. Evaluated per message with ctx.topic/ctx.msg/ctx.fields
    # in scope.
    # channel_transform = replace(ctx.topic, "/", ".")

    # Final fallback if no channel_mapping matches and no
    # channel_transform is set.
    default_channel_transform = "verbatim"   # verbatim | ignore | error

    # First-match-wins per-pattern overrides. Patterns follow MQTT syntax
    # with optional +name captures that populate ctx.fields.
    channel_mapping {
      pattern = "alerts/#"
      channel = "alerts"
    }
    channel_mapping {
      pattern = "device/+deviceId/status"
      channel = "devices.${ctx.fields.deviceId}.status"
    }
  }

  subscriber "in" {
    # Exactly one of:
    subscriber = bus.main
    # action   = send(ctx, bus.main, "redis/${ctx.topic}", ctx.msg)

    # Optional transform pipeline and async queue (same semantics as the
    # top-level `subscription` block — see config.md#subscription).
    # transforms = [ jq(".payload") ]
    # queue_size = 100

    # Exact channels use SUBSCRIBE; globs (*, ?, [...]) use PSUBSCRIBE.
    channel_subscription {
      channel        = "alerts"
      vinculum_topic = "alerts/redis"   # optional; default is the channel name
    }
    channel_subscription {
      channel = "devices.*"             # pattern → PSUBSCRIBE
    }
  }
}
```

### Pub/sub attributes

<!-- vinculum:begin block-attrs client redis_pubsub level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `connection` | expression (client-ref) | yes |  | Redis connection to use. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

**`connection`**

A `client "redis"` block.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

#### Blocks

- `publisher "<name>"` (0..n) — Publishes bus messages to Redis channels.
- `subscriber "<name>"` (0..n) — Subscribes to Redis channels and delivers what arrives.

<!-- vinculum:end block-attrs client redis_pubsub -->

#### `publisher`

<!-- vinculum:begin block-attrs client redis_pubsub publisher level=5 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `channel_transform` | expression |  |  | Expression deriving a channel name from a message. |
| `default_channel_transform` | string |  | `verbatim` | How to derive a channel from a bus topic with no `channel_mapping` block. |

**`channel_transform`**

Evaluated per message, and consulted before `default_channel_transform`.

Evaluated against the `message` context.

**`default_channel_transform`**

`verbatim` publishes to a channel named for the bus topic; `ignore` drops the message; `error` refuses it.

One of: `verbatim`, `ignore`, `error`.

##### Blocks

- `channel_mapping` (0..n) — Maps bus topics matching a pattern to a Redis channel.

<!-- vinculum:end block-attrs client redis_pubsub publisher -->

#### `subscriber`

<!-- vinculum:begin block-attrs client redis_pubsub subscriber level=5 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) |  |  | Expression evaluated once per message. |
| `on_decode_error` | expression (action-expression) |  |  | Evaluated when an inbound message cannot be decoded. |
| `partition_key` | expression |  |  | Expression deciding which messages must stay in order. |
| `partitions` | number |  | `1` | Number of messages that may be processed at once. |
| `queue_size` | number |  |  | Depth of an async queue wrapping the subscriber. |
| `subscriber` | expression (subscriber-ref) |  |  | Subscriber to forward messages to, instead of evaluating an action. |
| `transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied before the action or subscriber. |

- Specify at most one of action or subscriber.
- Specify either an action to evaluate or a subscriber to forward to.
- partitions runs one queue per partition, so it needs queue_size to say how deep each is.
- partition_key decides which partition a message goes to, which means nothing without partitions.

**`action`**

`ctx.topic` is the message topic and `ctx.msg` the payload; a protocol that extracts metadata also provides `ctx.fields`.

Evaluated against the `message` context.

**`on_decode_error`**

The message is dropped rather than delivered. Use this to publish to a dead-letter destination or record the failure.

Evaluated against the `decode-error` context.

**`partition_key`**

Messages whose key is equal are processed in the order they arrived, by one goroutine; messages whose keys differ may be processed at once. Choose the key that names the thing order matters for — a device, an account, a conversation.

Defaults to the topic. `null` asks for no ordering at all, dealing messages round-robin across every partition, which is both faster and more evenly spread than a key contrived to vary.

It is evaluated on the goroutine that hands the message over — the receiver's poll loop, or the bus's dispatch — so its cost falls on the thing `queue_size` was protecting. A plain `ctx.fields.<name>` or `ctx.topic` costs nothing: it is read straight off the message, with no expression evaluated at all. Anything else is evaluated per message, and reading `ctx.msg` is the expensive case, since the payload is converted for this expression as well as for the work.

The key sees the message as it arrived, not as `transforms` will deliver it: a pipeline that rewrites the topic does so after the partition has been chosen.

Evaluated against the `partition-key` context.

**`partitions`**

Runs this many queues, each drained by its own goroutine, so that many messages are handled in parallel. Order is preserved within a partition and not across them, and `partition_key` decides which messages share one — so the key is where ordering is configured and this is only how much parallelism the rest may use.

**A message picks its partition by hashing its key, so partitions do nothing until the key varies.** The default key is the topic: on a receiver where every message arrives on the same topic, every message hashes to the same partition and nothing runs in parallel. Set `partition_key`, or `partition_key = null` if no ordering is required at all.

`queue_size` is per partition, so `queue_size = 500` with `partitions = 8` is up to 4000 messages buffered — reconcile that with whatever bounds in-flight messages on the source, such as a RabbitMQ `prefetch` or an SQS visibility timeout.

Work that runs in parallel must tolerate running in parallel. Two partitions evaluating `set(ctx, var.n, get(var.n) + 1)` lose updates, whatever the key.

**`queue_size`**

When set, delivery is handed to a background goroutine so slow work does not block the source. The queue is bounded, and what happens to a message that arrives when it is full depends on where it came from: one that arrived over a transport that acknowledges is nacked, so the broker redelivers it, and any other is dropped and counted. On a receiver this composes with `ack` rather than conflicting with it — the acknowledgement follows the message through the queue and arrives when the work finishes.

A graceful shutdown runs the queue out rather than exiting past it: see [Boot and shutdown](health.md#boot-and-shutdown).

**`subscriber`**

Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.

**`transforms`**

A list of transform functions applied in order to each message. Only transform functions are in scope here.

##### Blocks

- `channel_subscription` (1..n) — One Redis channel or channel pattern to subscribe to.

<!-- vinculum:end block-attrs client redis_pubsub subscriber -->

A `channel_subscription`'s `vinculum_topic` reads the channel a message arrived
on as `ctx.channel`, alongside `ctx.msg` and `ctx.fields`. There is no
`ctx.topic`: no bus topic exists yet, since producing one is what the
expression is for.

> **Changed in 0.46.0.** The channel used to be offered here as `ctx.topic`,
> and under no other name. It is now `ctx.channel` — the same name
> `on_decode_error` gives it — and `ctx.topic` is gone. `vinculum_topic =
> ctx.topic` is reported by `vinculum check` rather than failing at runtime.

### Publisher behavior

- Payloads are serialized according to the client-level `wire_format`
  (default `"auto"`). In auto mode, strings and bytes pass through verbatim;
  everything else is JSON-encoded.
- `bus.Subscriber` fan-out: the "publishers" wrapper implements `bus.Subscriber`
  and forwards to every publisher block, so
  `subscriber = client.clientname.publishers` broadcasts to all of them. Or, you can send
  to a specific publisher with `client.clientname.publisher.publishername`

### Subscriber behavior

- Payloads are deserialized according to the client-level `wire_format`
  (default `"auto"`). In auto mode, JSON is decoded; non-JSON becomes a
  string.
- A decode failure is **fatal to the message**: it is dropped, not delivered
  as raw bytes. Pub/sub has no acknowledgement, so nothing accumulates. Set
  `on_decode_error` to observe it, or `wire_format = "auto"` for best-effort
  decoding. See [Decode failures](#decode-failures).
- go-redis automatically re-subscribes on reconnect, so the
  `SUBSCRIBE`/`PSUBSCRIBE` set stays live without explicit config.
- Redis keyspace notifications (`__keyevent@0__:expired` etc.) are just
  ordinary channels — subscribe to them like any other. Enabling the
  server-side `notify-keyspace-events` setting is the operator's
  responsibility; Vinculum does not touch it.

### Decode failures

The configured `wire_format` is a **contract**. When an entry's payload field
fails to deserialize, the entry is *not* delivered to the subscriber as raw
bytes and is *not* `XACK`ed.

> **Configure dead-lettering.** A failed entry stays in the pending-entries
> list. With `dead_letter_stream` set it is moved to the DLQ stream once
> `dead_letter_after` retries are exhausted. Without it, the entry stays
> pending indefinitely. Vinculum emits a config-time warning when a receiver
> combines a strict `wire_format` with no `dead_letter_stream`.

Set `on_decode_error` to observe the failure. It cannot suppress it; errors
inside the hook are logged and otherwise ignored.

```hcl
receiver "in" {
  stream             = "events"
  group              = "g1"
  wire_format        = "json"
  dead_letter_stream = "events-dlq"
  dead_letter_after  = 3
  action             = send(ctx, bus.main, ctx.topic, ctx.msg)

  on_decode_error = log::error("bad entry", {
    stream   = ctx.stream,
    entry_id = ctx.entry_id,
    error    = ctx.error,
    raw      = tostring(ctx.raw),
  })
}
```

**Hook context variables.** Both pub/sub and stream support
`on_decode_error`, and each adds its own transport identity to the shared
fields.

On a `redis_pubsub` subscriber:

<!-- vinculum:begin block-ctx client redis_pubsub subscriber on_decode_error level=4 -->

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
| `ctx.channel` | string | Channel the message was published to. *(added here)* |
| `ctx.matched_pattern` | string | The `channel_subscription` pattern that matched. *(added here)* *(not always present)* |

*This shape is open: a particular site may carry fields beyond these.*

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

**`ctx.matched_pattern`**

Absent when the subscription named the channel exactly rather than by pattern.

<!-- vinculum:end block-ctx client redis_pubsub subscriber on_decode_error -->

On a `redis_stream` consumer:

<!-- vinculum:begin block-ctx client redis_stream consumer on_decode_error level=4 -->

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
| `ctx.stream` | string | Stream the entry was read from. *(added here)* |
| `ctx.entry_id` | string | Redis entry ID, e.g. `1700000000000-0`. *(added here)* |
| `ctx.group` | string | Consumer group this receiver reads as. *(added here)* |
| `ctx.consumer` | string | This receiver's consumer name within the group. *(added here)* |

*This shape is open: a particular site may carry fields beyond these.*

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx client redis_stream consumer on_decode_error -->

`ctx.fields` is always empty for a stream entry: its fields are read in the same
pass that decodes the payload, so a partial map would depend on Go's randomized
map iteration order.

Use `wire_format = "auto_bytes"` if you want best-effort decoding instead: it
decodes JSON like `auto` and yields a [`bytes`](functions.md) value for anything
it can't parse. `auto` behaves the same but yields a string — pick whichever
type your handler wants. Neither ever fails to decode.

> **Changed in 0.44.0.** Earlier releases logged a warning and delivered the
> raw bytes. See [deprecations](deprecations.md#tolerant-wire-format-decoding).

### Trace context

Redis pub/sub has no header mechanism and no ratified OTel convention, so
trace context **and [baggage](baggage.md)** are **not** propagated. Subscribers
start a fresh root `SpanKindConsumer` span per delivered message, and
`ctx.baggage` is always empty for a pub/sub-received message (there is nothing
to strip, so no `baggage {}` block applies here). If end-to-end tracing or
baggage is required, use `redis_stream` (which carries both as entry fields) or
encode the values into the payload yourself.

---

## `client "redis_stream" "<name>"`

Redis Streams are a persistent, consumer-group-aware log. This block is
the closest Redis analogue to the Kafka client.

```hcl
client "redis_stream" "rs" {
  connection  = client.myredis
  wire_format = "auto"               # auto | auto_bytes | json | string | bytes (default: auto)
  metrics     = server.metrics.main
  tracing     = server.metrics.main

  producer "out" {
    # Stream name, evaluated per message. Default when omitted: vinculum
    # topic with "/" → ":".
    stream             = "events:${ctx.fields.region}"
    maxlen             = 10000
    approximate_maxlen = true              # MAXLEN ~ (default: true)
    default_stream_transform = "error"     # error | ignore

    # Entry layout — all have sensible defaults.
    payload_field      = "data"            # empty string suppresses the field
    topic_field        = "topic"
    content_type_field = "datacontenttype"
    fields_mode        = "flat"            # flat | nested | omit
  }

  consumer "in" {
    stream         = "events"
    group          = "workers"
    consumer_name  = sys.hostname           # default: <host>-<client>-<consumer>
    vinculum_topic = "stream/${ctx.stream}" # optional remap

    # Exactly one of:
    subscriber = bus.main
    # action   = [ ..., inbound::ack(ctx) ]

    # Optional transform pipeline and async queue (same semantics as the
    # top-level `subscription` block — see config.md#subscription).
    # transforms = [ jq(".payload") ]
    # queue_size = 100                    # the XACK waits for the work

    # Optional; inbound baggage is stripped by default. See doc/baggage.md.
    # baggage { allow = ["tenant_id"] }

    batch_size     = 10
    block_timeout  = "2s"
    ack            = "auto"               # auto | manual (manual also needs settle_timeout)
    group_create   = "create_if_missing"  # create_if_missing | require_existing | create_from_start

    reclaim_pending   = true
    reclaim_min_idle  = "5m"

    # dead_letter_stream = "events:dlq"
    # dead_letter_after  = 3
  }
}
```

### Stream attributes

<!-- vinculum:begin block-attrs client redis_stream level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `connection` | expression (client-ref) | yes |  | Redis connection to use. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

**`connection`**

A `client "redis"` block.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

#### Blocks

- `consumer "<name>"` (0..n) — Consumes a Redis stream as part of a consumer group.
- `producer "<name>"` (0..n) — Writes bus messages into a Redis stream.

<!-- vinculum:end block-attrs client redis_stream -->

#### `producer`

<!-- vinculum:begin block-attrs client redis_stream producer level=5 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `approximate_maxlen` | bool |  | `true` | Let Redis trim approximately, which is much cheaper. |
| `content_type_field` | string |  | `datacontenttype` | Stream field carrying the payload's content type. |
| `default_stream_transform` | string |  | `error` | How to derive a stream name from a bus topic when `stream` is unset. |
| `fields_mode` | string |  | `flat` | How the rest of the entry's fields map to message fields. |
| `maxlen` | number |  |  | Trim the stream to roughly this many entries. |
| `payload_field` | string |  | `data` | Stream field carrying the message payload. |
| `stream` | expression |  |  | Stream to write entries to. |
| `topic_field` | string |  | `topic` | Stream field carrying the bus topic. |

**`approximate_maxlen`**

Only meaningful alongside `maxlen`.

**`default_stream_transform`**

`error` refuses the message, since a bus topic is rarely a stream name by accident; `ignore` drops it.

One of: `error`, `ignore`.

**`fields_mode`**

`flat` puts each remaining stream field at the top level of the message's fields; `nested` groups them; `omit` discards them.

One of: `flat`, `nested`, `omit`.

**`maxlen`**

Without a cap the stream grows without bound.

<!-- vinculum:end block-attrs client redis_stream producer -->

#### `consumer`

<!-- vinculum:begin block-attrs client redis_stream consumer level=5 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `group` | string | yes |  | Consumer group to join. |
| `stream` | expression | yes |  | Stream to consume from. |
| `ack` | string |  | `auto` | When a received message is settled with the broker. |
| `action` | expression (action-expression) |  |  | Expression evaluated once per message. |
| `batch_size` | number |  | `10` | Maximum entries to read at once. |
| `block_timeout` | expression (duration) |  | `2s` | How long to wait for new entries before polling again. |
| `consumer_name` | expression |  |  | Name identifying this consumer within the group. |
| `content_type_field` | string |  | `datacontenttype` | Stream field carrying the payload's content type. |
| `dead_letter_after` | number |  |  | Delivery attempts before an entry is dead-lettered. |
| `dead_letter_stream` | string |  |  | Stream to move entries to once they have failed too often. |
| `fields_mode` | string |  | `flat` | How the rest of the entry's fields map to message fields. |
| `group_create` | string |  | `create_if_missing` | Where a newly created group starts reading from. |
| `on_decode_error` | expression (action-expression) |  |  | Evaluated when an inbound message cannot be decoded. |
| `partition_key` | expression |  |  | Expression deciding which messages must stay in order. |
| `partitions` | number |  | `1` | Number of messages that may be processed at once. |
| `payload_field` | string |  | `data` | Stream field carrying the message payload. |
| `queue_size` | number |  |  | Depth of an async queue wrapping the subscriber. |
| `reclaim_min_idle` | expression (duration) |  | `5m` | How long an entry must be idle before it can be reclaimed. |
| `reclaim_pending` | bool |  | `true` | Take over entries another consumer read but never acknowledged. |
| `settle_timeout` | expression (duration) |  |  | How long a message may go unsettled before it is nacked automatically. |
| `subscriber` | expression (subscriber-ref) |  |  | Subscriber to forward messages to, instead of evaluating an action. |
| `topic_field` | string |  | `topic` | Stream field carrying the bus topic. |
| `transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied before the action or subscriber. |
| `vinculum_topic` | expression (topic-pattern) |  |  | Bus topic to publish arriving entries to. |

- Specify at most one of action or subscriber.
- Specify either an action to evaluate or a subscriber to forward to.
- partitions runs one queue per partition, so it needs queue_size to say how deep each is.
- partition_key decides which partition a message goes to, which means nothing without partitions.

**`group`**

Redis distributes a stream's entries across the members of a group.

**`ack`**

`auto` issues `XACK` when the work finishes: the delivery travels on `ctx`, so the acknowledgement follows the entry through a `queue_size` queue, a bus, and any number of hops rather than firing when delivery returns. `manual` leaves the entry in the group's pending list until the configuration calls `inbound::ack()`, and requires `settle_timeout`. A nacked entry stays pending for `reclaim_min_idle` and `dead_letter_after` to act on, and its reason reaches the log only.

One of: `auto`, `manual`.

**`action`**

`ctx.topic` is the vinculum topic and `ctx.msg` the entry's payload. `ctx.fields` carries the entry's own fields as `fields_mode` maps them, plus `$entry_id`, the entry's Redis ID — useful for logging and correlation, and not needed to acknowledge anything. Under `ack = "manual"` the entry is settled with `inbound::ack()`, which reads the delivery from `ctx` and so works equally well from a `subscription` behind `subscriber`.

Evaluated against the `message` context.

**`consumer_name`**

Pending entries are tracked per consumer name, so a stable name lets a restarted process reclaim its own work.

**`fields_mode`**

`flat` puts each remaining stream field at the top level of the message's fields; `nested` groups them; `omit` discards them.

One of: `flat`, `nested`, `omit`.

**`group_create`**

`create_if_missing` creates the group at the end of the stream, so only new entries arrive; `create_from_start` replays everything already there; `require_existing` refuses to create one at all.

One of: `create_if_missing`, `require_existing`, `create_from_start`.

**`on_decode_error`**

The message is dropped rather than delivered. Use this to publish to a dead-letter destination or record the failure.

Evaluated against the `decode-error` context.

**`partition_key`**

Messages whose key is equal are processed in the order they arrived, by one goroutine; messages whose keys differ may be processed at once. Choose the key that names the thing order matters for — a device, an account, a conversation.

Defaults to the topic. `null` asks for no ordering at all, dealing messages round-robin across every partition, which is both faster and more evenly spread than a key contrived to vary.

It is evaluated on the goroutine that hands the message over — the receiver's poll loop, or the bus's dispatch — so its cost falls on the thing `queue_size` was protecting. A plain `ctx.fields.<name>` or `ctx.topic` costs nothing: it is read straight off the message, with no expression evaluated at all. Anything else is evaluated per message, and reading `ctx.msg` is the expensive case, since the payload is converted for this expression as well as for the work.

The key sees the message as it arrived, not as `transforms` will deliver it: a pipeline that rewrites the topic does so after the partition has been chosen.

Evaluated against the `partition-key` context.

**`partitions`**

Runs this many queues, each drained by its own goroutine, so that many messages are handled in parallel. Order is preserved within a partition and not across them, and `partition_key` decides which messages share one — so the key is where ordering is configured and this is only how much parallelism the rest may use.

**A message picks its partition by hashing its key, so partitions do nothing until the key varies.** The default key is the topic: on a receiver where every message arrives on the same topic, every message hashes to the same partition and nothing runs in parallel. Set `partition_key`, or `partition_key = null` if no ordering is required at all.

`queue_size` is per partition, so `queue_size = 500` with `partitions = 8` is up to 4000 messages buffered — reconcile that with whatever bounds in-flight messages on the source, such as a RabbitMQ `prefetch` or an SQS visibility timeout.

Work that runs in parallel must tolerate running in parallel. Two partitions evaluating `set(ctx, var.n, get(var.n) + 1)` lose updates, whatever the key.

**`queue_size`**

When set, delivery is handed to a background goroutine so slow work does not block the source. The queue is bounded, and what happens to a message that arrives when it is full depends on where it came from: one that arrived over a transport that acknowledges is nacked, so the broker redelivers it, and any other is dropped and counted. On a receiver this composes with `ack` rather than conflicting with it — the acknowledgement follows the message through the queue and arrives when the work finishes.

A graceful shutdown runs the queue out rather than exiting past it: see [Boot and shutdown](health.md#boot-and-shutdown).

**`reclaim_min_idle`**

Long enough that a slow consumer is not mistaken for a dead one.

**`reclaim_pending`**

This is what recovers work left behind by a crashed consumer.

**`settle_timeout`**

Required with `ack = "manual"`, where nothing settles the message until the configuration does. Optional with `ack = "auto"`, to bound a chain the configuration does not fully trust — the acknowledgement follows the work, so a handler that never finishes leaves the message outstanding. An unsettled message costs something for as long as it is outstanding: an SQS visibility window, a RabbitMQ prefetch slot, a Kafka partition's committable offset. On expiry the message is nacked and the failure is logged against the receiver.

**`subscriber`**

Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.

**`transforms`**

A list of transform functions applied in order to each message. Only transform functions are in scope here.

**`vinculum_topic`**

The stream name is used when omitted. Evaluated per entry, with the stream and the entry's Redis ID readable under the same names `on_decode_error` gives them. `ctx.msg` is the entry's `payload_field` and `ctx.fields` its remaining stream fields, as `fields_mode` maps them.

Evaluated against the `inbound-message` context.

##### Blocks

- `baggage` (optional) — Which inbound baggage keys to trust.

<!-- vinculum:end block-attrs client redis_stream consumer -->

**`vinculum_topic` expression context:**

<!-- vinculum:begin block-ctx client redis_stream consumer vinculum_topic level=5 -->

Fields readable as `ctx.<name>` (shape `inbound-message`):

| Field | Type | Description |
|---|---|:---|
| `ctx.msg` | dynamic | The message payload. |
| `ctx.fields` | object | String metadata attached to the message. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.stream` | string | Stream the entry was read from. *(added here)* |
| `ctx.entry_id` | string | Redis entry ID, e.g. `1700000000000-0`. *(added here)* |

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

<!-- vinculum:end block-ctx client redis_stream consumer vinculum_topic -->

> **Changed in 0.46.0.** The stream used to be offered here as `ctx.topic` and
> the entry ID as `ctx.message_id` — the same two values this consumer's
> `on_decode_error` has always called `ctx.stream` and `ctx.entry_id`. The two
> hooks now agree, and the old spellings are gone. `vinculum check` reports
> either one rather than letting it fail at runtime.

### Entry format

Vinculum-produced entries are legible to non-Vinculum consumers, and a
Vinculum consumer can read a stream written by other tools by naming the
payload field. The defaults match the CloudEvents-on-Redis-Streams
convention seen in the wild:

| Field | Contents |
| --- | --- |
| `data` | JSON payload (overridable via `payload_field`; empty string suppresses it) |
| `topic` | Vinculum origin topic (`topic_field`) |
| `datacontenttype` | `application/json` (`content_type_field`) |
| `traceparent` / `tracestate` | W3C trace context, if a span is active |

`fields_mode` controls where the vinculum `fields` map lands on the entry:

- `flat` (default): each entry in the map becomes a sibling field on the
  stream entry. Reserved names (the set above, plus `fields`) always win
  over user-supplied fields at runtime; a warning is logged at config
  time if a custom field name overlaps a reserved one.
- `nested`: the whole map is JSON-encoded into a single `fields` entry.
- `omit`: the map is dropped.

Consumers use the symmetric attributes to parse incoming entries, so a
`nested`-mode producer pairs naturally with a `nested`-mode consumer.

### Group creation

- `create_if_missing` (default) — `XGROUP CREATE ... MKSTREAM` starting
  from `$` (new entries only). `BUSYGROUP` errors are ignored.
- `require_existing` — fail at `Start` if the group does not exist.
- `create_from_start` — `XGROUP CREATE ... 0` to replay history.

### Manual ack: `ack = "manual"`

With `ack = "manual"`, entries stay in the group's pending list until the
configuration settles them with
[`inbound::ack()`](functions.md#acknowledging-an-inbound-message):

```hcl
consumer "in" {
  stream         = "events"
  group          = "workers"
  ack            = "manual"
  settle_timeout = "2m"

  action = [
    do_something(ctx, ctx.msg),
    inbound::ack(ctx),
  ]
}
```

The entry being acknowledged travels on `ctx`, not in `fields`, so the same
expression works from a `subscription` several bus hops downstream — which is
the point of routing through a bus in the first place:

```hcl
consumer "in" {
  stream         = "events"
  group          = "workers"
  subscriber     = bus.work
  ack            = "manual"
  settle_timeout = "2m"
}

subscription "handle" {
  target = bus.work
  topics = ["events"]
  action = [do_something(ctx, ctx.msg), inbound::ack(ctx)]
}
```

`settle_timeout` is required, and bounds how long an entry may go unsettled: on
expiry it is nacked and the failure is logged, naming the consumer. A nacked
entry stays pending, where `reclaim_min_idle` and `dead_letter_after` decide
what becomes of it; the reason reaches the log and nowhere else.

`inbound::keepalive(ctx)` re-claims the entry for this same consumer, resetting
its idle time — the lease Redis Streams has — for work that will outlast
`reclaim_min_idle`.

### System fields

The entry ID is not a field of the entry, so the consumer adds it to every
delivery under the `$` prefix reserved for system-generated names,
regardless of `fields_mode`:

| Vinculum field | Contents |
| --- | --- |
| `$entry_id` | Redis entry ID, e.g. `1700000000000-0`. |

It is not needed to acknowledge anything — a settle reads the delivery from
`ctx`. It earns its place by identifying the entry: time-ordered, unique, and
what `XPENDING` and every Redis console show, so it is worth having for
logging, correlation, and deduplication.

### Pending recovery

When `reclaim_pending = true` (default), at `Start` the consumer walks
its group's pending list via `XPENDING`, reclaims entries idle longer
than `reclaim_min_idle` via `XCLAIM`, and runs them through the delivery
path directly (plain `XREADGROUP >` will not redeliver already-claimed
entries).

### Dead-letter

Setting `dead_letter_stream` requires a positive `dead_letter_after`.
After a delivery failure, once the entry's `RetryCount` reaches that
threshold, the consumer `XADD`s it to the DLQ stream with two extra
fields — `_dlq_original_stream` and `_dlq_original_id` — and `XACK`s the
original.

### Trace context

The producer injects `traceparent`/`tracestate` (and `baggage`) as reserved
stream entry fields. The consumer extracts them and attaches the producer
context as a span **link** on a fresh-root consumer span (`trace.WithNewRoot` +
`trace.WithLinks`). This avoids marathon traces when a persistent entry
is consumed minutes or hours after production, while still letting a
trace UI navigate producer → consumer.

Inbound [baggage](baggage.md) is carried onto the action context but, as
untrusted input, is **stripped by default**; opt into trusting it per consumer
with a `baggage {}` block (`passthrough`/`allow`/`deny`). See
[Server-side trust filtering](baggage.md#server-side-trust-filtering).

---

## `client "redis_kv" "<name>"`

Exposes Redis string and hash operations through vinculum's generic
[`get()` / `set()` / `increment()`](functions.md) functions via the
`richcty.Gettable`/`Settable`/`Incrementable` interfaces. Composable with
every other vinculum type that implements those interfaces (variables,
metrics, etc.).

### Key/value attributes

<!-- vinculum:begin block-attrs client redis_kv level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `connection` | expression (client-ref) | yes |  | Redis connection to use. |
| `default_ttl` | expression (duration) |  |  | Expiry applied to keys written without an explicit TTL. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `hash_mode` | bool |  | `false` | Store values as fields of one hash rather than as separate keys. |
| `key_prefix` | string |  |  | Prefix prepended to every key. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

**`connection`**

A `client "redis"` block.

**`default_ttl`**

Keys do not expire when omitted.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`hash_mode`**

A two-part key selects the hash and the field within it. Redis expires a whole hash rather than a field, so `default_ttl` applies to the hash.

**`key_prefix`**

Keeps this client's keys in their own namespace.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

<!-- vinculum:end block-attrs client redis_kv -->

### String mode

```hcl
client "redis_kv" "cache" {
  connection  = client.myredis
  key_prefix  = "app:"
  default_ttl = "1h"
  wire_format = "auto"       # auto | auto_bytes | json | string | bytes  (default: auto)
  metrics     = server.metrics.main
}

# GET app:mykey  → decoded per wire_format
get(ctx, client.cache, "mykey")
get(ctx, client.cache, "mykey", "fallback")           # second arg is default

# SET app:mykey <value> [EX <seconds>]
set(ctx, client.cache, "mykey", {status = "ok"})
set(ctx, client.cache, "mykey", "value", "5m")        # explicit TTL
set(ctx, client.cache, "mykey", "value", 0)           # ttl=0 → PERSIST

# INCRBY app:page_views 1  (or INCRBYFLOAT for non-integer delta)
increment(ctx, client.cache, "page_views", 1)
```

### Wire format

| Mode | `set(v)` | `get()` |
| --- | --- | --- |
| `string` | Strings, bytes, numbers, bools to string form; objects/lists error. | Raw string. |
| `json` | Everything JSON-encoded. | Everything JSON-decoded; malformed JSON errors. |
| `bytes` | Same as `string`. | Raw bytes. |
| `auto` (default) | Strings and bytes verbatim; other cty types JSON-encoded. | Values whose first non-whitespace byte is `{`, `[`, `"`, digit, `-`, `t`, `f`, or `n` are attempted as JSON; anything else (and JSON with trailing data, e.g. `2026-04-14`) stays a string. |

`increment()` bypasses encoding — Redis `INCRBY`/`INCRBYFLOAT` require a
numeric string regardless.

Bytes pass through verbatim in every mode, matching the MQTT/Kafka
client convention.

### Hash mode

```hcl
client "redis_kv" "devices" {
  connection = client.myredis
  key_prefix = "dev:"
  hash_mode  = true
}

# HGET dev:abc123 last_seen
get(ctx, client.devices, "abc123", "last_seen")

# HSET dev:abc123 last_seen <value>
set(ctx, client.devices, "abc123", "last_seen", tostring(time::now()))

# HGETALL dev:abc123 → cty object with every field decoded
get(ctx, client.devices, "abc123")
```

A hash-mode block permanently switches to hash semantics — there is no
per-call toggle. Use separate `redis_kv` blocks with different
`key_prefix`es for different namespaces.

### Key deletion

For Redis KV, set a short
TTL via `set(ctx, c, k, v, "5s")` or an explicit `default_ttl` on the
block.

---

## Addressing summary

Every block surfaces a cty object shaped to match MQTT/Kafka conventions:

| Address | Value |
| --- | --- |
| `client.<base>` | The connection manager (passive; rarely used directly). |
| `client.<pubsub>.publisher.<p>` | A single named pub/sub publisher as a `bus.Subscriber`. |
| `client.<pubsub>.publishers` | Fan-out to all publishers on the pubsub client. |
| `client.<stream>.producer.<p>` | A single named stream producer. |
| `client.<stream>.producers` | Fan-out to all producers. |
| `client.<kv>` | The KV client capsule — pass to `get()`/`set()`/`increment()`. |

The wrapper for each messaging client is itself a `bus.Subscriber`, so
`client.rps.publishers` on a subscription target publishes every message
through every publisher on that client.

---

## Observability

All four block types accept a `metrics = ` expression, and
`redis_pubsub`/`redis_stream` additionally accept `tracing = ...`.

Pub/sub and streams emit OTel **messaging** semantic-convention
instruments; KV emits **database** semantic-convention instruments.
Vinculum extensions use the `vinculum.*` prefix. All instruments carry
`vinculum.client.name = <block name>` so multiple clients of the same type
appear as distinct series.

Messaging (pub/sub and stream):

| Metric | Instrument |
| --- | --- |
| `messaging.client.sent.messages` | Counter |
| `messaging.client.consumed.messages` | Counter |
| `messaging.client.operation.duration` | Histogram (publish / receive) |
| `messaging.process.duration` | Histogram (subscriber/consumer action) |
| `vinculum.messaging.errors` | Counter, labeled by `error.type` and operation |
| `vinculum.messaging.connected` | UpDownCounter |
| `vinculum.messaging.stream.pending` | UpDownCounter (stream consumer) |
| `vinculum.messaging.stream.reclaimed` | Counter (stream consumer) |
| `vinculum.messaging.stream.dead_lettered` | Counter (stream consumer) |

Database (KV):

| Metric | Instrument |
| --- | --- |
| `db.client.operation.count` | Counter |
| `db.client.operation.duration` | Histogram |
| `vinculum.db.cache.hits` / `vinculum.db.cache.misses` | Counter |
| `vinculum.db.errors` | Counter, labeled by `error.type` and `db.operation.name` |

---

## Complete example

```hcl
bus "main" {}

client "redis" "myredis" {
  address  = "localhost:6379"
  password = env.REDIS_PASSWORD
}

# Cache / session store.
client "redis_kv" "sessions" {
  connection  = client.myredis
  key_prefix  = "sess:"
  default_ttl = "30m"
}

# Pub/sub: bus traffic out, Redis alerts in.
client "redis_pubsub" "rps" {
  connection = client.myredis

  publisher "main" {}

  subscriber "in" {
    subscriber = bus.main
    channel_subscription { channel = "alerts" }
    channel_subscription { channel = "devices.*" }
  }
}

# Stream: durable event log.
client "redis_stream" "rs" {
  connection = client.myredis

  producer "out" {
    stream = "events"
    maxlen = 100000
  }

  consumer "in" {
    stream        = "events"
    group         = "workers"
    block_timeout = "2s"
    subscriber    = bus.main
  }
}

# Fan bus traffic on alerts/# into the Redis channel publisher.
subscription "alerts_to_redis" {
  target     = bus.main
  topics     = ["alerts/#"]
  subscriber = client.rps.publisher.main
}
```
