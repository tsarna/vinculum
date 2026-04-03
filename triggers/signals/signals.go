package signals

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
)

func init() {
	cfg.RegisterTriggerType("signals", cfg.TriggerRegistration{Process: processSignalsTrigger, HasDependencyId: false})
}

func processSignalsTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	signalsDef := cfg.SignalsDefinition{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &signalsDef)
	if diags.HasErrors() {
		return diags
	}

	config.SigActions.TracerProvider = triggerDef.TracerProvider

	diags = diags.Extend(config.SetSignalAction("SIGHUP", signalsDef.SigHup))
	diags = diags.Extend(config.SetSignalAction("SIGINFO", signalsDef.SigInfo))
	diags = diags.Extend(config.SetSignalAction("SIGUSR1", signalsDef.SigUsr1))
	diags = diags.Extend(config.SetSignalAction("SIGUSR2", signalsDef.SigUsr2))

	return diags
}
