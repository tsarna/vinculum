package start

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterTriggerType("start", cfg.TriggerRegistration{Process: processStartTrigger, HasDependencyId: true})
}

type triggerStartBody struct {
	Action hcl.Expression `hcl:"action"`
}

type startTrigger struct {
	name   string
	config *cfg.Config
	expr   hcl.Expression
}

func (t *startTrigger) PostStart() error {
	evalCtx, err := hclutil.NewEvalContext(context.Background()).
		WithStringAttribute("trigger", "start").
		WithStringAttribute("name", t.name).
		BuildEvalContext(t.config.EvalCtx())
	if err != nil {
		t.config.Logger.Error("start trigger: error building eval context",
			zap.String("name", t.name), zap.Error(err))
		return nil
	}

	value, diags := t.expr.Value(evalCtx)
	if diags.HasErrors() {
		t.config.Logger.Error("start trigger: action error",
			zap.String("name", t.name), zap.Error(diags))
		return nil
	}

	t.config.CtyTriggerMap[t.name] = value
	t.config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(t.config.CtyTriggerMap)
	return nil
}

func processStartTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerStartBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	name := block.Labels[1]

	// Store a null placeholder so other blocks can reference trigger.<name> during processing.
	// The real value is set in PostStart() after all Startables have completed.
	config.CtyTriggerMap[name] = cty.NullVal(cty.DynamicPseudoType)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.PostStartables = append(config.PostStartables, &startTrigger{name: name, config: config, expr: body.Action})

	return diags
}
