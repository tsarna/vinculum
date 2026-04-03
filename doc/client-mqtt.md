# MQTT Client (`client "mqtt"`)

Vinculum can publish messages to and subscribe to messages from MQTT brokers
using `client "mqtt"` blocks. The implementation uses
[paho.golang](https://github.com/eclipse/paho.golang) with its `autopaho`
sub-package for automatic reconnection, and supports MQTT 5.0 features
including user properties, shared subscriptions, and Last Will and Testament.

A single `client "mqtt"` block may contain any number of named `sender` and
`receiver` sub-blocks (at least one total). All senders and receivers within
a block share the same MQTT connection.

---

## `client "mqtt" "<name>"`

```hcl
client "mqtt" "iot" {
  # Broker URLs — list multiple for failover (autopaho tries them in order).
  # Schemes: mqtt:// (plain TCP), mqtts:// (TLS), ws:// (WebSocket), wss:// (WebSocket+TLS)
  brokers = ["mqtts://broker.example.com:8883"]

  # MQTT client identifier. Must be unique per active connection.
  # Default: "vinculum-<block-name>-<hostname>"
  client_id = "vinculum-iot-${sys.hostname}"

  # Connection parameters
  keep_alive               = "30s"    # PINGREQ interval (default: 30s)
  clean_start              = false    # false = restore session on reconnect (default: false)
  session_expiry_interval  = "3600s"  # how long broker retains session after disconnect (default: 0)

  # Optional TLS
  tls {
    enabled              = true
    ca_cert              = "/etc/certs/ca.crt"
    cert                 = "/etc/certs/client.crt"  # optional, for mTLS
    key                  = "/etc/certs/client.key"  # optional, for mTLS
    insecure_skip_verify = false                     # default: false
  }

  # Optional credentials
  auth {
    username = "vinculum"
    password = env.MQTT_PASSWORD
  }

  # Optional reconnect backoff
  reconnect {
    initial_delay  = "1s"
    max_delay      = "60s"
    backoff_factor = 2.0
  }

  # Optional Last Will and Testament
  will {
    topic   = "vinculum/${sys.hostname}/status"
    payload = jsonencode({status = "offline"})
    qos     = 1
    retain  = true
  }

  # Lifecycle hooks (evaluated synchronously; keep them fast)
  on_connect    = send(ctx, bus.main, "mqtt/connected",    {client = "iot"})
  on_disconnect = send(ctx, bus.main, "mqtt/disconnected", {client = "iot"})

  # Named sender blocks (zero or more)
  sender "main" { ... }

  # Named receiver blocks (zero or more)
  receiver "main" { ... }
}
```

### Connection attributes

| Attribute | Type | Default | Description |
|---|---|---|---|
| `brokers` | `list(string)` | required | Broker URLs. |
| `client_id` | expression | `"vinculum-<name>-<hostname>"` | MQTT client identifier. Must be unique per active connection. |
| `keep_alive` | duration | `"30s"` | How often to send a PINGREQ to the broker. |
| `clean_start` | bool | `false` | Request a clean session on the initial connection. |
| `session_expiry_interval` | duration | `0` | How long the broker retains session state after disconnect. `0` means the session ends when the connection closes. |

### `tls`

The `tls` sub-block configures transport security. See also [TLS configuration](config.md#tls).

| Attribute | Type | Description |
|---|---|---|
| `enabled` | bool | Enable TLS. Must be `true` for TLS to take effect. |
| `ca_cert` | string | Path to a PEM-encoded CA certificate for verifying the broker. If omitted, the system CA pool is used. |
| `cert` | string | Path to a PEM-encoded client certificate (for mutual TLS). |
| `key` | string | Path to the private key corresponding to `cert`. |
| `insecure_skip_verify` | bool | Skip broker certificate verification. Not recommended outside of testing. Default: `false`. |

### `auth`

| Attribute | Type | Description |
|---|---|---|
| `username` | string | MQTT username. |
| `password` | expression | MQTT password. Use `env.VAR_NAME` to avoid hardcoding credentials. |

### `reconnect`

Controls how autopaho backs off between reconnection attempts. If omitted,
autopaho uses its own default constant backoff.

| Attribute | Type | Default | Description |
|---|---|---|---|
| `initial_delay` | duration | `"1s"` | Delay before the first reconnect attempt. |
| `max_delay` | duration | `"60s"` | Maximum delay between reconnect attempts. |
| `backoff_factor` | number | `2.0` | Exponential multiplier applied to the delay on each attempt. |

### `will`

Last Will and Testament: the broker publishes this message when the client
disconnects unexpectedly (network failure, process kill). A graceful
`Stop()` suppresses the will — the broker discards it on a clean DISCONNECT.

| Attribute | Type | Description |
|---|---|---|
| `topic` | expression | MQTT topic to publish the will on. Evaluated once at config time. |
| `payload` | expression | Message payload (string). Evaluated once at config time. |
| `qos` | number | QoS for the will message (0 or 1). Default: `0`. |
| `retain` | bool | Whether the broker retains the will. Default: `false`. |

### `on_connect` / `on_disconnect`

Optional HCL expressions evaluated synchronously:

- `on_connect` — fires in `OnConnectionUp`, after each connection or
  reconnection, after subscriptions are registered.
- `on_disconnect` — fires in `OnConnectionDown`, when the connection drops,
  before any reconnection attempt.

Standard VCL context (`ctx`, `bus.*`, `send()`, `loginfo()`, etc.) is
available. Message variables (`ctx.topic`, `ctx.msg`, `ctx.fields`) are not
available — there is no message in flight at lifecycle hook time.

---

## `sender "<name>"`

Each `sender` sub-block creates a named MQTT sender. Senders are addressed
in `subscription` blocks via `client.<name>.sender.<name>` (single sender)
or `client.<name>.senders` (fan-out to all senders).

```hcl
sender "main" {
  qos    = 1      # default QoS for all publishes (0 or 1; default: 1)
  retain = false  # default retain flag (default: false)

  # Topic mappings — evaluated in order, first match wins.
  topic "alerts/#" {
    qos    = 1
    retain = true   # retain the last alert for new subscribers
    # mqtt_topic omitted: use vinculum topic verbatim
  }
  topic "sensor/+deviceId/reading" {
    mqtt_topic = "sensors/${ctx.fields.deviceId}/data"  # HCL expression
    qos        = 1
    retain     = false
  }

  # What to do when no topic matches:
  #   verbatim — publish to vinculum topic verbatim at sender-level QoS/retain (default)
  #   error    — return an error from OnEvent
  #   ignore   — silently drop the message
  default_topic_transform = "verbatim"
}
```

### `topic "<pattern>"`

Each `topic` block maps a vinculum topic pattern to MQTT delivery settings.
The pattern is the block label. The primary purpose is to set per-pattern QoS
and retain flags; `mqtt_topic` is optional for cases where the MQTT topic
should differ from the vinculum topic.

| Attribute | Type | Description |
|---|---|---|
| `mqtt_topic` | expression | MQTT topic to publish to. If omitted, the vinculum topic is used verbatim. |
| `qos` | number | QoS for messages matching this pattern (0 or 1). Overrides the sender-level default. |
| `retain` | bool | Retain flag for messages matching this pattern. Overrides the sender-level default. |

**`mqtt_topic` expression context:**

| Variable | Description |
|---|---|
| `ctx.topic` | The vinculum topic string |
| `ctx.msg` | The message payload |
| `ctx.fields` | Named segments captured from the pattern (e.g. `ctx.fields.deviceId`) |

### `default_topic_transform`

Applied when no `topic` block matches.

| Value | Behavior |
|---|---|
| `verbatim` (default) | Publish to the vinculum topic as-is at sender-level QoS/retain. |
| `error` | Return an error from `OnEvent`. |
| `ignore` | Silently discard the message. |

### Message serialization

| Payload type | Wire encoding |
|---|---|
| `cty.Value` | Converted via `go2cty2go`, then `json.Marshal` |
| `[]byte` | Passed through unchanged |
| other Go value | `json.Marshal` |

vinculum `fields` are encoded as MQTT 5 user properties (one property per key).

---

## `receiver "<name>"`

Each `receiver` sub-block creates an MQTT subscription that receives messages
from the broker and dispatches them to a vinculum bus or subscriber.

```hcl
receiver "main" {
  subscriber = bus.main        # forward to a bus or subscriber
  # OR
  # action = loginfo(ctx, "mqtt", {topic = ctx.topic, msg = ctx.msg})

  qos              = 1         # default QoS for subscriptions in this block (default: 0)
  handle_retained  = true      # deliver retained messages (default: true)
  shared_group     = ""        # MQTT 5 shared subscription group name

  subscription "sensors/+deviceId/data" {
    vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"  # HCL expression
    qos            = 1    # overrides receiver-level qos for this subscription
  }
  subscription "alerts/#" {
    vinculum_topic = "alerts/mqtt"
  }
}
```

### `subscriber` / `action`

Exactly one must be specified.

- `subscriber` — forward each received message to a bus or subscriber (e.g. `bus.main`).
- `action` — evaluate an HCL expression for each message.

#### Action context variables

| Variable | Description |
|---|---|
| `ctx.topic` | Vinculum topic of the received message |
| `ctx.msg` | Deserialized message payload |
| `ctx.fields` | `map(string)` from MQTT 5 user properties and extracted pattern fields |

### `handle_retained`

When `false`, retained messages delivered by the broker at subscribe time are
silently dropped. When `true` (default), retained messages are delivered and
`fields["$retained"] = "true"` is added to distinguish them from live messages.

### `shared_group`

When set, each `topic_subscription` is registered as
`$share/<group>/<mqtt_topic>`. The broker load-balances delivery across all
clients in the group — only one instance receives each message. This is the
MQTT equivalent of Kafka consumer groups and the correct pattern for
horizontally scaling vinculum.

### `subscription "<mqtt-topic>"`

Each `subscription` block subscribes to one MQTT topic pattern. The MQTT
topic (or pattern) is the block label.

| Attribute | Type | Description |
|---|---|---|
| `vinculum_topic` | expression | Vinculum topic for dispatching the message. Default: use the MQTT topic verbatim. |
| `qos` | number | QoS for this subscription (0 or 1). Overrides the receiver-level default. |

**`vinculum_topic` expression context:**

| Variable | Description |
|---|---|
| `ctx.topic` | The incoming MQTT topic string |
| `ctx.msg` | The deserialized message payload |
| `ctx.fields` | `map(string)` from MQTT 5 user properties and topic pattern field extraction |

**Named wildcard field extraction:** `+deviceId` in the subscription label
extracts the matched segment into `fields["deviceId"]`. The broker subscription
uses the plain `+` wildcard; extraction happens locally.

**Message deserialization:**

| Payload | Vinculum `msg` type |
|---|---|
| Valid JSON | `any` (`map[string]any`, `[]any`, scalar) |
| Non-JSON bytes | `[]byte` |

MQTT 5 user properties become `fields["key"] = "value"` (last value wins for
duplicate keys).

---

## Addressing senders in subscriptions

`client.<name>` resolves to a cty object with two attributes:

| Expression | Meaning |
|---|---|
| `client.<name>.senders` | Fan-out: dispatch `OnEvent` to **all** named senders. |
| `client.<name>.sender.<name>` | Route to a single named sender. |

```hcl
# Fan-out to all senders
subscription "all_to_mqtt" {
  target     = bus.main
  topics     = ["sensor/#", "alerts/#"]
  subscriber = client.iot.senders
}

# Single named sender
subscription "alerts_to_mqtt" {
  target     = bus.main
  topics     = ["alerts/#"]
  subscriber = client.iot.sender.main
}
```

---

## Distributed Tracing

Add a `tracing` attribute to enable W3C TraceContext propagation over MQTT 5
user properties. The subscriber extracts incoming trace properties and creates
a child span; the publisher injects trace context into outgoing user properties.

```hcl
client "mqtt" "iot" {
    tracing = client.tracer   # optional; auto-wired to the default client "otlp"
    ...
}
```

If there is exactly one `client "otlp"` block (or one marked `default = true`),
the MQTT client auto-wires to it when `tracing =` is omitted.

**Subscriber:** for each message received, a new root `SpanKindConsumer` span is
created and linked to the producer span (extracted from the `traceparent` user
property). This follows the [OTel messaging semantic conventions](https://opentelemetry.io/docs/specs/otel/trace/semantic_conventions/messaging/)
recommendation for async pub/sub: the consumer trace is independent but linked,
so the async boundary is correctly represented. Spans carry `messaging.system`,
`messaging.destination.name`, `messaging.operation.type`, and
`messaging.operation.name` attributes.

**Publisher:** the current trace context is injected into outgoing MQTT 5 user
properties as `traceparent` / `tracestate`, and a `SpanKindProducer` span wraps
the broker publish call.

**Property filtering:** W3C trace user properties (`traceparent`, `tracestate`,
`baggage`) are stripped from the `fields` map visible in VCL action expressions
so business metadata stays clean.

See [client "otlp"](client-otlp.md) for tracing configuration and auto-wiring rules.

---

## Observability

When a [`server "metrics"`](server-metrics.md) block is present, the MQTT
client exposes connection, sender, and receiver metrics.

```hcl
client "mqtt" "iot" {
  metrics = server.mymetrics   # optional; uses default server if omitted
  ...
}
```

### Connection metrics

| Metric | Type | Description |
|---|---|---|
| `mqtt_client_connected` | gauge | `1` when connected, `0` when not. |
| `mqtt_client_reconnects_total` | counter | Total reconnection events since start. |

### Sender metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `mqtt_publisher_messages_sent_total` | counter | `mqtt_topic` | Messages successfully published. |
| `mqtt_publisher_errors_total` | counter | `mqtt_topic` | Publish errors. |
| `mqtt_publisher_publish_duration_seconds` | histogram | `mqtt_topic` | Round-trip time for QoS 1 PUBACK. |

### Receiver metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `mqtt_subscriber_messages_received_total` | counter | `mqtt_topic` | Messages received and dispatched. |
| `mqtt_subscriber_errors_total` | counter | `mqtt_topic` | Processing errors. |
| `mqtt_subscriber_process_duration_seconds` | histogram | `mqtt_topic` | Time for `subscriber.OnEvent` to return. |

---

## Pitfalls

**Client ID uniqueness.** Two connections with the same `client_id` cause the
broker to disconnect the older one. When running multiple vinculum instances,
ensure each has a unique client ID (e.g. using `sys.hostname`).

**Retained message burst on reconnect.** Every reconnect re-subscribes, and
the broker re-delivers the last retained message for every matching topic. Use
`handle_retained = false` if retained messages are not needed, or filter with
`inbound_transforms` or subscription-level transforms.

**`$SYS` broker topics.** Many brokers publish diagnostics under `$SYS/`.
Subscriptions to `#` receive them. Filter with `drop_topic_pattern("$SYS/#")`
in `subscription` block transforms.

**Will is not sent on graceful disconnect.** This is correct MQTT behavior.
Publish a goodbye message explicitly in `on_disconnect` if needed.

**QoS mismatch.** MQTT delivers at the lower of publisher and subscriber QoS.
Ensure consistency across your pipeline.

**Multiple broker URLs are for failover, not clustering.** autopaho tries URLs
in order. MQTT brokers do not form a transparent cluster the way Kafka does.

---

## Complete example

```hcl
bus "main" {}

client "mqtt" "iot" {
  brokers   = ["mqtts://mqtt.example.com:8883"]
  client_id = "vinculum-iot-${sys.hostname}"

  tls {
    enabled = true
    ca_cert = "/etc/certs/ca.crt"
  }

  auth {
    username = "vinculum"
    password = env.MQTT_PASSWORD
  }

  reconnect {
    initial_delay  = "1s"
    max_delay      = "60s"
    backoff_factor = 2.0
  }

  will {
    topic   = "vinculum/${sys.hostname}/status"
    payload = jsonencode({status = "offline"})
    qos     = 1
    retain  = true
  }

  on_connect    = send(ctx, bus.main, "mqtt/status", {status = "online"})
  on_disconnect = send(ctx, bus.main, "mqtt/status", {status = "offline"})

  sender "out" {
    qos = 1
    default_topic_transform = "verbatim"

    topic "alerts/#" {
      qos    = 1
      retain = true
    }
  }

  receiver "in" {
    subscriber      = bus.main
    handle_retained = false
    shared_group    = "vinculum-prod"

    subscription "sensors/+deviceId/data" {
      vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"
      qos            = 1
    }
    subscription "alerts/#" {
      qos = 1
    }
  }
}

# Forward vinculum events to MQTT
subscription "to_mqtt" {
  target     = bus.main
  topics     = ["sensor/#", "alerts/#"]
  subscriber = client.iot.sender.out
}
```
