// Package sns implements the `client "sns_sender"` block for vinculum's
// SNS integration.
package sns

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	snssender "github.com/tsarna/vinculum-sns/sender"
	awsclient "github.com/tsarna/vinculum/clients/aws"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("sns_sender", processSender)
}

// SNSSenderDefinition is the HCL schema for `client "sns_sender" "<name>"`.
type SNSSenderDefinition struct {
	AWS              hcl.Expression   `hcl:"aws,optional"`
	Region           string           `hcl:"region,optional"`
	SNSTopic         hcl.Expression   `hcl:"sns_topic,optional"`
	Subject          hcl.Expression   `hcl:"subject,optional"`
	MessageStructure string           `hcl:"message_structure,optional"`
	TopicAttribute   string           `hcl:"topic_attribute,optional"`
	MessageGroupID   hcl.Expression   `hcl:"message_group_id,optional"`
	DeduplicationID  hcl.Expression   `hcl:"deduplication_id,optional"`
	WireFormat       hcl.Expression   `hcl:"wire_format,optional"`
	Metrics          hcl.Expression   `hcl:"metrics,optional"`
	Tracing          hcl.Expression   `hcl:"tracing,optional"`
	DefRange         hcl.Range        `hcl:",def_range"`
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
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
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
