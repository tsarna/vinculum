// Package sns implements the `client "sns_sender"` block for vinculum's
// SNS integration.
package sns

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	snssender "github.com/tsarna/vinculum-sns/sender"
	wire "github.com/tsarna/vinculum-wire"
	awsclient "github.com/tsarna/vinculum/clients/aws"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("sns_sender", processSender, cfg.WithSchema(snsSenderSchema))
}

// SNSSenderDefinition is the HCL schema for `client "sns_sender" "<name>"`.
// awsClientAttrs and awsMessageAttrs are declared in the sqs package's
// receiver; SNS repeats the same two groups here because the packages are
// separate.
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

var snsSenderSchema = cfg.TypeSchema{
	Sample:  &SNSSenderDefinition{},
	Summary: "Publishes messages to an Amazon SNS topic.",
	DocPage: "client-sns.md#client-sns_sender-name",
	Doc:     `Acts as a subscriber: messages sent to this client are published to the topic.`,
	Attrs: cfg.MergeAttrs(awsClientAttrs, map[string]cfg.AttrMeta{
		"sns_topic": {
			Summary: "Where to publish: a topic ARN, an endpoint ARN, or a phone number.",
			Doc: "Which of the three it is, is detected from the value — a leading `+` is " +
				"an SMS phone number, an ARN containing `/` is an endpoint target for " +
				"mobile push, and any other SNS ARN is a topic. A value matching none of " +
				"them fails the publish.\n\n" +
				"The bus topic is used as the target when this is omitted. A constant is " +
				"resolved once at config load; anything else is evaluated per message.",
			Context: "message",
		},
		"subject": {
			Summary: "Subject line for subscribers that have one, such as email.",
			Doc: "Evaluated per message. A `$Subject` field on the message overrides it " +
				"for that message.",
			Context: "message",
		},
		"message_structure": {
			Summary: "Set to `json` to send a different payload per protocol.",
			Doc: "The message must then be a JSON object keyed by protocol, with a " +
				"`default` entry. A `$MessageStructure` field on the message overrides " +
				"it for that message.",
			Enum: []string{"json"},
		},
		"topic_attribute": {
			Summary: "Message attribute carrying the bus topic.",
			Doc:     "Lets a subscriber recover the topic a message was published on.",
		},
		"message_group_id": {
			Summary: "Group ID for a FIFO topic.",
			Doc: "Messages sharing a group are delivered in order; different groups proceed " +
				"independently. Required by a FIFO topic, and evaluated per message.",
			Context: "message",
		},
		"deduplication_id": {
			Summary: "Deduplication ID for a FIFO topic.",
			Doc: "AWS discards a repeat of the same ID within the deduplication window. " +
				"Evaluated per message.",
			Context: "message",
		},
		"wire_format": cfg.WireFormatAttr,
		"metrics":     cfg.MetricsAttr,
		"tracing":     cfg.TracingAttr,
	}),
}

type SNSSenderDefinition struct {
	AWS              hcl.Expression `hcl:"aws,optional"`
	Region           string         `hcl:"region,optional"`
	SNSTopic         hcl.Expression `hcl:"sns_topic,optional"`
	Subject          hcl.Expression `hcl:"subject,optional"`
	MessageStructure string         `hcl:"message_structure,optional"`
	TopicAttribute   string         `hcl:"topic_attribute,optional"`
	MessageGroupID   hcl.Expression `hcl:"message_group_id,optional"`
	DeduplicationID  hcl.Expression `hcl:"deduplication_id,optional"`
	WireFormat       hcl.Expression `hcl:"wire_format,optional"`
	Metrics          hcl.Expression `hcl:"metrics,optional"`
	Tracing          hcl.Expression `hcl:"tracing,optional"`
	DefRange         hcl.Range      `hcl:",def_range"`
}

// SNSSenderClient wraps an SNSSender for vinculum config integration.
type SNSSenderClient struct {
	cfg.BaseClient
	sender *snssender.SNSSender
}

// CtyValue exposes the sender as a subscriber capsule so it can be used
// as a subscription target (e.g. `subscriber = client.alerts`).
func (c *SNSSenderClient) CtyValue() cty.Value {
	return cfg.NewSubscriberCapsule(c.sender)
}

func (c *SNSSenderClient) Start() error {
	c.sender.Start()
	return nil
}

func (c *SNSSenderClient) Stop() error {
	c.sender.Stop()
	return nil
}

