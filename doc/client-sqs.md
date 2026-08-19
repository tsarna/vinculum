# SQS Clients (`client "aws"`, `client "sqs_sender"`, `client "sqs_receiver"`)

Vinculum can send messages to and receive messages from AWS SQS queues using
`client "sqs_sender"` and `client "sqs_receiver"` blocks. Both share an
optional `client "aws"` block for credentials and region configuration.

The implementation uses the [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2).

SQS is fundamentally different from MQTT or Kafka: it is queue-based (not
pub/sub), pull-based (polling), and requires explicit message deletion. The
SQS clients act as a **bridge** between named queues and the vinculum bus,
with explicit topic assignment on both sides.

---

## `client "aws" "<name>"`

Holds AWS credentials and region configuration. Multiple AWS service clients
(SQS, [SNS](client-sns.md)) can reference the same `client "aws"` block.

```hcl
client "aws" "prod" {
    region = "us-east-1"

    # Credentials -- optional. If omitted, uses the default credential chain:
    # environment variables, shared credentials file, IAM instance profile,
    # ECS task role, IRSA (EKS), SSO.

    # Static credentials (not recommended for production):
    # access_key_id     = env.AWS_ACCESS_KEY_ID
    # secret_access_key = env.AWS_SECRET_ACCESS_KEY
    # session_token     = env.AWS_SESSION_TOKEN

    # Assume a role:
    # role_arn    = "arn:aws:iam::123456789012:role/vinculum-prod"
    # external_id = "vinculum"

    # Custom endpoint (for LocalStack, ElasticMQ, or VPC endpoints):
    # endpoint = "http://localhost:4566"

    # AWS profile from shared config (~/.aws/config):
    # profile = "production"
}
```

<!-- vinculum:begin block-attrs client aws level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `region` | string | yes | AWS region to operate in. |
| `access_key_id` | expression |  | Static access key ID. |
| `disabled` | bool |  | Skip this block entirely. |
| `endpoint` | expression (url) |  | Override the service endpoint URL. |
| `external_id` | string |  | External ID required by the assumed role's trust policy. |
| `profile` | string |  | Named profile to read from the shared AWS config file. |
| `role_arn` | string |  | Role to assume once the base credentials are resolved. |
| `secret_access_key` | expression |  | Static secret access key. |
| `session_token` | expression |  | Session token accompanying temporary credentials. |

- access_key_id and secret_access_key must be specified together.
- external_id requires role_arn.

**`region`**

For example `"us-east-1"`.

**`access_key_id`**

Prefer the default credential chain or a role; supply this from the environment if you must set it.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`endpoint`**

For a local stack such as LocalStack, or a VPC endpoint.

**`secret_access_key`**

Supply it from the environment rather than a literal.

<!-- vinculum:end block-attrs client aws -->

When no `client "aws"` block is referenced, the sender and receiver create
their own `aws.Config` using the default credential chain with only the
`region` attribute from their own block.

---

## `client "sqs_sender" "<name>"`

Receives vinculum bus events and sends them as SQS messages. Implements
`bus.Subscriber` so it can be used directly as a subscription target.

```hcl
client "sqs_sender" "orders" {
    aws       = client.prod          # optional; uses default credential chain if omitted
    region    = "us-east-1"          # ignored if aws is set
    queue_url = "https://sqs.us-east-1.amazonaws.com/123456789012/orders"

    # Include vinculum topic as a message attribute (optional)
    # topic_attribute = "source_topic"

    # Delay before message becomes visible (0-900 seconds; default: 0)
    # delay_seconds = 0

    # FIFO queue options (only for .fifo queues):
    # message_group_id = ctx.topic       # required for FIFO queues
    # deduplication_id = ctx.fields["$id"] # optional

    # Wire format for payload serialization (default: "auto")
    # wire_format = "json"

    # Metrics and tracing
    # metrics = server.metrics
    # tracing = client.otlp
}

subscription "orders_to_sqs" {
    target     = bus.main
    topics     = ["order/created", "order/updated"]
    subscriber = client.orders
}
```

<!-- vinculum:begin block-attrs client sqs_sender level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `queue_url` | expression (url) | yes |  | URL of the queue to send to. |
| `aws` | expression (client-ref) |  |  | Shared AWS configuration to use. |
| `deduplication_id` | expression |  |  | Deduplication ID for a FIFO queue or topic. |
| `delay_seconds` | number |  | `0` | Seconds to withhold each message from receivers. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `message_group_id` | expression |  |  | Group ID for a FIFO queue or topic. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `region` | string |  |  | AWS region to operate in. |
| `topic_attribute` | string |  |  | Message attribute carrying the bus topic. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

