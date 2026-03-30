package start

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
)

func init() {
	cfg.RegisterTriggerType("start", cfg.TriggerRegistration{Process: processStartTrigger, HasDependencyId: true})
}

type triggerStartBody struct {
	Action hcl.Expression `hcl:"action"`
}

func processStartTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerStartBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	name := block.Labels[1]

	evalCtx, err := hclutil.NewEvalContext(context.Background()).
		WithStringAttribute("trigger", "start").
		WithStringAttribute("name", name).
		BuildEvalContext(config.EvalCtx())
	if err != nil {
		return diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Error building eval context", Detail: err.Error()})
	}

	value, addDiags := body.Action.Value(evalCtx)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	config.CtyTriggerMap[name] = value
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)

	return diags
}
