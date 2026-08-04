package signals

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
)

func init() {
	cfg.RegisterTriggerType("signals", cfg.TriggerRegistration{Process: processSignalsTrigger, HasDependencyId: false},
		cfg.WithSchema(signalsTriggerSchema))
}

var signalsTriggerSchema = cfg.TypeSchema{
	Sample:  &cfg.SignalsDefinition{},
	Summary: "Maps OS signals to actions.",
	DocPage: "trigger.md#trigger-signals",
	Doc: `Each attribute names a signal and gives the action to evaluate when it
arrives. Which signals exist varies by OS.

Several ` + "`trigger \"signals\"`" + ` blocks may coexist, but a given signal may be
handled in only one non-disabled block. SIGINT and SIGTERM are reserved for
graceful shutdown — react to those with ` + "`trigger \"shutdown\"`" + `.

Does **not** create a ` + "`trigger.<name>`" + ` value.`,
	Attrs: map[string]cfg.AttrMeta{
		"SIGHUP": {
			Summary: "Evaluated on SIGHUP.",
			Doc:     "Conventionally a request to reload configuration or reopen log files.",
			Hint:    cfg.HintActionExpression,
			Context: "trigger-signals",
		},
		"SIGINFO": {
			Summary: "Evaluated on SIGINFO.",
			Doc:     "A BSD/macOS status-request signal, typically sent with Ctrl-T. Not available on Linux.",
			Hint:    cfg.HintActionExpression,
			Context: "trigger-signals",
		},
		"SIGUSR1": {
			Summary: "Evaluated on SIGUSR1.",
			Doc:     "Reserved for the application; give it whatever meaning suits.",
			Hint:    cfg.HintActionExpression,
			Context: "trigger-signals",
		},
		"SIGUSR2": {
			Summary: "Evaluated on SIGUSR2.",
			Doc:     "Reserved for the application; give it whatever meaning suits.",
			Hint:    cfg.HintActionExpression,
			Context: "trigger-signals",
		},
		"disabled": cfg.DisabledAttr,
	},
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
