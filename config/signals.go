package config

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/tsarna/vinculum/platform"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func init() {
	// SetSignalAction below builds the context a signal action sees, so its
	// shape is described here rather than in the triggers/signals package that
	// registers the block. Note there is no ctx.trigger or ctx.name: a signal
	// handler is identified by the signal, not by the block it was declared in.
	RegisterContextSchema("trigger-signals", ContextSchema{
		Summary: "Evaluated when the named signal arrives.",
		Fields: []ContextField{
			{Name: "signal", Type: attrTypeString, Summary: "Signal name, e.g. `\"SIGHUP\"`."},
			{
				Name: "signal_num", Type: attrTypeNumber,
				Summary: "OS-level signal number.",
				Doc:     "Numbers vary by platform; `sys.signals.SIGHUP` is the portable way to name one.",
			},
		},
	})
}

type SignalsDefinition struct {
	SigHup   hcl.Expression `hcl:"SIGHUP,optional"`
	SigInfo  hcl.Expression `hcl:"SIGINFO,optional"`
	SigUsr1  hcl.Expression `hcl:"SIGUSR1,optional"`
	SigUsr2  hcl.Expression `hcl:"SIGUSR2,optional"`
	Disabled bool           `hcl:"disabled,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type SignalActionHandler struct {
	Ctx            context.Context
	Logger         *zap.Logger
	UserLogger     *zap.Logger
	SignalActions  map[platform.Signal]hcl.Expression
	SignalCtx      map[platform.Signal]*hcl.EvalContext
	SigChannel     chan os.Signal
	AddedStartable bool
	TracerProvider trace.TracerProvider
	// Files is the source file map for rendering a failing signal action against
	// its own line, set from the Config during Build(). Nil for a handler not
	// built that way (the diagnostic writer tolerates a nil map).
	Files map[string]*hcl.File
}

func NewSignalActionHandler(logger, userLogger *zap.Logger) *SignalActionHandler {
	return &SignalActionHandler{
		Logger:        logger,
		UserLogger:    userLogger,
		Ctx:           context.Background(),
		SignalActions: make(map[platform.Signal]hcl.Expression),
		SignalCtx:     make(map[platform.Signal]*hcl.EvalContext),
		SigChannel:    make(chan os.Signal, 16),
	}
}

func (config *Config) SetSignalAction(sigName string, action hcl.Expression) hcl.Diagnostics {
	if !IsExpressionProvided(action) {
		return nil
	}

	signalNum := platform.SignalNum(sigName)
	if signalNum == 0 {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid signal name",
			Detail:   fmt.Sprintf("Invalid signal name: %s", sigName),
			Subject:  action.Range().Ptr(),
		}}
	}

	if _, ok := config.SigActions.SignalActions[signalNum]; ok {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Signal already defined",
			Detail:   fmt.Sprintf("Signal %s already defined", sigName),
			Subject:  action.Range().Ptr(),
		}}
	}

	config.SigActions.SignalActions[signalNum] = action

	ctx, err := hclutil.NewEvalContext(config.SigActions.Ctx).
		WithStringAttribute("signal", sigName).
		WithAttribute("signal_num", cty.NumberIntVal(int64(signalNum))).
		BuildEvalContext(config.evalCtx)
	if err != nil {
		return hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Error building signal context", Detail: err.Error()}}
	}

	config.SigActions.SignalCtx[signalNum] = ctx

	if !config.SigActions.AddedStartable {
		config.Logger.Info("Adding signal action handler to startables")

		config.SigActions.AddedStartable = true
		config.Startables = append(config.Startables, config.SigActions)
	}

	return nil
}

func (sa *SignalActionHandler) Start() error {
	for sig := range sa.SignalActions {
		signal.Notify(sa.SigChannel, sig)
	}

	go func() {
		sa.Logger.Info("Signal notification goroutine started")

		for {
			select {
			case sig := <-sa.SigChannel:
				go func() {
					platformSig := platform.FromOsSignal(sig)
					if platformSig == 0 {
						sa.UserLogger.Error("Invalid signal", zap.String("signal", sig.String()))
						return
					}

					sa.Logger.Debug("Signal received", zap.String("signal", platformSig.String()))

					sigExpr, ok := sa.SignalActions[platformSig]
					if !ok {
						sa.UserLogger.Error("Signal action expression not found", zap.String("signal", platformSig.String()))
						return
					}

					evalCtx, ok := sa.SignalCtx[platformSig]
					if !ok {
						sa.UserLogger.Error("Signal context not found", zap.String("signal", platformSig.String()))
						return
					}

					_, stopSpan := hclutil.StartTriggerSpan(context.Background(), sa.TracerProvider, "signal", platformSig.String())
					result, diags := sigExpr.Value(evalCtx)
					if diags.HasErrors() {
						sa.UserLogger.Error("Error executing signal action", actionErrorField(diags, sa.Files))
						stopSpan(diags)
					} else {
						stopSpan(nil)
					}

					if result.Type() != cty.NilType {
						sa.Logger.Debug("Signal action expression result", zap.String("signal", platformSig.String()), zap.Any("result", result))
						return
					}
				}()
			case <-sa.Ctx.Done():
				return
			}
		}
	}()

	return nil
}