**`aws`**

A `client "aws"` block. Without one, the default AWS credential chain is used.

**`deduplication_id`**

AWS discards a repeat of the same ID within the deduplication window. Evaluated per message.

Evaluated against the `message` context.

**`delay_seconds`**

SQS caps it at 900, which is fifteen minutes.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`message_group_id`**

Messages sharing a group are delivered in order; different groups proceed independently. Required by a FIFO queue, and evaluated per message.

Evaluated against the `message` context.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`region`**

Overrides the region of the referenced `client "aws"` block.

**`topic_attribute`**

Lets a receiver recover the topic a message was published on.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

<!-- vinculum:end block-attrs client sqs_sender -->

`message_group_id` and `deduplication_id` are evaluated per outbound message
against this context:

<!-- vinculum:begin block-ctx client sqs_sender message_group_id level=3 -->

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

<!-- vinculum:end block-ctx client sqs_sender message_group_id -->

### Message format

- **Payload** is serialized via the configured wire format using `SerializeString`
  (SQS message bodies are strings).
- **Vinculum fields** become SQS `MessageAttribute`s with `DataType = "String"`.
  The `$` prefix used by vinculum internal fields is replaced with `_` (e.g.
  `$id` becomes `_id`).
- **Topic attribute** (if configured) is included as an additional message
  attribute.
- **Trace context** (W3C `traceparent`, `tracestate`, `baggage`) is injected
  into message attributes when tracing is active.

SQS allows at most 10 message attributes per message. Trace attributes (up to
3) take priority, followed by `topic_attribute` (if set), then user fields.
Excess fields are dropped with a warning log.

### FIFO queues

Queue URLs ending in `.fifo` are FIFO queues. The `message_group_id` attribute
is **required** for FIFO queues and controls ordering. Common patterns:

```hcl
message_group_id = "all"          # single group, strict order
message_group_id = ctx.topic      # per-topic ordering
message_group_id = ctx.msg.sku    # per-entity ordering
```

---

## `client "sqs_receiver" "<name>"`

Polls an SQS queue using long-polling and dispatches received messages to a
vinculum subscriber or action.

```hcl
client "sqs_receiver" "tasks" {
    aws            = client.prod
    queue_url      = "https://sqs.us-east-1.amazonaws.com/123456789012/tasks"

    # Destination -- exactly one of subscriber or action is required.
    subscriber     = bus.main
    # action       = log::info(ctx, "sqs msg", {topic = ctx.topic, body = ctx.msg})

    # Optional transform pipeline and async queue (same semantics as the
    # top-level `subscription` block — see config.md#subscription).
    # transforms = [ jq(".payload") ]
    # queue_size = 100

    # Optional; inbound baggage is stripped by default. See doc/baggage.md.
    # baggage { allow = ["tenant_id"] }

    # Vinculum topic for received messages (default: queue name from URL)
    vinculum_topic = "tasks/incoming"
    # Dynamic: vinculum_topic = "sqs/${ctx.fields[\"type\"]}"

    # Polling parameters
    wait_time    = "20s"      # long-poll wait (0-20s; default: 20s)
    max_messages = 10         # messages per poll (1-10; default: 10)

    # Visibility and deletion
    # visibility_timeout = "30s"  # override queue's default
    auto_delete = true            # delete after successful delivery (default: true)

    # Concurrency
    concurrency = 1           # polling goroutines (default: 1)

    # Wire format for deserialization (default: "auto")
    # wire_format = "json"

    # Metrics and tracing
    # metrics = server.metrics
    # tracing = client.otlp
}
```

