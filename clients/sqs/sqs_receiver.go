package sqs

import (
	"context"
	"fmt"

	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	sqsreceiver "github.com/tsarna/vinculum-sqs/receiver"
	wire "github.com/tsarna/vinculum-wire"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("sqs_receiver", processReceiver, cfg.WithSchema(sqsReceiverSchema))
}

// SQSReceiverDefinition is the HCL schema for `client "sqs_receiver" "<name>"`.
// awsClientAttrs documents how an AWS-backed client finds its credentials and
// region: either a shared `client "aws"` block, or a region plus the default
// AWS credential chain.
var awsClientAttrs = map[string]cfg.AttrMeta{
	"aws": {
		Summary: "Shared AWS configuration to use.",
		Doc:     "A `client \"aws\"` block. Without one, the default AWS credential chain is used.",
		Hint:    cfg.HintClientRef,
	},
	"region": {
		Summary: "AWS region to operate in.",
		Doc:     "Overrides the region of the referenced `client \"aws\"` block.",
	},
}

// awsMessageAttrs documents the attributes SQS and SNS senders share for
// mapping a bus message onto a queue or topic message.
var awsMessageAttrs = map[string]cfg.AttrMeta{
	"topic_attribute": {
		Summary: "Message attribute carrying the bus topic.",
		Doc:     "Lets a receiver recover the topic a message was published on.",
	},
	"message_group_id": {
		Summary: "Group ID for a FIFO queue or topic.",
		Doc: "Messages sharing a group are delivered in order; different groups proceed " +
			"independently. Required by a FIFO queue, and evaluated per message.",
		Context: "message",
	},
	"deduplication_id": {
		Summary: "Deduplication ID for a FIFO queue or topic.",
		Doc: "AWS discards a repeat of the same ID within the deduplication window. " +
			"Evaluated per message.",
		Context: "message",
	},
}

var sqsReceiverSchema = cfg.TypeSchema{
	Sample:  &SQSReceiverDefinition{},
	Summary: "Receives messages from an Amazon SQS queue.",
	DocPage: "client-sqs.md#client-sqs_receiver-name",
	Doc: `Polls an SQS queue and delivers each message to the bus or an action. Messages
are deleted once handled, unless ` + "`ack`" + ` says otherwise.`,
	Attrs: cfg.MergeAttrs(awsClientAttrs, cfg.SubscriberSourceAttrs, map[string]cfg.AttrMeta{
		"action": cfg.SubscriberSourceAttrs["action"].WithDoc(
			"`ctx.topic` is the vinculum topic and `ctx.msg` the decoded body. " +
				"`ctx.fields` carries the message's own attributes plus the " +
				"`$`-prefixed SQS system attributes. Under `ack = \"manual\"` the " +
				"message is settled with `inbound::ack()`, which reads the delivery " +
				"from `ctx` and so works equally well from a `subscription` behind " +
				"`subscriber`."),
		"queue_url": {
			Summary: "URL of the queue to receive from.",
			Hint:    cfg.HintURL,
		},
		"vinculum_topic": {
			Summary: "Bus topic to publish arriving messages to.",
			Doc: "The queue's name, taken from `queue_url`, is used when omitted. " +
				"Evaluated per message, before the body is decoded — so `ctx.msg` is " +
				"the raw body rather than a `wire_format` result, and is null when the " +
				"message has none. `ctx.fields` is populated from the message's SQS " +
				"attributes.",
			Hint:    cfg.HintTopicPattern,
			Context: "inbound-message",
			ContextFields: []cfg.ContextField{
				{
					Name: "queue", Type: "string",
					Summary: "Queue the message was received from.",
					Doc:     "The queue's name, taken from `queue_url`.",
				},
				{
					Name: "message_id", Type: "string",
					Summary: "SQS message ID.",
					Doc:     "Empty in the unusual case that SQS returned a message without one.",
				},
			},
		},
		"wait_time": {
			Summary: "How long a poll waits for a message before returning empty.",
			Doc: "Long polling: a non-zero wait cuts both latency and request count. " +
				"SQS caps it at 20 seconds.",
			Hint:    cfg.HintDuration,
			Default: "20s",
		},
		"max_messages": {
			Summary: "Maximum messages to fetch per poll.",
			Doc:     "SQS caps it at 10.",
			Default: "10",
		},
		"visibility_timeout": {
			Summary: "How long a received message stays hidden from other receivers.",
			Doc: "Must exceed the time handling takes, or the message is redelivered while " +
				"still being processed. The queue's own setting applies when omitted.",
			Hint: cfg.HintDuration,
		},
		"ack": cfg.AckAttr.WithDoc(
			"`auto` deletes a message once delivery returns without error; a handler that " +
				"returns an error leaves it on the queue, so it reappears after the " +
				"visibility timeout and is retried. That is fast but loses a message whose " +
				"handling fails after delivery returned — including whenever `queue_size` " +
				"is set, since delivery then returns at the moment the message is queued. " +
				"`manual` deletes nothing until the configuration calls `inbound::ack()`, " +
				"and requires `settle_timeout`. `inbound::nack()` sends nothing: the " +
				"message returns when its visibility timeout lapses and the queue's own " +
				"redrive policy decides when it has been tried enough, and the reason " +
				"reaches the log only."),
		"settle_timeout": cfg.SettleTimeoutAttr,
		"concurrency": {
			Summary: "Number of polling loops run in parallel.",
			Doc:     "Each polls independently, so this multiplies `max_messages` in flight.",
			Default: "1",
		},
		"on_decode_error": cfg.OnDecodeErrorAttr.WithContextFields(
			cfg.ContextField{Name: "queue", Type: "string", Summary: "Queue the message was received from."},
			cfg.ContextField{
				Name: "message_id", Type: "string", Optional: true,
				Summary: "SQS message ID.",
				Doc:     "Absent in the unusual case that SQS returned a message without one.",
			},
		),
		"wire_format": cfg.WireFormatAttr,
		"metrics":     cfg.MetricsAttr,
		"tracing":     cfg.TracingAttr,
	}),
	Constraints: cfg.SubscriberSourceConstraints,
}

