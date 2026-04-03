package shutdown

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterTriggerType("shutdown", cfg.TriggerRegistration{Process: processShutdownTrigger, HasDependencyId: false})
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
		config: config,
		action: body.Action,
		name:   block.Labels[1],
	}
	config.Stoppables = append(config.Stoppables, action)
	return diags
}

// ShutdownTriggerAction evaluates an action expression during graceful shutdown.
type ShutdownTriggerAction struct {
	config *cfg.Config
	action hcl.Expression
	name   string
}

func (a *ShutdownTriggerAction) Stop() error {
	a.config.Logger.Debug("Executing shutdown trigger", zap.String("name", a.name))

	spanCtx, stopSpan := hclutil.StartTriggerSpan(context.Background(), "shutdown", a.name)

	evalCtx, err := hclutil.NewEvalContext(spanCtx).
		WithStringAttribute("trigger", "shutdown").
		WithStringAttribute("name", a.name).
		BuildEvalContext(a.config.EvalCtx())
	if err != nil {
		a.config.Logger.Error("Error building shutdown trigger context", zap.String("name", a.name), zap.Error(err))
		stopSpan(err)
		return nil
	}

	value, addDiags := a.action.Value(evalCtx)
	if addDiags.HasErrors() {
		a.config.Logger.Error("Error executing shutdown trigger", zap.String("name", a.name), zap.Error(addDiags))
		stopSpan(addDiags)
		return nil
	}

	stopSpan(nil)
	a.config.Logger.Debug("Shutdown trigger executed", zap.String("name", a.name), zap.Any("result", value))
	return nil
}
