package config

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/vinculum/platform"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

type SignalsDefinition struct {
	SigHup   hcl.Expression `hcl:"SIGHUP,optional"`
	SigInfo  hcl.Expression `hcl:"SIGINFO,optional"`
	SigUsr1  hcl.Expression `hcl:"SIGUSR1,optional"`
	SigUsr2  hcl.Expression `hcl:"SIGUSR2,optional"`
	Disabled bool           `hcl:"disabled,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type SignalsBlockHandler struct {
	BlockHandlerBase
}

func NewSignalsBlockHandler() *SignalsBlockHandler {
	return &SignalsBlockHandler{}
}

type SignalActionHandler struct {
	Ctx            context.Context
	Logger         *zap.Logger
	SignalActions  map[platform.Signal]hcl.Expression
	SignalCtx      map[platform.Signal]*hcl.EvalContext
	SigChannel     chan os.Signal
	AddedStartable bool
}

func NewSignalActionHandler(logger *zap.Logger) *SignalActionHandler {
	return &SignalActionHandler{
		Logger:        logger,
		Ctx:           context.Background(),
		SignalActions: make(map[platform.Signal]hcl.Expression),
		SignalCtx:     make(map[platform.Signal]*hcl.EvalContext),
		SigChannel:    make(chan os.Signal, 16),
	}
}

func (h *SignalsBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	diags := hcl.Diagnostics{}

	signalsDef := SignalsDefinition{}
	diags = diags.Extend(gohcl.DecodeBody(block.Body, config.evalCtx, &signalsDef))
	if diags.HasErrors() {
		return diags
	}

	if signalsDef.Disabled {
		return nil
	}

	diags = diags.Extend(config.SetSignalAction("SIGHUP", signalsDef.SigHup))
	diags = diags.Extend(config.SetSignalAction("SIGINFO", signalsDef.SigInfo))
	diags = diags.Extend(config.SetSignalAction("SIGUSR1", signalsDef.SigUsr1))
	diags = diags.Extend(config.SetSignalAction("SIGUSR2", signalsDef.SigUsr2))

	return diags
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

	ctx, diags := NewContext(config.SigActions.Ctx).
		WithStringAttribute("signal", sigName).
		WithAttribute("signal_num", cty.NumberIntVal(int64(signalNum))).
		BuildEvalContext(config.evalCtx)

	if diags.HasErrors() {
		return diags
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
						sa.Logger.Error("Invalid signal", zap.String("signal", sig.String()))
						return
					}

					sa.Logger.Debug("Signal received", zap.String("signal", platformSig.String()))

					sigExpr, ok := sa.SignalActions[platformSig]
					if !ok {
						sa.Logger.Error("Signal action expression not found", zap.String("signal", platformSig.String()))
						return
					}

					evalCtx, ok := sa.SignalCtx[platformSig]
					if !ok {
						sa.Logger.Error("Signal context not found", zap.String("signal", platformSig.String()))
						return
					}

					result, diags := sigExpr.Value(evalCtx)
					if diags.HasErrors() {
						sa.Logger.Error("Error executing signal action", zap.Error(diags))
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
