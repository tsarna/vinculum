package shutdown

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterTriggerType("shutdown", cfg.TriggerRegistration{Process: processShutdownTrigger, HasDependencyId: false},
		cfg.WithSchema(shutdownTriggerSchema))

	cfg.RegisterContextSchema("trigger-shutdown", cfg.ContextSchema{
		Summary: "Evaluated once during graceful shutdown.",
		Fields: []cfg.ContextField{
			{Name: "trigger", Type: "string", Summary: "Always `\"shutdown\"`."},
			{Name: "name", Type: "string", Summary: "Name of this trigger block."},
		},
	})
}

var shutdownTriggerSchema = cfg.TypeSchema{
	Sample:  &triggerShutdownBody{},
	Summary: "Evaluates an action once during graceful shutdown.",
	Doc: `Runs after SIGINT or SIGTERM, in the reverse of the order stoppable
components were registered, and before they are torn down. Errors are logged
but do not abort the shutdown sequence.

Does **not** create a ` + "`trigger.<name>`" + ` value.`,
	Attrs: map[string]cfg.AttrMeta{
		"action": {
			Summary: "Evaluated once during shutdown.",
			Hint:    cfg.HintActionExpression,
			Context: "trigger-shutdown",
		},
	},
}

type triggerShutdownBody struct {
	Action hcl.Expression `hcl:"action"`
}

func processShutdownTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerShutdownBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	action := &ShutdownTriggerAction{
		config:         config,
		action:         body.Action,
		name:           block.Labels[1],
		tracerProvider: triggerDef.TracerProvider,
	}
	config.PreStoppables = append(config.PreStoppables, action)
	return diags
}

// ShutdownTriggerAction evaluates an action expression during graceful shutdown.
type ShutdownTriggerAction struct {
	config         *cfg.Config
	action         hcl.Expression
	name           string
	tracerProvider trace.TracerProvider
}

func (a *ShutdownTriggerAction) PreStop() error {
	a.config.Logger.Debug("Executing shutdown trigger", zap.String("name", a.name))

	spanCtx, stopSpan := hclutil.StartTriggerSpan(context.Background(), a.tracerProvider, "shutdown", a.name)

	evalCtx, err := hclutil.NewEvalContext(spanCtx).
		WithStringAttribute("trigger", "shutdown").
		WithStringAttribute("name", a.name).
		BuildEvalContext(a.config.EvalCtx())
	if err != nil {
		a.config.UserLogger.Error("Error building shutdown trigger context", zap.String("name", a.name), zap.Error(err))
		stopSpan(err)
		return nil
	}

	value, addDiags := a.action.Value(evalCtx)
	if addDiags.HasErrors() {
		a.config.UserLogger.Error("Error executing shutdown trigger", zap.String("name", a.name), a.config.ActionError(addDiags))
		stopSpan(addDiags)
		return nil
	}

	stopSpan(nil)
	a.config.Logger.Debug("Shutdown trigger executed", zap.String("name", a.name), zap.Any("result", value))
	return nil
}
