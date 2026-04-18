package sqs

import (
	"context"
	"fmt"

	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	sqsreceiver "github.com/tsarna/vinculum-sqs/receiver"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("sqs_receiver", processReceiver)
}

// SQSReceiverDefinition is the HCL schema for `client "sqs_receiver" "<name>"`.
type SQSReceiverDefinition struct {
	AWS               hcl.Expression `hcl:"aws,optional"`
	Region            string         `hcl:"region,optional"`
	QueueURL          hcl.Expression `hcl:"queue_url"`
	Subscriber        hcl.Expression `hcl:"subscriber,optional"`
	Action            hcl.Expression `hcl:"action,optional"`
	VinculumTopic     hcl.Expression `hcl:"vinculum_topic,optional"`
	WaitTime          hcl.Expression `hcl:"wait_time,optional"`
	MaxMessages       *int           `hcl:"max_messages,optional"`
	VisibilityTimeout hcl.Expression `hcl:"visibility_timeout,optional"`
	AutoDelete        *bool          `hcl:"auto_delete,optional"`
	Concurrency       *int           `hcl:"concurrency,optional"`
	WireFormat        hcl.Expression `hcl:"wire_format,optional"`
	Metrics           hcl.Expression `hcl:"metrics,optional"`
	Tracing           hcl.Expression `hcl:"tracing,optional"`
	DefRange          hcl.Range      `hcl:",def_range"`
}

// SQSReceiverClient wraps an SQSReceiver for vinculum config integration.
type SQSReceiverClient struct {
	cfg.BaseClient
	receiver *sqsreceiver.SQSReceiver
}

// CtyValue exposes the receiver as a capsule so VCL functions like
// sqs_delete() and sqs_extend_visibility() can reference it.
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

	// Resolve subscriber or action (exactly one required).
	hasSubscriber := cfg.IsExpressionProvided(def.Subscriber)
	hasAction := cfg.IsExpressionProvided(def.Action)
	if hasSubscriber == hasAction {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("sqs_receiver %q: exactly one of subscriber or action must be specified", clientName),
			Subject:  &def.DefRange,
		}}
	}

	var target bus.Subscriber
	if hasSubscriber {
		sub, subDiags := cfg.GetSubscriberFromExpression(config, def.Subscriber)
		if subDiags.HasErrors() {
			return nil, subDiags
		}
		target = sub
	} else {
		target = cfg.NewActionSubscriber(config, def.Action)
	}

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

	// Resolve metrics and tracing.
	mp, mpDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if mpDiags.HasErrors() {
		return nil, mpDiags
	}
	tp, tpDiags := config.ResolveTracerProvider(def.Tracing)
	if tpDiags.HasErrors() {
		return nil, tpDiags
	}

	// Build receiver.
	builder := sqsreceiver.NewReceiver().
		WithClient(sqsClient).
		WithClientName(clientName).
		WithQueueURL(queueURL).
		WithSubscriber(target).
		WithWireFormat(ctyWF).
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

		if len(fields) > 0 {
			ctyFields := make(map[string]cty.Value, len(fields))
			for k, v := range fields {
				ctyFields[k] = cty.StringVal(v)
			}
			ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
		}

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
