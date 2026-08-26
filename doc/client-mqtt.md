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

  # Wire format for payload serialization/deserialization (default: "auto")
  # wire_format = "json"     # auto | auto_bytes | json | string | bytes

  # Named sender blocks (zero or more)
  sender "main" { ... }

  # Named receiver blocks (zero or more)
  receiver "main" { ... }
}
```

### Attributes

<!-- vinculum:begin block-attrs client mqtt level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `brokers` | list (url) | yes |  | Broker addresses to connect to. |
| `clean_start` | bool |  | `false` | Discard any session the broker holds for this client id. |
| `client_id` | expression |  | `vinculum-<name>-<hostname>` | MQTT client identifier presented to the broker. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `keep_alive` | expression (duration) |  | `30s` | Interval at which to send keep-alive pings. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `on_connect` | expression (action-expression) |  |  | Evaluated after the connection is established and ready. |
| `on_disconnect` | expression (action-expression) |  |  | Evaluated when the connection is lost or closed. |
| `readiness` | bool |  | `true` | Whether this component gates the process's readiness. |
| `session_expiry_interval` | expression (duration) |  | `0` | How long the broker keeps the session after disconnect. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

**`brokers`**

For example `["tcp://mqtt.example.com:1883"]`. Several addresses are tried in order.

**`clean_start`**

When false, the broker resumes the existing session and replays queued messages.

**`client_id`**

Must be unique per connection.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

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

**`session_expiry_interval`**

Zero means the session ends when the connection closes.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

#### Blocks

- `auth` (optional) — Credentials presented to the broker.
- `receiver "<name>"` (0..n) — Subscribes to MQTT topics and delivers what arrives.
- `reconnect` (optional) — How to retry a lost connection.
- `sender "<name>"` (0..n) — Publishes bus messages to MQTT topics.
- `tls` (optional) — TLS settings for this connection.
- `will` (optional) — Message the broker publishes if this client disconnects ungracefully.

<!-- vinculum:end block-attrs client mqtt -->

### `tls`

The `tls` sub-block configures transport security. See also [TLS configuration](config.md#tls).

<!-- vinculum:begin block-attrs client mqtt tls level=4 -->

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

<!-- vinculum:end block-attrs client mqtt tls -->

### `auth`

<!-- vinculum:begin block-attrs client mqtt auth level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `password` | expression |  | Password to authenticate with. |
| `username` | string |  | Username to authenticate with. |

**`password`**

Supply it from the environment rather than a literal.

<!-- vinculum:end block-attrs client mqtt auth -->

### `reconnect`

Controls how the client backs off between reconnection attempts. If omitted,
autopaho uses its own default constant backoff.

<!-- vinculum:begin block-attrs client mqtt reconnect level=4 -->

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

<!-- vinculum:end block-attrs client mqtt reconnect -->

### `will`

Last Will and Testament: the broker publishes this message when the client
disconnects unexpectedly (network failure, process kill). A graceful
`Stop()` suppresses the will — the broker discards it on a clean DISCONNECT.

Both `topic` and `payload` are evaluated once, at config load.

<!-- vinculum:begin block-attrs client mqtt will level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `payload` | expression | yes |  | Payload of the will message. |
| `topic` | expression (topic-pattern) | yes |  | MQTT topic to publish the will to. |
| `qos` | number |  | `0` | MQTT quality of service for the will. |
| `retain` | bool |  | `false` | Ask the broker to retain the will message. |

**`qos`**

`0` at most once, `1` at least once, `2` exactly once.

<!-- vinculum:end block-attrs client mqtt will -->

### `on_connect` / `on_disconnect`

Optional HCL expressions evaluated synchronously:

- `on_connect` — fires in `OnConnectionUp`, after each connection or
  reconnection, after subscriptions are registered.
- `on_disconnect` — fires in `OnConnectionDown`, when the connection drops,
  before any reconnection attempt.

Standard VCL context (`ctx`, `bus.*`, `send()`, `log::info()`, etc.) is
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

### Sender attributes

<!-- vinculum:begin block-attrs client mqtt sender level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `default_topic_transform` | string |  | `verbatim` | How to derive an MQTT topic from a bus topic with no `topic` block. |
| `qos` | number |  | `1` | Default quality of service for published messages. |
| `retain` | bool |  | `false` | Ask the broker to retain published messages. |

**`qos`**

`0` at most once, `1` at least once, `2` exactly once.

#### Blocks

- `topic "<pattern>"` (0..n) — Maps bus topics matching a pattern to an MQTT topic.

<!-- vinculum:end block-attrs client mqtt sender -->

### `topic "<pattern>"`

Each `topic` block maps a vinculum topic pattern to MQTT delivery settings.
The pattern is the block label. The primary purpose is to set per-pattern QoS
and retain flags; `mqtt_topic` is optional for cases where the MQTT topic
should differ from the vinculum topic.

<!-- vinculum:begin block-attrs client mqtt sender topic level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `mqtt_topic` | expression (topic-pattern) |  | MQTT topic to publish to. |
| `qos` | number |  | Quality of service for this mapping. |
| `retain` | bool |  | Retain flag for this mapping. |

**`mqtt_topic`**

The vinculum topic is used verbatim when omitted. Evaluated per message, so it can interpolate the fields the pattern captured.

Evaluated against the `message` context.

**`qos`**

Overrides the sender's default.

**`retain`**

Overrides the sender's default.

<!-- vinculum:end block-attrs client mqtt sender topic -->

**`mqtt_topic` expression context:**

<!-- vinculum:begin block-ctx client mqtt sender topic mqtt_topic level=4 -->

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

<!-- vinculum:end block-ctx client mqtt sender topic mqtt_topic -->

Named segments captured from the pattern arrive in `ctx.fields` — `+deviceId`
in the label becomes `ctx.fields.deviceId`.

### `default_topic_transform`

Applied when no `topic` block matches.

| Value | Behavior |
|---|---|
| `verbatim` (default) | Publish to the vinculum topic as-is at sender-level QoS/retain. |
| `error` | Return an error from `OnEvent`. |
| `ignore` | Silently discard the message. |

### Message serialization

Controlled by the client-level `wire_format` attribute (default `"auto"`):

| Wire format | Serialize | Deserialize |
|---|---|---|
| `auto` | Strings/bytes verbatim; everything else JSON-encoded | Auto-detects JSON; falls back to string |
| `json` | All values JSON-encoded; bytes pass through | Strict JSON; errors on malformed input |
| `string` | Strings, bytes, numbers, bools to string form | Returns string |
| `bytes` | Same as string | Returns bytes |

vinculum `fields` are encoded as MQTT 5 user properties (one property per key).

---

## `receiver "<name>"`

Each `receiver` sub-block creates an MQTT subscription that receives messages
from the broker and dispatches them to a vinculum bus or subscriber.

```hcl
receiver "main" {
  subscriber = bus.main        # forward to a bus or subscriber
  # OR
  # action = log::info(ctx, "mqtt", {topic = ctx.topic, msg = ctx.msg})

  # Optional transform pipeline and async queue (same semantics as the
  # top-level `subscription` block — see config.md#subscription).
  # transforms = [ jq(".payload") ]
  # queue_size = 100

  qos              = 1         # default QoS for subscriptions in this block (default: 0)
  handle_retained  = true      # deliver retained messages (default: true)
  shared_group     = ""        # MQTT 5 shared subscription group name

  # Optional; inbound baggage is stripped by default. See doc/baggage.md.
  baggage {
    allow = ["tenant_id"]
  }

  subscription "sensors/+deviceId/data" {
    vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"  # HCL expression
    qos            = 1    # overrides receiver-level qos for this subscription
  }
  subscription "alerts/#" {
    vinculum_topic = "alerts/mqtt"
  }
}
```

### Receiver attributes

<!-- vinculum:begin block-attrs client mqtt receiver level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) |  |  | Expression evaluated once per message. |
| `handle_retained` | bool |  | `true` | Deliver messages the broker retained before this subscription. |
| `on_decode_error` | expression (action-expression) |  |  | Evaluated when an inbound message cannot be decoded. |
| `qos` | number |  | `0` | Default quality of service to subscribe with. |
| `queue_size` | number |  |  | Depth of an async queue wrapping the subscriber. |
| `shared_group` | string |  |  | Shared subscription group name. |
| `subscriber` | expression (subscriber-ref) |  |  | Subscriber to forward messages to, instead of evaluating an action. |
| `transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied before the action or subscriber. |

