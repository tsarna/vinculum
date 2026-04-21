# SNS Client (`client "sns_sender"`)

Vinculum can publish messages to AWS SNS topics using `client "sns_sender"`.
It reuses the shared `client "aws"` block for credentials and region
configuration (see [client-sqs.md](client-sqs.md#client-aws-name) for the
`client "aws"` reference).

The implementation uses the [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2).

SNS is a push-based pub/sub service that complements SQS:

- **Fan-out.** Publishing to an SNS topic delivers the message to all
  subscribers (SQS queues, Lambda functions, HTTP endpoints, email, SMS).
- **No receiver polling.** SNS pushes to subscribers. Reception of SNS
  messages is normally done via SQS (SNS → SQS subscription), which
  vinculum already supports via `client "sqs_receiver"`.
- **Multiple target types.** A single `sns_sender` can target a Topic ARN
  (fan-out), a Target ARN (specific endpoint), or a Phone Number (SMS).

Because SNS has no receiver-side polling, vinculum provides only a sender.

---

## `client "sns_sender" "<name>"`

Receives vinculum bus events and publishes them to AWS SNS. Implements
`bus.Subscriber` so it can be used directly as a subscription target.

```hcl
client "sns_sender" "alerts" {
    aws       = client.prod          # optional; uses default credential chain if omitted
    region    = "us-east-1"          # ignored if aws is set

    # --- Target resolution ---

    # Expression to compute the SNS target value per message.
    # The SNS Publish property is auto-detected from the resolved value:
    #   - Starts with "+"             → PhoneNumber
    #   - ARN with no "/" in resource → TopicArn
    #   - ARN with "/" in resource    → TargetArn
    sns_topic = "arn:aws:sns:us-east-1:123456789012:alerts"

    # Dynamic example:
    # sns_topic = "arn:aws:sns:us-east-1:123456789012:${replace(ctx.topic, "/", "-")}"

    # If omitted, the vinculum topic is used as-is (passthrough mode).

    # --- SNS message properties ---

    # Default Subject for SNS messages (per-message expression).
    # Overridden by $Subject field on individual messages.
    # subject = "Alert: ${ctx.topic}"

    # Message structure — "json" for per-protocol payloads. Plain string.
    # Overridden by $MessageStructure field.
    # message_structure = "json"

    # --- Topic passthrough ---

    # Include the original vinculum topic as an SNS message attribute.
    # topic_attribute = "source_topic"

    # --- FIFO topic options (only for .fifo topics) ---

    # message_group_id = ctx.topic         # required for FIFO topics
    # deduplication_id = ctx.fields["$id"] # optional

    # --- Serialization ---

    # Wire format for payload serialization (default: "auto")
    # wire_format = "json"

    # Metrics and tracing
    # metrics = server.metrics
    # tracing = client.otlp
}

subscription "alerts_to_sns" {
    target     = bus.main
    topics     = ["alert/#"]
    subscriber = client.alerts
}
```

| Attribute | Type | Description |
| --------- | ---- | ----------- |
| `aws` | expression | Reference to a `client "aws"` block. |
| `region` | string | AWS region (ignored if `aws` is set). |
| `sns_topic` | expression | SNS target value expression. Auto-detects TopicArn, TargetArn, or PhoneNumber. If omitted, vinculum topic is used as-is. |
| `subject` | expression | Default Subject for SNS messages (per-message expression). Overridden by `$Subject` field. |
| `message_structure` | string | Message structure (`"json"` for per-protocol payloads). Overridden by `$MessageStructure` field. |
| `topic_attribute` | string | If set, includes the vinculum topic as a message attribute with this name. |
| `message_group_id` | expression | **Required for FIFO topics.** Per-message group ID expression. |
| `deduplication_id` | expression | Per-message deduplication ID expression (FIFO topics). |
| `wire_format` | expression | Wire format for serialization. Default: `"auto"`. |
| `metrics` | expression | Metrics backend reference. |
| `tracing` | expression | Tracing backend reference. |

### Target resolution

The `sns_topic` expression has access to the per-message context: `ctx.topic`,
`ctx.msg`, `ctx.fields`.

**Constant expression optimization:** If `sns_topic` is a constant (e.g. a
string literal ARN), the value is resolved once at config time and reused for
every message — no per-message HCL evaluation. This is the most common case.

**Auto-detection** of the SNS Publish property from the resolved value:

| Resolved value pattern | SNS property | Example |
| ---------------------- | ------------ | ------- |
| Starts with `+` | `PhoneNumber` | `+14155552671` |
| `arn:aws:sns:REGION:ACCOUNT:NAME` (no `/`) | `TopicArn` | `arn:aws:sns:us-east-1:123456789012:alerts` |
| `arn:aws:sns:REGION:ACCOUNT:endpoint/...` (has `/`) | `TargetArn` | `arn:aws:sns:us-east-1:123456789012:endpoint/GCM/myapp/abc123` |

Values that don't match any pattern cause `OnEvent` to return an error.

### Field mapping

**`$CamelCase` fields → SNS publish properties:**

| Vinculum field | SNS property | Notes |
| -------------- | ------------ | ----- |
| `$Subject` | `Subject` | Email/SMS subject line. Overrides `subject` config. |
| `$MessageStructure` | `MessageStructure` | Per-protocol payloads. Overrides `message_structure` config. |

**All other fields → SNS message attributes** with `DataType = "String"`.
`$`-prefixed fields are consumed by the property mapping above and are not
forwarded as message attributes.

SNS allows at most 10 message attributes per message. Trace attributes (up to
3) take priority, followed by `topic_attribute` (if set), then user fields.
Excess fields are dropped with a warning log.

### Message format

Payload is serialized via the configured wire format using `SerializeString`
(SNS message bodies are strings).

Trace context (W3C `traceparent`, `tracestate`, `baggage`) is injected into
message attributes when tracing is active. SNS forwards message attributes to
SQS subscribers, so trace context carries through the SNS → SQS path.

### FIFO topics

Topic ARNs ending in `.fifo` are FIFO topics. The `message_group_id`
attribute is **required** for FIFO topics. Common patterns:

```hcl
message_group_id = "all"          # single group, strict order
message_group_id = ctx.topic      # per-topic ordering
message_group_id = ctx.msg.sku    # per-entity ordering
```

If `deduplication_id` is omitted and content-based deduplication is enabled
on the topic, SNS uses a hash of the message body.

---

## Metrics

All SNS metrics carry attributes: `messaging.system=aws_sns`,
`messaging.destination.name=<topic>`, `vinculum.client.name=<client>`.

| Metric | Type | Unit | Description |
| ------ | ---- | ---- | ----------- |
| `messaging.client.sent.messages` | Int64Counter | `{message}` | Messages published |
| `messaging.client.operation.duration` | Float64Histogram | `s` | Publish API call latency |

---

## Complete examples

### Static topic ARN (most common)

```hcl
client "aws" "prod" {
    region = "us-east-1"
}

client "sns_sender" "alerts" {
    aws       = client.prod
    sns_topic = "arn:aws:sns:us-east-1:123456789012:alerts"
}

subscription "alerts_to_sns" {
    target     = bus.main
    topics     = ["alert/#"]
    subscriber = client.alerts
}
```

### Dynamic topic mapping

```hcl
client "sns_sender" "events" {
    aws       = client.prod
    sns_topic = "arn:aws:sns:us-east-1:123456789012:${replace(ctx.topic, "/", "-")}"

    topic_attribute = "source_topic"
}

subscription "all_events" {
    target     = bus.main
    topics     = ["event/#"]
    subscriber = client.events
}
```

### With subject for email subscribers

```hcl
client "sns_sender" "notifications" {
    aws       = client.prod
    sns_topic = "arn:aws:sns:us-east-1:123456789012:user-notifications"
    subject   = "Notification: ${ctx.topic}"
}

# Override subject per message using $Subject field:
subscription "urgent" {
    target     = bus.main
    topics     = ["urgent/#"]
    transforms = [set_field("$Subject", "URGENT: action required")]
    subscriber = client.notifications
}
```

### FIFO topic

```hcl
client "sns_sender" "order_events" {
    aws       = client.prod
    sns_topic = "arn:aws:sns:us-east-1:123456789012:order-events.fifo"

    message_group_id = ctx.msg.order_id
    deduplication_id = ctx.msg.event_id
}
```

### SNS → SQS pipeline (end-to-end)

```hcl
client "aws" "prod" {
    region = "us-east-1"
}

# Publish to SNS topic (fans out to SQS subscribers)
client "sns_sender" "order_events" {
    aws             = client.prod
    sns_topic       = "arn:aws:sns:us-east-1:123456789012:order-events"
    topic_attribute = "source_topic"
}

subscription "orders_to_sns" {
    target     = bus.main
    topics     = ["order/#"]
    subscriber = client.order_events
}

# Receive from one of the SQS queues subscribed to the SNS topic
client "sqs_receiver" "order_processor" {
    aws            = client.prod
    queue_url      = "https://sqs.us-east-1.amazonaws.com/123456789012/order-processing"
    subscriber     = bus.main
    vinculum_topic = ctx.fields["source_topic"]
}
```
