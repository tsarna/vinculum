// Package sqs implements the `client "sqs_sender"` and `client "sqs_receiver"`
// blocks for vinculum's SQS integration.
package sqs

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	sqssender "github.com/tsarna/vinculum-sqs/sender"
	awsclient "github.com/tsarna/vinculum/clients/aws"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("sqs_sender", processSender)
}

// SQSSenderDefinition is the HCL schema for `client "sqs_sender" "<name>"`.
type SQSSenderDefinition struct {
	AWS             hcl.Expression   `hcl:"aws,optional"`
	Region          string           `hcl:"region,optional"`
	QueueURL        hcl.Expression   `hcl:"queue_url"`
	DelaySeconds    *int             `hcl:"delay_seconds,optional"`
	TopicAttribute  string           `hcl:"topic_attribute,optional"`
	MessageGroupID  hcl.Expression   `hcl:"message_group_id,optional"`
	DeduplicationID hcl.Expression   `hcl:"deduplication_id,optional"`
	Batch           *BatchDefinition `hcl:"batch,block"`
	WireFormat      hcl.Expression   `hcl:"wire_format,optional"`
	Metrics         hcl.Expression   `hcl:"metrics,optional"`
	Tracing         hcl.Expression   `hcl:"tracing,optional"`
	DefRange        hcl.Range        `hcl:",def_range"`
}

// BatchDefinition is the HCL schema for the `batch {}` sub-block.
type BatchDefinition struct {
	Enabled  *bool          `hcl:"enabled,optional"`
	MaxSize  *int           `hcl:"max_size,optional"`
	MaxDelay hcl.Expression `hcl:"max_delay,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

// SQSSenderClient wraps an SQSSender for vinculum config integration.
type SQSSenderClient struct {
	cfg.BaseClient
	sender *sqssender.SQSSender
}

// CtyValue exposes the sender as a subscriber capsule so it can be used
// as a subscription target (e.g. `subscriber = client.orders`).
func (c *SQSSenderClient) CtyValue() cty.Value {
	return cfg.NewSubscriberCapsule(c.sender)
}

func (c *SQSSenderClient) Start() error {
	c.sender.Start()
	return nil
}

func (c *SQSSenderClient) Stop() error {
	c.sender.Stop()
	return nil
}

func processSender(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := SQSSenderDefinition{}
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
			Summary:  "sqs_sender: queue_url must be a non-empty string",
			Subject:  def.QueueURL.Range().Ptr(),
		}}
	}
	queueURL := queueURLVal.AsString()

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
				Summary:  "sqs_sender: invalid wire_format",
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

	// Build sender.
	builder := sqssender.NewSender().
		WithClient(sqsClient).
		WithClientName(clientName).
		WithQueueURL(queueURL).
		WithWireFormat(ctyWF).
		WithMeterProvider(mp).
		WithLogger(config.Logger).
		WithTracerProvider(tp)

	if def.DelaySeconds != nil {
		builder = builder.WithDelaySeconds(int32(*def.DelaySeconds))
	}
	if def.TopicAttribute != "" {
		builder = builder.WithTopicAttribute(def.TopicAttribute)
	}

	// FIFO queue support.
	if cfg.IsExpressionProvided(def.MessageGroupID) {
		fifo := &sqssender.FIFOConfig{
			GroupIDFunc: makeSenderExprFunc(config, def.MessageGroupID),
		}
		if cfg.IsExpressionProvided(def.DeduplicationID) {
			fifo.DeduplicationFunc = makeSenderExprFunc(config, def.DeduplicationID)
		}
		builder = builder.WithFIFOConfig(fifo)
	}

	// Batching.
	if def.Batch != nil && (def.Batch.Enabled == nil || *def.Batch.Enabled) {
		bc := &sqssender.BatchConfig{MaxSize: 10}
		if def.Batch.MaxSize != nil {
			bc.MaxSize = *def.Batch.MaxSize
		}
		if cfg.IsExpressionProvided(def.Batch.MaxDelay) {
			d, dDiags := config.ParseDuration(def.Batch.MaxDelay)
			if dDiags.HasErrors() {
				return nil, dDiags
			}
			bc.MaxDelay = d
		}
		builder = builder.WithBatchConfig(bc)
	}

	sender, err := builder.Build()
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("sqs_sender %s: %s", clientName, err.Error()),
			Subject:  &def.DefRange,
		}}
	}

	wrapper := &SQSSenderClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		sender: sender,
	}

	config.Startables = append(config.Startables, wrapper)
	config.Stoppables = append(config.Stoppables, wrapper)

	return wrapper, nil
}

// resolveSQSClient creates an SQS API client from either an explicit
// `client "aws"` reference or inline region + default credentials.
func resolveSQSClient(config *cfg.Config, awsExpr hcl.Expression, region string, defRange *hcl.Range) (*sqs.Client, hcl.Diagnostics) {
	if cfg.IsExpressionProvided(awsExpr) {
		base, baseDiags := cfg.GetClientFromExpression(config, awsExpr)
		if baseDiags.HasErrors() {
			return nil, baseDiags
		}
		connector, ok := base.(awsclient.AWSConnector)
		if !ok {
			r := awsExpr.Range()
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "aws must reference a client \"aws\" block",
				Detail:   fmt.Sprintf("got %T", base),
				Subject:  &r,
			}}
		}
		return sqs.NewFromConfig(connector.Config()), nil
	}

	// Inline: use default credential chain with region.
	if region == "" {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "either aws or region is required",
			Detail:   "set aws = client.<name> to reference an AWS config block, or set region for inline credentials",
			Subject:  defRange,
		}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	awsCfg, err := awsclient.BuildDefaultConfig(ctx, region)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "failed to load AWS config",
			Detail:   err.Error(),
			Subject:  defRange,
		}}
	}

	return sqs.NewFromConfig(awsCfg), nil
}

// makeSenderExprFunc builds a closure that evaluates an HCL expression
// per-message with the standard vinculum sender context (ctx.topic, ctx.msg,
// ctx.fields). Used for message_group_id and deduplication_id.
func makeSenderExprFunc(config *cfg.Config, expr hcl.Expression) func(topic string, msg any, fields map[string]string) (string, error) {
	return func(topic string, msg any, fields map[string]string) (string, error) {
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("convert msg: %w", err)
		}

		ctxBuilder := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("topic", topic).
			WithAttribute("msg", ctyMsg)

		if len(fields) > 0 {
			ctyFields := make(map[string]cty.Value, len(fields))
			for k, v := range fields {
				ctyFields[k] = cty.StringVal(v)
			}
			ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
		}

		evalCtx, err := ctxBuilder.BuildEvalContext(config.EvalCtx())
		if err != nil {
			return "", err
		}
		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return "", diags
		}
		if val.IsNull() {
			return "", nil
		}
		if val.Type() != cty.String {
			return "", fmt.Errorf("expression must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}
