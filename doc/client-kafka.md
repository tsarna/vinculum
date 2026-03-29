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
  linger      = "5ms"      # max wait to fill a batch — default: 0 (immediate)
  max_records = 10000      # max buffered records before ProduceSync blocks

  # Connection timeouts
  dial_timeout     = "10s"   # default: 10s
  request_timeout  = "30s"   # default: 30s
  metadata_max_age = "300s"  # how often to refresh broker/partition metadata

  # Named sender blocks (zero or more)
  sender "main" { ... }

  # Named receiver blocks (zero or more)
  receiver "main" { ... }
}
```

### TLS

The `tls` sub-block configures transport security.

| Attribute | Type | Description |
|---|---|---|
| `enabled` | bool | Enable TLS. Required to be `true` for TLS to take effect. |
| `ca_cert` | string | Path to a PEM-encoded CA certificate file for verifying the broker. If omitted, the system CA pool is used. |
| `cert` | string | Path to a PEM-encoded client certificate (for mutual TLS). |
| `key` | string | Path to the private key corresponding to `cert`. |
| `insecure_skip_verify` | bool | Skip broker certificate verification. Not recommended outside of testing. Default: `false`. |

### SASL

The `sasl` sub-block configures authentication.

| Attribute | Type | Description |
|---|---|---|
| `mechanism` | string | One of `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`. |
| `username` | string | SASL username. |
| `password` | string | SASL password. Use `env.VAR_NAME` to avoid hardcoding credentials. |

### Sender delivery settings

These attributes are connection-level settings and apply to all senders
within the block. They correspond directly to franz-go client options.

| Attribute | Type | Default | Description |
|---|---|---|---|
| `acks` | string | `"all"` | `"all"` — wait for all in-sync replicas; `"leader"` — wait for partition leader only; `"none"` — fire and forget. |
| `compression` | string | `"none"` | Compression codec: `none`, `gzip`, `snappy`, `lz4`, or `zstd`. |
| `idempotent` | bool | `true` when `acks = "all"` | Enables idempotent producer, preventing duplicate records on retry. |
| `linger` | duration | `"0"` | Maximum time to wait before flushing a batch. Higher values increase throughput at the cost of latency. |
| `max_records` | number | unlimited | Maximum number of records buffered before `ProduceSync` blocks. |

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

### `produce_mode`

| Value | Behavior |
|---|---|
| `sync` (default) | Waits for broker acknowledgement before returning. Reliable; provides backpressure. |
| `async` | Returns immediately after queueing the record internally. Higher throughput; errors are logged rather than returned to the caller. |

### `topic "<pattern>"`

Each `topic` block maps a vinculum topic pattern to a Kafka topic and optional
record key. The pattern is the block label.

| Attribute | Type | Description |
|---|---|---|
| `kafka_topic` | string | Destination Kafka topic. |
| `key` | expression | Record key expression (evaluated per message). `null` means no key. |

**Key expression context** (all accessed via `ctx`):

| Variable | Description |
|---|---|
| `ctx.topic` | The incoming vinculum topic string |
| `ctx.msg` | The message payload |
| `ctx.fields` | Named segments captured from the topic pattern (e.g. `ctx.fields.deviceId`) |

Common key expressions: `ctx.fields.deviceId`, `ctx.msg.id`, `ctx.topic`, `null`.

### `default_topic_transform`

Applied when no `topic` block matches.

| Value | Behavior |
|---|---|
| `error` (default) | Return an error for unmatched topics. |
| `slash_to_dot` | Convert the vinculum topic to a Kafka topic by replacing `/` with `.` (`a/b/c` → `a.b.c`). |
| `ignore` | Silently discard the message. |

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

  start_offset = "stored"          # stored | earliest | latest — default: stored
  commit_mode  = "after_process"   # after_process | periodic | manual — default: after_process
  dlq_topic    = "vinculum.dlq"    # optional: dead-letter queue topic

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

### `subscriber` / `action`

Exactly one must be specified.

- `subscriber` — forward each received message to a bus or subscriber (e.g. `bus.main`).
- `action` — evaluate an HCL expression for each message. See context variables below.

#### Action context variables

When `action` is used, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.topic` | Vinculum topic of the received message |
| `ctx.msg` | Message payload |
| `ctx.fields` | Map of string metadata fields from Kafka record headers (only present if headers exist) |

### `start_offset`

Controls which offset to start from when no committed offset exists for a
partition.

| Value | Behavior |
|---|---|
| `stored` (default) | Resume from the last committed offset. Correct for production use. |
| `earliest` | Read from the beginning of the topic. Useful for initial bootstrap; will reprocess all historical messages if the group offset is reset. |
| `latest` | Skip existing messages; only receive new ones after the consumer starts. |

### `commit_mode`

| Value | Behavior |
|---|---|
| `after_process` (default) | Commit the offset after `subscriber.OnEvent` returns successfully. At-least-once delivery guarantee. Strongly recommended. |
| `periodic` | Auto-commit on a time interval (franz-go default behavior). Risk of duplicate or lost messages on crash. |
| `manual` | Not committed automatically; reserved for future transactional use. |

### `dlq_topic`

Optional. If set, records that fail processing (i.e. `subscriber.OnEvent` returns
an error) are forwarded to this Kafka topic instead of being dropped. The DLQ
record preserves the original key and value, and adds the following headers:

| Header | Contents |
|---|---|
| `vinculum-error` | The error message from the failed handler |
| `vinculum-original-topic` | The original Kafka topic the record came from |
| `vinculum-timestamp` | ISO 8601 timestamp of when the failure occurred |

The offset is committed only after a successful DLQ send. If the DLQ send
itself fails, the record is not committed and will be redelivered.

### `subscription "<kafka-topic>"`

Each `subscription` block maps one Kafka topic to a vinculum topic. The Kafka
topic is the block label. Multiple blocks may be declared within a single
receiver.

| Attribute | Type | Description |
|---|---|---|
| `vinculum_topic` | expression | Vinculum topic to publish the message under. May be a static string or an HCL string interpolation. |

**`vinculum_topic` expression context** (all accessed via `ctx`):

| Variable | Description |
|---|---|
| `ctx.kafka_topic` | The source Kafka topic name |
| `ctx.key` | The record key as a string, or `null` if the record has no key |
| `ctx.fields` | `map(string)` populated from Kafka record headers |
| `ctx.msg` | The deserialized message payload |

**Deserialization:** The Kafka record value is parsed as JSON if possible
(producing `map`, `list`, or scalar values). If the bytes are not valid JSON,
the raw `[]byte` is passed through unchanged. Kafka record headers become the
`fields` map.

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
  action = loginfo("event", {topic = ctx.topic, msg = ctx.msg})
}
```

Receiver with `action` instead of `subscriber` — log each Kafka message directly:

```hcl
client "kafka" "events" {
  brokers = ["kafka:9092"]

  receiver "logger" {
    group_id = "vinculum-logger"
    action   = loginfo("kafka", {topic = ctx.topic, msg = ctx.msg})

    subscription "sensor.readings" {
      vinculum_topic = "sensor/readings"
    }
  }
}
```
