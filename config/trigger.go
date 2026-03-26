package config

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

type TriggerDefinition struct {
	Type          string    `hcl:",label"`
	Name          string    `hcl:",label"`
	Disabled      bool      `hcl:"disabled,optional"`
	DefRange      hcl.Range `hcl:",def_range"`
	RemainingBody hcl.Body  `hcl:",remain"`
}

type TriggerBlockHandler struct {
	BlockHandlerBase
}

func NewTriggerBlockHandler() *TriggerBlockHandler {
	return &TriggerBlockHandler{}
}

// GetBlockDependencyId returns "trigger.<name>" for trigger types that produce a
// cty value (like "start"), enabling correct dependency ordering for blocks that
// reference trigger.<name>. Other trigger types return "" (no ordering needed).
func (h *TriggerBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	if len(block.Labels) == 2 && (block.Labels[0] == "after" || block.Labels[0] == "interval" || block.Labels[0] == "once" || block.Labels[0] == "start") {
		return "trigger." + block.Labels[1], nil
	}
	return "", nil
}

func (h *TriggerBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	triggerDef := TriggerDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &triggerDef)
	if diags.HasErrors() {
		return diags
	}

	if triggerDef.Disabled {
		return nil
	}

	name := block.Labels[1]

	// Enforce name uniqueness across all non-disabled trigger blocks
	if existingRange, ok := config.TriggerDefRanges[name]; ok {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Trigger already defined",
			Detail:   fmt.Sprintf("Trigger %q already defined at %s", name, existingRange),
			Subject:  &triggerDef.DefRange,
		}}
	}
	config.TriggerDefRanges[name] = triggerDef.DefRange

	switch block.Labels[0] {
	case "after":
		return processAfterTrigger(config, block, &triggerDef)
	case "cron":
		return processCronTrigger(config, block, &triggerDef)
	case "start":
		return processStartTrigger(config, block, &triggerDef)
	case "shutdown":
		return processShutdownTrigger(config, block, &triggerDef)
	case "once":
		return processOnceTrigger(config, block, &triggerDef)
	case "interval":
		return processIntervalTrigger(config, block, &triggerDef)
	case "signals":
		return processSignalsTrigger(config, block, &triggerDef)
	default:
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid trigger type",
			Detail:   fmt.Sprintf("Invalid trigger type: %q. Valid types are: after, cron, interval, once, shutdown, signals, start", block.Labels[0]),
			Subject:  &block.DefRange,
		}}
	}
}

func processCronTrigger(config *Config, block *hcl.Block, triggerDef *TriggerDefinition) hcl.Diagnostics {
	cronDef := CronDefinition{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.evalCtx, &cronDef)
	if diags.HasErrors() {
		return diags
	}
	cronDef.Name = block.Labels[1]

	cronObj, addDiags := BuildCron(config, block, &cronDef)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	config.Startables = append(config.Startables, NewErrorlessStartable(cronObj))
	return diags
}

type triggerStartBody struct {
	Action hcl.Expression `hcl:"action"`
}

func processStartTrigger(config *Config, block *hcl.Block, triggerDef *TriggerDefinition) hcl.Diagnostics {
	body := triggerStartBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.evalCtx, &body)
	if diags.HasErrors() {
		return diags
	}

	name := block.Labels[1]

	evalCtx, addDiags := NewContext(context.Background()).
		WithStringAttribute("trigger", "start").
		WithStringAttribute("name", name).
		BuildEvalContext(config.evalCtx)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	value, addDiags := body.Action.Value(evalCtx)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	config.CtyTriggerMap[name] = value
	config.evalCtx.Variables["trigger"] = ctyObjectOrEmpty(config.CtyTriggerMap)

	return diags
}

type triggerShutdownBody struct {
	Action hcl.Expression `hcl:"action"`
}

func processShutdownTrigger(config *Config, block *hcl.Block, triggerDef *TriggerDefinition) hcl.Diagnostics {
	body := triggerShutdownBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.evalCtx, &body)
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
	config *Config
	action hcl.Expression
	name   string
}

func (a *ShutdownTriggerAction) Stop() error {
	a.config.Logger.Debug("Executing shutdown trigger", zap.String("name", a.name))

	evalCtx, diags := NewContext(context.Background()).
		WithStringAttribute("trigger", "shutdown").
		WithStringAttribute("name", a.name).
		BuildEvalContext(a.config.evalCtx)
	if diags.HasErrors() {
		a.config.Logger.Error("Error building shutdown trigger context", zap.String("name", a.name), zap.Error(diags))
		return nil
	}

	value, addDiags := a.action.Value(evalCtx)
	if addDiags.HasErrors() {
		a.config.Logger.Error("Error executing shutdown trigger", zap.String("name", a.name), zap.Error(addDiags))
		return nil
	}

	a.config.Logger.Debug("Shutdown trigger executed", zap.String("name", a.name), zap.Any("result", value))
	return nil
}

// ctyObjectOrEmpty returns cty.ObjectVal(m) when m is non-empty, or
// cty.EmptyObjectVal when m has no entries (cty.ObjectVal panics on empty maps).
func ctyObjectOrEmpty(m map[string]cty.Value) cty.Value {
	if len(m) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(m)
}

func processSignalsTrigger(config *Config, block *hcl.Block, triggerDef *TriggerDefinition) hcl.Diagnostics {
	signalsDef := SignalsDefinition{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.evalCtx, &signalsDef)
	if diags.HasErrors() {
		return diags
	}

	diags = diags.Extend(config.SetSignalAction("SIGHUP", signalsDef.SigHup))
	diags = diags.Extend(config.SetSignalAction("SIGINFO", signalsDef.SigInfo))
	diags = diags.Extend(config.SetSignalAction("SIGUSR1", signalsDef.SigUsr1))
	diags = diags.Extend(config.SetSignalAction("SIGUSR2", signalsDef.SigUsr2))

	return diags
}