type SQSReceiverDefinition struct {
	AWS               hcl.Expression               `hcl:"aws,optional"`
	Region            string                       `hcl:"region,optional"`
	QueueURL          hcl.Expression               `hcl:"queue_url"`
	Subscriber        hcl.Expression               `hcl:"subscriber,optional"`
	Action            hcl.Expression               `hcl:"action,optional"`
	Transforms        hcl.Expression               `hcl:"transforms,optional"`
	OnDecodeError     hcl.Expression               `hcl:"on_decode_error,optional"`
	QueueSize         *int                         `hcl:"queue_size,optional"`
	Baggage           *hclutil.BaggageFilterConfig `hcl:"baggage,block"`
	VinculumTopic     hcl.Expression               `hcl:"vinculum_topic,optional"`
	WaitTime          hcl.Expression               `hcl:"wait_time,optional"`
	MaxMessages       *int                         `hcl:"max_messages,optional"`
	VisibilityTimeout hcl.Expression               `hcl:"visibility_timeout,optional"`
	Ack               string                       `hcl:"ack,optional"`
	AckRange          hcl.Range                    `hcl:"ack,attr_range"`
	SettleTimeout     hcl.Expression               `hcl:"settle_timeout,optional"`
	Concurrency       *int                         `hcl:"concurrency,optional"`
	WireFormat        hcl.Expression               `hcl:"wire_format,optional"`
	Metrics           hcl.Expression               `hcl:"metrics,optional"`
	Tracing           hcl.Expression               `hcl:"tracing,optional"`
	DefRange          hcl.Range                    `hcl:",def_range"`
}

// SQSReceiverClient wraps an SQSReceiver for vinculum config integration.
type SQSReceiverClient struct {
	cfg.BaseClient
	receiver *sqsreceiver.SQSReceiver
}

func (c *SQSReceiverClient) Start() error {
	return c.receiver.Start(context.Background())
}

