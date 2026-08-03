package sqs

import (
	"context"
	"fmt"

	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
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
		Doc:     "Messages sharing a group are delivered in order; different groups proceed independently.",
	},
	"deduplication_id": {
		Summary: "Deduplication ID for a FIFO queue or topic.",
		Doc:     "AWS discards a repeat of the same ID within the deduplication window.",
	},
}

var sqsReceiverSchema = cfg.TypeSchema{
	Sample:  &SQSReceiverDefinition{},
	Summary: "Receives messages from an Amazon SQS queue.",
	Doc: `Polls an SQS queue and delivers each message to the bus or an action. Messages
are deleted once handled, unless ` + "`auto_delete`" + ` says otherwise.`,
	Attrs: cfg.MergeAttrs(awsClientAttrs, cfg.SubscriberSourceAttrs, map[string]cfg.AttrMeta{
		"queue_url": {
			Summary: "URL of the queue to receive from.",
			Hint:    cfg.HintURL,
		},
		"vinculum_topic": {
			Summary: "Bus topic to publish arriving messages to.",
			Hint:    cfg.HintTopicPattern,
		},
		"wait_time": {
			Summary: "How long a poll waits for a message before returning empty.",
			Doc:     "Long polling: a non-zero wait cuts both latency and request count.",
			Hint:    cfg.HintDuration,
		},
		"max_messages": {
			Summary: "Maximum messages to fetch per poll.",
		},
		"visibility_timeout": {
			Summary: "How long a received message stays hidden from other receivers.",
			Doc:     "Must exceed the time handling takes, or the message is redelivered while still being processed.",
			Hint:    cfg.HintDuration,
		},
		"auto_delete": {
			Summary: "Delete a message when it is received rather than after it is handled.",
			Doc:     "Faster, but a message is lost if handling fails.",
			Hint:    cfg.HintBool,
		},
		"concurrency": {
			Summary: "Number of messages handled at once.",
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
	AutoDelete        *bool                        `hcl:"auto_delete,optional"`
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

// CtyValue exposes the receiver as a capsule so VCL functions like
// sqs::delete() and sqs::extend_visibility() can reference it.
func (c *SQSReceiverClient) CtyValue() cty.Value {
	return sqsreceiver.NewReceiverCapsule(c.receiver)
}

func (c *SQSReceiverClient) Start() error {
	return c.receiver.Start(context.Background())
}

func (c *SQSReceiverClient) Stop() error {
	return c.receiver.Stop(context.Background())
}

func processReceiver(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
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
	if def.AutoDelete != nil {
		builder = builder.WithAutoDelete(*def.AutoDelete)
	}
	if def.Concurrency != nil {
		builder = builder.WithConcurrency(*def.Concurrency)
	}

	// Vinculum topic resolution.
	if cfg.IsExpressionProvided(def.VinculumTopic) {
		topicFn := makeVinculumTopicFunc(config, def.VinculumTopic)
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

// makeVinculumTopicFunc builds a closure that evaluates the vinculum_topic
// HCL expression per-message with message-specific context variables.
func makeVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) sqsreceiver.TopicFunc {
	return func(msg sqstypes.Message, fields map[string]string) string {
		// Build per-message eval context.
		var msgID string
		if msg.MessageId != nil {
			msgID = *msg.MessageId
		}

		ctxBuilder := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("message_id", msgID)

		ctyFields := make(map[string]cty.Value, len(fields))
		for k, v := range fields {
			ctyFields[k] = cty.StringVal(v)
		}
		ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))

		// Include deserialized body as ctx.msg if available.
		if msg.Body != nil {
			ctyMsg, err := go2cty2go.AnyToCty(*msg.Body)
			if err == nil {
				ctxBuilder = ctxBuilder.WithAttribute("msg", ctyMsg)
			}
		}

		evalCtx, err := ctxBuilder.BuildEvalContext(config.EvalCtx())
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
