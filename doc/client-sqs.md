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

Holds AWS credentials and region configuration. Multiple SQS (and future AWS
service) clients can reference the same `client "aws"` block.

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

| Attribute | Type | Description |
|---|---|---|
| `region` | string | **Required.** AWS region. |
| `access_key_id` | expression | Static access key ID. Use `env.*` references. |
| `secret_access_key` | expression | Static secret access key. Use `env.*` references. |
| `session_token` | expression | Session token for temporary credentials. |
| `role_arn` | string | ARN of an IAM role to assume via STS. |
| `external_id` | string | External ID for cross-account role assumption. |
| `endpoint` | expression | Custom endpoint URL (LocalStack, ElasticMQ, VPC endpoints). |
| `profile` | string | AWS profile name from `~/.aws/config`. |

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

    # Batching (optional)
    # batch {
    #     enabled   = true
    #     max_size  = 10       # 1-10, default: 10
    #     max_delay = "100ms"  # default: 100ms
    # }

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

| Attribute | Type | Description |
|---|---|---|
| `aws` | expression | Reference to a `client "aws"` block. |
| `region` | string | AWS region (ignored if `aws` is set). |
| `queue_url` | expression | **Required.** Full SQS queue URL. |
| `delay_seconds` | int | Message delivery delay in seconds (0--900). Default: 0. |
| `topic_attribute` | string | If set, includes the vinculum topic as a message attribute with this name. |
| `message_group_id` | expression | **Required for FIFO queues.** Per-message group ID expression. |
| `deduplication_id` | expression | Per-message deduplication ID expression (FIFO queues). |
| `wire_format` | expression | Wire format for serialization. Default: `"auto"`. |
| `metrics` | expression | Metrics backend reference. |
| `tracing` | expression | Tracing backend reference. |

### Batch sub-block

| Attribute | Type | Description |
|---|---|---|
| `enabled` | bool | Enable batching. Default: `true` (if block present). |
| `max_size` | int | Maximum messages per batch (1--10). Default: 10. |
| `max_delay` | duration | Maximum wait time before flushing a partial batch. Default: `"100ms"`. |

When batching is enabled, `OnEvent` buffers messages and a background goroutine
sends them via `SendMessageBatch`. Each `OnEvent` call blocks until the batch
containing its message has been sent. Partial batch failures route errors back
to the individual callers.

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
    # action       = log_info(ctx, "sqs msg", {topic = ctx.topic, body = ctx.msg})

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

| Attribute | Type | Description |
|---|---|---|
| `aws` | expression | Reference to a `client "aws"` block. |
| `region` | string | AWS region (ignored if `aws` is set). |
| `queue_url` | expression | **Required.** Full SQS queue URL. |
| `subscriber` | expression | Vinculum subscriber to receive messages. Exactly one of `subscriber` or `action`. |
| `action` | expression | Inline action expression. Exactly one of `subscriber` or `action`. |
| `vinculum_topic` | expression | Vinculum topic for dispatched messages. Default: queue name from URL. |
| `wait_time` | duration | Long-poll wait time (0--20s). Default: `"20s"`. |
| `max_messages` | int | Messages per poll (1--10). Default: 10. |
| `visibility_timeout` | duration | Override queue's default visibility timeout. |
| `auto_delete` | bool | Delete messages after successful delivery. Default: `true`. |
| `concurrency` | int | Number of concurrent polling goroutines. Default: 1. |
| `wire_format` | expression | Wire format for deserialization. Default: `"auto"`. |
| `metrics` | expression | Metrics backend reference. |
| `tracing` | expression | Tracing backend reference. |

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
`sqs_delete()` function. The `sqs_extend_visibility()` function can extend
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
        sqs_delete(ctx, client.tasks, ctx.fields["$receipt_handle"]),
    ]
}
```

See [functions.md](functions.md) for `sqs_delete()` and
`sqs_extend_visibility()` documentation.

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
| `messaging.batch.message_count` | Float64Histogram | `{message}` | Messages per batch |

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

    batch {
        enabled   = true
        max_size  = 10
        max_delay = "200ms"
    }
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