func (c *SQSReceiverClient) Stop() error {
	return c.receiver.Stop(context.Background())
}

// renamedReceiverAttrs retires `auto_delete`, which was the Redis and RabbitMQ
// behaviour under a third name. The rename is scoped to this block rather than
// declared globally: `auto_delete` is a perfectly good attribute of a RabbitMQ
// receiver's `declare` block, where it means something else entirely.
var renamedReceiverAttrs = cfg.RenameSpec{
	Attrs: map[string]cfg.RenamedAttr{
		"auto_delete": {
			Now:   "ack",
			Since: "0.46.0",
			Note: "`auto_delete = true` is the default, now written `ack = \"auto\"`. " +
				"`auto_delete = false` is `ack = \"manual\"`, which also requires " +
				"`settle_timeout` and is settled with `inbound::ack()` rather than the " +
				"removed `sqs::delete()`.",
		},
	},
}

func processReceiver(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	// Before decoding, so a configuration still saying auto_delete is told what
	// it became rather than that the argument is not expected here.
	if diags := cfg.CheckRenamedAttrs(remainingBody, renamedReceiverAttrs); diags.HasErrors() {
		return nil, diags
	}

	def := SQSReceiverDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	clientName := block.Labels[1]

	// Resolve SQS client from AWS config.
	sqsClient, awsDiags := resolveSQSClient(config, def.AWS, def.Region, &def.DefRange)
	if awsDiags.HasErrors() {
		return nil, awsDiags
	}

	// Resolve queue URL.
	queueURLVal, urlDiags := def.QueueURL.Value(config.EvalCtx())
	if urlDiags.HasErrors() {
		return nil, urlDiags
	}
	if queueURLVal.Type() != cty.String || queueURLVal.AsString() == "" {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "sqs_receiver: queue_url must be a non-empty string",
			Subject:  def.QueueURL.Range().Ptr(),
		}}
	}
	queueURL := queueURLVal.AsString()

	// Resolve metrics and tracing first so the subscriber source can propagate
	// the tracer provider into its async queue (when queue_size is set).
	mp, mpDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if mpDiags.HasErrors() {
		return nil, mpDiags
	}
	tp, tpDiags := config.ResolveTracerProvider(def.Tracing)
	if tpDiags.HasErrors() {
		return nil, tpDiags
	}

	if baggageDiags := def.Baggage.Validate(); baggageDiags.HasErrors() {
		return nil, baggageDiags
	}

	policy, ackDiags := config.ResolveAck(cfg.AckRequest{
		Receiver:      fmt.Sprintf("sqs_receiver client %q", clientName),
		Value:         def.Ack,
		ValueRange:    def.AckRange,
		SettleTimeout: def.SettleTimeout,
		DefRange:      def.DefRange,
	})
	if ackDiags.HasErrors() {
		return nil, ackDiags
	}

	// Resolve subscriber / action (+ optional transforms / async queue).
	target, subDiags := cfg.SubscriberSource{
		Subscriber: def.Subscriber,
		Action:     def.Action,
		Transforms: def.Transforms,
		QueueSize:  def.QueueSize,
	}.Resolve(config, def.DefRange, "sqs_receiver/"+clientName, tp)
	if subDiags.HasErrors() {
		return nil, subDiags
	}

	// Bound how long a message may go unsettled, outside the async queue so the
	// clock starts when the message arrives rather than when a worker reaches
	// it. A no-op unless ack = "manual".
	target = cfg.NewSettleTimeoutSubscriber(policy, target, "sqs_receiver/"+clientName, config.UserLogger)

	// Strip untrusted inbound baggage at this external boundary before it reaches
	// the action. Secure by default: a nil baggage block strips everything; opt
	// in with baggage { passthrough | allow | deny }.
	target = cfg.NewBaggageFilterSubscriber(def.Baggage, target, config.Logger)

	// Resolve wire format.
	var wf wire.WireFormat = wire.Auto
	if cfg.IsExpressionProvided(def.WireFormat) {
		wfVal, wfDiags := def.WireFormat.Value(config.EvalCtx())
		if wfDiags.HasErrors() {
			return nil, wfDiags
		}
		resolved, err := cfg.GetWireFormatFromValue(wfVal)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "sqs_receiver: invalid wire_format",
				Detail:   err.Error(),
				Subject:  def.WireFormat.Range().Ptr(),
			}}
		}
		wf = resolved
	}
	ctyWF := &cfg.CtyWireFormat{Inner: wf}

	// Build receiver.
	builder := sqsreceiver.NewReceiver().
		WithClient(sqsClient).
		WithClientName(clientName).
		WithQueueURL(queueURL).
		WithSubscriber(target).
		WithWireFormat(ctyWF).
		WithDecodeErrorHook(cfg.MakeDecodeErrorHook(config, def.OnDecodeError,
			fmt.Sprintf("sqs receiver %q", clientName))).
		WithMeterProvider(mp).
		WithLogger(config.Logger).
		WithTracerProvider(tp)

	// Wait time (duration → seconds).
	if cfg.IsExpressionProvided(def.WaitTime) {
		d, dDiags := config.ParseDuration(def.WaitTime)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		builder = builder.WithWaitTime(int32(d.Seconds()))
	}

	// Visibility timeout (duration → seconds).
	if cfg.IsExpressionProvided(def.VisibilityTimeout) {
		d, dDiags := config.ParseDuration(def.VisibilityTimeout)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		secs := int32(d.Seconds())
		builder = builder.WithVisibilityTimeout(secs)
	}

	if def.MaxMessages != nil {
		builder = builder.WithMaxMessages(int32(*def.MaxMessages))
	}
	builder = builder.WithAutoDelete(!policy.Manual())
	if def.Concurrency != nil {
		builder = builder.WithConcurrency(*def.Concurrency)
	}

	// Vinculum topic resolution.
	if cfg.IsExpressionProvided(def.VinculumTopic) {
		topicFn := makeVinculumTopicFunc(config, def.VinculumTopic, sqsreceiver.QueueNameFromURL(queueURL))
		builder = builder.WithTopicFunc(topicFn)
	}
	// else: default topic = queue name (handled by builder)

	receiver, err := builder.Build()
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("sqs_receiver %s: %s", clientName, err.Error()),
			Subject:  &def.DefRange,
		}}
	}

	wrapper := &SQSReceiverClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		receiver: receiver,
	}

	config.Startables = append(config.Startables, wrapper)
	config.Stoppables = append(config.Stoppables, wrapper)

	return wrapper, nil
}