func processSender(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := SNSSenderDefinition{}
	diags := cfg.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	clientName := block.Labels[1]

	// Resolve SNS client from AWS config.
	snsClient, awsDiags := resolveSNSClient(config, def.AWS, def.Region, &def.DefRange)
	if awsDiags.HasErrors() {
		return nil, awsDiags
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
				Summary:  "sns_sender: invalid wire_format",
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
	builder := snssender.NewSender().
		WithClient(snsClient).
		WithClientName(clientName).
		WithWireFormat(ctyWF).
		WithMeterProvider(mp).
		WithLogger(config.Logger).
		WithTracerProvider(tp)

	// Track whether any hooks need per-message evaluation.
	hasHooks := false

	// Target resolution: sns_topic expression.
	if cfg.IsExpressionProvided(def.SNSTopic) {
		// Check if the expression is a constant (most common: static ARN literal).
		if constVal, ok := cfg.IsConstantExpression(def.SNSTopic); ok {
			if constVal.Type() != cty.String || constVal.AsString() == "" {
				return nil, hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "sns_sender: sns_topic must be a non-empty string",
					Subject:  def.SNSTopic.Range().Ptr(),
				}}
			}
			builder = builder.WithStaticTarget(constVal.AsString())
		} else {
			// Dynamic expression — evaluated per message.
			builder = builder.WithTopicHook(makeSenderHook(def.SNSTopic))
			hasHooks = true
		}
	} else {
		// No sns_topic — passthrough mode (vinculum topic used as target value).
		builder = builder.WithPassthrough()
	}

	// Subject expression.
	if cfg.IsExpressionProvided(def.Subject) {
		builder = builder.WithSubjectHook(makeSenderHook(def.Subject))
		hasHooks = true
	}

	// Message structure.
	if def.MessageStructure != "" {
		builder = builder.WithMessageStructure(def.MessageStructure)
	}

	// Topic attribute.
	if def.TopicAttribute != "" {
		builder = builder.WithTopicAttribute(def.TopicAttribute)
	}

	// FIFO topic support.
	if cfg.IsExpressionProvided(def.MessageGroupID) {
		fifo := &snssender.FIFOConfig{
			GroupIDHook: makeSenderHook(def.MessageGroupID),
		}
		if cfg.IsExpressionProvided(def.DeduplicationID) {
			fifo.DeduplicationHook = makeSenderHook(def.DeduplicationID)
		}
		builder = builder.WithFIFOConfig(fifo)
		hasHooks = true
	}

	// Provide shared hook context builder when any hooks are configured.
	if hasHooks {
		builder = builder.WithMakeHookContext(makeSenderHookContext(config))
	}

	sender, err := builder.Build()
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("sns_sender %s: %s", clientName, err.Error()),
			Subject:  &def.DefRange,
		}}
	}

	wrapper := &SNSSenderClient{
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

// resolveSNSClient creates an SNS API client from either an explicit
// `client "aws"` reference or inline region + default credentials.
func resolveSNSClient(config *cfg.Config, awsExpr hcl.Expression, region string, defRange *hcl.Range) (*sns.Client, hcl.Diagnostics) {
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
		return sns.NewFromConfig(connector.Config()), nil
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

	return sns.NewFromConfig(awsCfg), nil
}

// makeSenderHookContext returns a MakeHookContextFunc that builds a shared
// HCL evaluation context from the per-message data (topic, msg, fields).
// This context is built once per OnEvent call and reused across all hooks.
func makeSenderHookContext(config *cfg.Config) snssender.MakeHookContextFunc {
	return func(topic string, msg any, fields map[string]string) (snssender.HookContext, error) {
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return nil, fmt.Errorf("convert msg: %w", err)
		}

		ctxBuilder := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("topic", topic).
			WithAttribute("msg", ctyMsg)

		ctyFields := make(map[string]cty.Value, len(fields))
		for k, v := range fields {
			ctyFields[k] = cty.StringVal(v)
		}
		ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))

		evalCtx, err := ctxBuilder.BuildEvalContext(config.EvalCtx())
		if err != nil {
			return nil, err
		}
		return evalCtx, nil
	}
}

// makeSenderHook returns a HookFunc that evaluates an HCL expression against
// the shared HookContext (an *hcl.EvalContext built by makeSenderHookContext).
func makeSenderHook(expr hcl.Expression) snssender.HookFunc {
	return func(hookCtx snssender.HookContext) (string, error) {
		evalCtx := hookCtx.(*hcl.EvalContext)
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
