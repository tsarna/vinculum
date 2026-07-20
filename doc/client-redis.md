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

| Attribute | Type | Default | Notes |
| --- | --- | --- | --- |
| `mode` | string | `"standalone"` | One of `standalone`, `cluster`, `sentinel`. |
| `address` | string | — | Required for standalone; rejected in cluster/sentinel. |
| `addresses` | list(string) | — | Required for cluster/sentinel; rejected in standalone. |
| `master_name` | string | — | Required for sentinel; rejected otherwise. |
| `database` | number | `0` | Standalone/sentinel only. |
| `username` / `password` | string / expression | — | Redis 6+ ACL. Plain `requirepass` uses `password` alone. |
| `sentinel_username` / `sentinel_password` | string / expression | — | Separate credentials for sentinel nodes. |
| `tls` | block | — | See [TLS configuration](config.md#tls). |
| `pool_size` | number | go-redis default | Max connections per node. |
| `min_idle_conns` | number | `0` | Idle connections kept warm. |
| `dial_timeout` | duration | go-redis default | Connect timeout. |

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
    # action   = send(ctx, bus.main, "redis/${ctx.topic}", msg)

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

**Hook context variables** (pub/sub and stream both support
`on_decode_error`):

| Variable | Description |
|---|---|
| `ctx.raw` | The undecoded payload, as a [`bytes`](functions.md) object |
| `ctx.error` | The deserialize error message |
| `ctx.wire_format` | The configured format name |
| `ctx.topic` | The channel (pub/sub) or stream name |
| `ctx.fields` | *(pub/sub)* Fields extracted before the failure. Always empty for streams: fields are read in the same pass that decodes the payload, so a partial map would depend on Go's randomized map iteration order. |
| `ctx.channel` | *(pub/sub)* The Redis channel |
| `ctx.matched_pattern` | *(pub/sub)* The subscribed pattern, when the match was by pattern |
| `ctx.stream` | *(stream)* The stream name |
| `ctx.entry_id` | *(stream)* The entry ID |
| `ctx.group` | *(stream)* The consumer group |
| `ctx.consumer` | *(stream)* The consumer name |

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
    vinculum_topic = "stream/${ctx.topic}"  # optional remap

    # Exactly one of:
    subscriber = bus.main
    # action   = [ ..., redis::ack(ctx, client.rs.consumer.in, ctx.message_id) ]

    # Optional transform pipeline and async queue (same semantics as the
    # top-level `subscription` block — see config.md#subscription).
    # transforms = [ jq(".payload") ]
    # queue_size = 100

    # Optional; inbound baggage is stripped by default. See doc/baggage.md.
    # baggage { allow = ["tenant_id"] }

    batch_size     = 10
    block_timeout  = "2s"
    auto_ack       = true
    group_create   = "create_if_missing"  # create_if_missing | require_existing | create_from_start

    reclaim_pending   = true
    reclaim_min_idle  = "5m"

    # dead_letter_stream = "events:dlq"
    # dead_letter_after  = 3
  }
}
```

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

### Manual ack: `redis::ack()`

With `auto_ack = false`, entries stay pending until acknowledged. The
`redis::ack` function is registered globally:

```hcl
redis::ack(ctx, client.rs.consumer.in, ctx.message_id)
```

The entry ID is exposed on the action eval context as `ctx.message_id`
alongside `ctx.topic`, `ctx.msg`, and `ctx.fields`.

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
| `client.<stream>.consumer.<c>` | The consumer capsule — pass to `redis::ack()`. |
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