// makeVinculumTopicFunc builds the per-message HCL evaluator for a receiver's
// `vinculum_topic` expression, against the shared `inbound-message` shape plus
// the queue and message ID — the same two names this receiver's
// `on_decode_error` uses.
//
// An SQS message carries no destination of its own — the queue is the
// receiver's, not the message's — so `vinculum_topic` falls back to the queue
// name when it is not set.
//
// ctx.msg is the *raw* body here, unlike every other receiver: the topic is
// chosen before the body is decoded. A message with no body yields a null
// rather than no attribute at all, so an expression can test for it instead of
// failing to evaluate.
func makeVinculumTopicFunc(config *cfg.Config, expr hcl.Expression, queueName string) sqsreceiver.TopicFunc {
	return func(msg sqstypes.Message, fields map[string]string) string {
		var body any
		if msg.Body != nil {
			body = *msg.Body
		}
		ctxBuilder, err := cfg.NewInboundContext(body, fields)
		if err != nil {
			return ""
		}

		var msgID string
		if msg.MessageId != nil {
			msgID = *msg.MessageId
		}

		evalCtx, err := ctxBuilder.
			WithStringAttribute("queue", queueName).
			WithStringAttribute("message_id", msgID).
			BuildEvalContext(config.EvalCtx())
		if err != nil {
			return ""
		}
		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return ""
		}
		if val.IsNull() || val.Type() != cty.String {
			return ""
		}
		return val.AsString()
	}
}