- Specify at most one of action or subscriber.
- Specify either an action to evaluate or a subscriber to forward to.

**`action`**

`ctx.topic` is the message topic and `ctx.msg` the payload; a protocol that extracts metadata also provides `ctx.fields`.

Evaluated against the `message` context.

**`on_decode_error`**

The message is dropped rather than delivered. Use this to publish to a dead-letter destination or record the failure.

Evaluated against the `decode-error` context.

**`qos`**

`0` at most once, `1` at least once, `2` exactly once.

**`queue_size`**

When set, decouples delivery from the action so a slow action does not block the source.

**`shared_group`**

Instances in the same group share the topic's messages rather than each receiving all of them.

**`subscriber`**

Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.

**`transforms`**

A list of transform functions applied in order to each message. Only transform functions are in scope here.

#### Blocks

- `baggage` (optional) — Which inbound baggage keys to trust.
- `subscription "<mqtt_topic>"` (1..n) — One MQTT topic filter to subscribe to.

<!-- vinculum:end block-attrs client mqtt receiver -->

### `subscriber` / `action`

Exactly one must be specified.

- `subscriber` — forward each received message to a bus or subscriber (e.g. `bus.main`).
- `action` — evaluate an HCL expression for each message.

Optionally, `transforms = [...]` applies a transform pipeline to each message
before delivery, and `queue_size = N` wraps delivery in an async background
queue of depth `N` so slow handlers don't block the MQTT client's inbound
dispatch. Same semantics as the top-level [subscription](config.md#subscription)
block.

#### Action context variables

<!-- vinculum:begin block-ctx client mqtt receiver action level=5 -->

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

<!-- vinculum:end block-ctx client mqtt receiver action -->

`ctx.fields` carries the MQTT 5 user properties and the segments extracted from
the subscription pattern.

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

### `baggage`

Inbound MQTT message [baggage](baggage.md) (W3C key/value pairs carried in MQTT 5
user properties) is untrusted input, so each receiver **strips it by default**
before it reaches the action or downstream re-propagation. Add an optional
`baggage {}` block to opt into trusting upstream baggage — `passthrough = true`,
`allow = [...]`, or `deny = [...]`. See
[Server-side trust filtering](baggage.md#server-side-trust-filtering) for the
full attribute set. The block is per-receiver. (Trace continuity is unaffected;
only baggage key/value pairs are filtered.)

### `subscription "<mqtt-topic>"`

Each `subscription` block subscribes to one MQTT topic pattern. The MQTT
topic (or pattern) is the block label.

<!-- vinculum:begin block-attrs client mqtt receiver subscription level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `qos` | number |  | Quality of service for this subscription. |
| `vinculum_topic` | expression (topic-pattern) |  | Bus topic to publish arriving messages to. |

**`qos`**

Overrides the receiver's default.

**`vinculum_topic`**

The MQTT topic is used verbatim when omitted. Evaluated per message, so placeholders captured by the filter can be interpolated.

Evaluated against the `message` context.

<!-- vinculum:end block-attrs client mqtt receiver subscription -->

**`vinculum_topic` expression context:**

<!-- vinculum:begin block-ctx client mqtt receiver subscription vinculum_topic level=4 -->

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

<!-- vinculum:end block-ctx client mqtt receiver subscription vinculum_topic -->

Here `ctx.topic` is the incoming MQTT topic, and `ctx.fields` carries the MQTT 5
user properties along with the segments the pattern extracted.

**Named wildcard field extraction:** `+deviceId` in the subscription label
extracts the matched segment into `fields["deviceId"]`. The broker subscription
uses the plain `+` wildcard; extraction happens locally.

**Message deserialization** is controlled by the client-level `wire_format`
(default `"auto"`). In auto mode, JSON payloads are decoded; non-JSON becomes
a string. Use `"json"` for strict decoding, or `"string"`/`"bytes"` for raw
passthrough.

MQTT 5 user properties become `fields["key"] = "value"` (last value wins for
duplicate keys).

#### Decode failures

The configured `wire_format` is a **contract**. When an inbound payload fails
to deserialize, the message is dropped — it is *not* delivered to the subscriber as raw
bytes.

MQTT has no negative acknowledgement, so the message is simply not
delivered. Nothing accumulates and nothing is redelivered.

Set `on_decode_error` to observe the failure (log it, publish it to a
dead-letter topic, increment a counter). The hook cannot suppress the failure;
errors inside it are logged and otherwise ignored.

```hcl
receiver "in" {
  topic_subscription "sensors/+deviceId/reading" {}
  action = send(ctx, bus.main, ctx.topic, ctx.msg)

  on_decode_error = log::error("bad payload", {
    topic = ctx.topic,
    error = ctx.error,
    raw   = tostring(ctx.raw),
  })
}
```

**Hook context variables:**

<!-- vinculum:begin block-ctx client mqtt receiver on_decode_error level=5 -->

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
| `ctx.mqtt_topic` | string | The MQTT topic the message arrived on. *(added here)* |

*This shape is open: a particular site may carry fields beyond these.*

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

**`ctx.mqtt_topic`**

Equal to `ctx.topic` here: a vinculum topic derived from the payload cannot be computed once the payload has failed to decode, so `ctx.topic` falls back to this.

<!-- vinculum:end block-ctx client mqtt receiver on_decode_error -->

`ctx.topic` and `ctx.mqtt_topic` carry the same string here, because a vinculum
topic that depends on the payload cannot be computed once the payload has failed
to decode, so `ctx.topic` falls back to the MQTT topic.

> **Changed in 0.45.0.** This field was previously offered under the name
> `topic`, which collides with `ctx.topic` above; the collision was resolved in
> favour of the fixed field, so it was dropped and never reached a config at all.


Use `wire_format = "auto_bytes"` if you want best-effort decoding instead: it
decodes JSON like `auto` and yields a [`bytes`](functions.md) value for anything
it can't parse. `auto` behaves the same but yields a string — pick whichever
type your handler wants. Neither ever fails to decode.

> **Changed in 0.44.0.** Earlier releases logged a warning and delivered the
> raw bytes. See [deprecations](deprecations.md#tolerant-wire-format-decoding).


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