The `baggage` block is a [baggage](baggage.md) trust filter. Inbound baggage is
**stripped by default** before it reaches the action; opt in with
`passthrough`/`allow`/`deny`. See
[Server-side trust filtering](baggage.md#server-side-trust-filtering).
`transforms` and `queue_size` behave as they do on a
[subscription](config.md#subscription).

<!-- vinculum:begin block-attrs client sqs_receiver level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `queue_url` | expression (url) | yes |  | URL of the queue to receive from. |
| `action` | expression (action-expression) |  |  | Expression evaluated once per message. |
| `auto_delete` | bool |  | `true` | Delete a message once it has been handled successfully. |
| `aws` | expression (client-ref) |  |  | Shared AWS configuration to use. |
| `concurrency` | number |  | `1` | Number of polling loops run in parallel. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `max_messages` | number |  | `10` | Maximum messages to fetch per poll. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `on_decode_error` | expression (action-expression) |  |  | Evaluated when an inbound message cannot be decoded. |
| `queue_size` | number |  |  | Depth of an async queue wrapping the subscriber. |
| `region` | string |  |  | AWS region to operate in. |
| `subscriber` | expression (subscriber-ref) |  |  | Subscriber to forward messages to, instead of evaluating an action. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `transforms` | expression (transform-pipeline) |  |  | Transform pipeline applied before the action or subscriber. |
| `vinculum_topic` | expression (topic-pattern) |  |  | Bus topic to publish arriving messages to. |
| `visibility_timeout` | expression (duration) |  |  | How long a received message stays hidden from other receivers. |
| `wait_time` | expression (duration) |  | `20s` | How long a poll waits for a message before returning empty. |
| `wire_format` | expression |  | `auto` | How to encode and decode message payloads. |

- Specify at most one of action or subscriber.
- Specify either an action to evaluate or a subscriber to forward to.

**`action`**

`ctx.topic` is the message topic and `ctx.msg` the payload; a protocol that extracts metadata also provides `ctx.fields`.

Evaluated against the `message` context.

**`auto_delete`**

A handler that returns an error leaves the message on the queue, so it reappears after the visibility timeout and is retried. Turn this off to take over deletion yourself with `sqs::delete()`, which is what lets a handler defer or abandon a message deliberately.

**`aws`**

A `client "aws"` block. Without one, the default AWS credential chain is used.

**`concurrency`**

Each polls independently, so this multiplies `max_messages` in flight.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`max_messages`**

SQS caps it at 10.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`on_decode_error`**

The message is dropped rather than delivered. Use this to publish to a dead-letter destination or record the failure.

Evaluated against the `decode-error` context.

**`queue_size`**

When set, decouples delivery from the action so a slow action does not block the source.

**`region`**

Overrides the region of the referenced `client "aws"` block.

**`subscriber`**

Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`transforms`**

A list of transform functions applied in order to each message. Only transform functions are in scope here.

**`vinculum_topic`**

The queue's name, taken from `queue_url`, is used when omitted. Evaluated per message.

Evaluated against the `sqs-message` context.

**`visibility_timeout`**

Must exceed the time handling takes, or the message is redelivered while still being processed. The queue's own setting applies when omitted.

**`wait_time`**

Long polling: a non-zero wait cuts both latency and request count. SQS caps it at 20 seconds.

**`wire_format`**

A `wire_format` block, or the name of a built-in format. Under `auto`, strings and bytes pass through and everything else is JSON-encoded; decoding auto-detects JSON and falls back to a string.

### Blocks

- `baggage` (optional) — Which inbound baggage keys to trust.

<!-- vinculum:end block-attrs client sqs_receiver -->

**Action context variables:**

<!-- vinculum:begin block-ctx client sqs_receiver action level=3 -->

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

<!-- vinculum:end block-ctx client sqs_receiver action -->

**`vinculum_topic` expression context:**

<!-- vinculum:begin block-ctx client sqs_receiver vinculum_topic level=3 -->

Fields readable as `ctx.<name>` (shape `sqs-message`):

| Field | Type | Description |
|---|---|:---|
| `ctx.message_id` | string | SQS message ID. |
| `ctx.msg` | dynamic | The message body. *(not always present)* |
| `ctx.fields` | object | String metadata attached to the message. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.msg`**

Absent when the message has no body, or when the body cannot be converted — the topic is chosen before the body is decoded, so this is the raw body rather than a `wire_format` result.

**`ctx.fields`**

Populated from the message's SQS attributes.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx client sqs_receiver vinculum_topic -->

### Decode failures

The configured `wire_format` is a **contract**. When an inbound body fails to
deserialize, the message is *not* delivered to the subscriber as a raw string
and is *not* deleted from the queue.

> **Configure a redrive policy.** Because the message is not deleted, it
> becomes visible again after the visibility timeout and is redelivered
> indefinitely. Attach an SQS
> [redrive policy](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html)
> so a persistently malformed message lands in a dead-letter queue instead of
> cycling forever. Vinculum cannot see the queue's redrive policy, so it cannot
> warn you about this at config time.

Set `on_decode_error` to observe the failure. The hook cannot suppress it;
errors inside the hook are logged and otherwise ignored.

```hcl
client "sqs_receiver" "in" {
  queue_url   = "https://sqs.us-east-1.amazonaws.com/123456789012/events"
  wire_format = "json"
  action      = send(ctx, bus.main, ctx.topic, ctx.msg)

  on_decode_error = log::error("bad body", {
    queue      = ctx.queue,
    message_id = ctx.message_id,
    error      = ctx.error,
    raw        = tostring(ctx.raw),
  })
}
```

**Hook context variables:**

<!-- vinculum:begin block-ctx client sqs_receiver on_decode_error level=3 -->

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
| `ctx.queue` | string | Queue the message was received from. *(added here)* |
| `ctx.message_id` | string | SQS message ID. *(added here)* *(not always present)* |

*This shape is open: a particular site may carry fields beyond these.*

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

**`ctx.message_id`**

Absent in the unusual case that SQS returned a message without one.

<!-- vinculum:end block-ctx client sqs_receiver on_decode_error -->

`ctx.topic` and `ctx.queue` carry the same string here: with no
`vinculum_topic` computed yet, the best-effort topic is the queue name.

Use `wire_format = "auto_bytes"` if you want best-effort decoding instead: it
decodes JSON like `auto` and yields a [`bytes`](functions.md) value for anything
it can't parse. `auto` behaves the same but yields a string — pick whichever
type your handler wants. Neither ever fails to decode.

> **Changed in 0.44.0.** Earlier releases logged a warning and delivered the
> raw string. See [deprecations](deprecations.md#tolerant-wire-format-decoding).

### System attributes

SQS system attributes are mapped to `$`-prefixed vinculum fields:

| SQS system attribute | Vinculum field | Notes |
|---|---|---|
| `MessageId` | `$message_id` | Unique SQS message ID |
| `ReceiptHandle` | `$receipt_handle` | Needed for manual delete |
| `ApproximateReceiveCount` | `$receive_count` | How many times delivered |
| `SentTimestamp` | `$sent_timestamp` | Epoch millis when sent |
| `ApproximateFirstReceiveTimestamp` | `$first_receive_timestamp` | Epoch millis |
| `MessageGroupId` (FIFO) | `$message_group_id` | FIFO group |
| `MessageDeduplicationId` (FIFO) | `$deduplication_id` | FIFO dedup |
| `SequenceNumber` (FIFO) | `$sequence_number` | FIFO sequence |

### Manual deletion

When `auto_delete = false`, messages must be explicitly deleted using the
`sqs::delete()` function. The `sqs::extend_visibility()` function can extend
the visibility timeout for long-running processing.

```hcl
client "sqs_receiver" "tasks" {
    auto_delete    = false
    subscriber     = bus.main
    vinculum_topic = "tasks/incoming"
    ...
}

subscription "process_tasks" {
    target = bus.main
    topics = ["tasks/incoming"]
    action = [
        do_something(ctx, ctx.msg),
        sqs::delete(ctx, client.tasks, ctx.fields["$receipt_handle"]),
    ]
}
```

See [functions.md](functions.md) for `sqs::delete()` and
`sqs::extend_visibility()` documentation.

### Dead-letter queues

DLQs are configured on the queue itself (AWS console or IaC), not in
vinculum. Use a separate `sqs_receiver` to consume from a DLQ:

```hcl
client "sqs_receiver" "tasks_dlq" {
    aws            = client.prod
    queue_url      = "https://sqs.us-east-1.amazonaws.com/123456789012/tasks-dlq"
    subscriber     = bus.main
    vinculum_topic = "tasks/dead_letter"
}
```

---

## Metrics

All SQS metrics carry attributes: `messaging.system=aws_sqs`,
`messaging.destination.name=<queue>`, `vinculum.client.name=<client>`.

### Sender metrics

| Metric | Type | Unit | Description |
|---|---|---|---|
| `messaging.client.sent.messages` | Int64Counter | `{message}` | Messages sent |
| `messaging.client.operation.duration` | Float64Histogram | `s` | Send API call latency |

### Receiver metrics

| Metric | Type | Unit | Description |
|---|---|---|---|
| `messaging.client.consumed.messages` | Int64Counter | `{message}` | Messages received and dispatched |
| `messaging.process.duration` | Float64Histogram | `s` | subscriber.OnEvent processing time |

---

## Complete example

```hcl
client "aws" "prod" {
    region = "us-east-1"
}

# Send order events to SQS for external consumers
client "sqs_sender" "order_events" {
    aws       = client.prod
    queue_url = "https://sqs.us-east-1.amazonaws.com/123456789012/order-events.fifo"

    message_group_id = ctx.msg.order_id
    deduplication_id = ctx.msg.event_id
}

subscription "orders_to_sqs" {
    target     = bus.main
    topics     = ["order/#"]
    subscriber = client.order_events
}

# Receive task assignments from another service
client "sqs_receiver" "task_queue" {
    aws            = client.prod
    queue_url      = "https://sqs.us-east-1.amazonaws.com/123456789012/task-assignments"
    subscriber     = bus.main
    vinculum_topic = "tasks/incoming"
    concurrency    = 3
}
```
