package cron

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/robfig/cron/v3"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterTriggerType("cron", cfg.TriggerRegistration{Process: processCronTrigger, HasDependencyId: false})
}

func processCronTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	cronDef := CronDefinition{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &cronDef)
	if diags.HasErrors() {
		return diags
	}
	cronDef.Name = block.Labels[1]

	cronObj, addDiags := BuildCron(config, block, &cronDef, triggerDef.TracerProvider)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	config.Startables = append(config.Startables, cfg.NewErrorlessStartable(cronObj))
	return diags
}

type CronDefinition struct {
	Name     string
	Timezone string             `hcl:"timezone,optional"`
	At       []CronAtDefinition `hcl:"at,block"`
}

type CronAtDefinition struct {
	Schedule string         `hcl:"schedule,label"`
	Name     string         `hcl:"name,label"`
	Action   hcl.Expression `hcl:"action"`
	DefRange hcl.Range      `hcl:",def_range"`
}

func BuildCron(config *cfg.Config, block *hcl.Block, cronDef *CronDefinition, tp trace.TracerProvider) (*cron.Cron, hcl.Diagnostics) {
	cronLogger := NewZapCronLogger(config.Logger)

	cronParser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)

	if cronDef.Timezone == "" {
		cronDef.Timezone = "Local"
	}

	diags := hcl.Diagnostics{}

	location, err := time.LoadLocation(cronDef.Timezone)
	if err != nil {
		diags = diags.Append(
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid timezone",
				Detail:   fmt.Sprintf("Invalid timezone: %s", cronDef.Timezone),
				Subject:  &block.DefRange,
			},
		)
	}

	cronObj := cron.New(cron.WithLogger(cronLogger), cron.WithParser(cronParser), cron.WithLocation(location))

	for _, atBlock := range cronDef.At {
		action := atBlock.Action
		if action == nil {
			diags = diags.Append(
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid at block",
					Detail:   "Cron At block must have an expression action attribute",
					Subject:  &atBlock.DefRange,
				},
			)
			continue
		}

		atAction := &AtAction{
			config:         config,
			action:         action,
			cronName:       cronDef.Name,
			atName:         atBlock.Name,
			tracerProvider: tp,
		}

		cronObj.AddJob(atBlock.Schedule, atAction)
	}

	return cronObj, diags
}

type AtAction struct {
	config         *cfg.Config
	action         hcl.Expression
	cronName       string
	atName         string
	tracerProvider trace.TracerProvider
}

func (a *AtAction) Run() {
	a.config.Logger.Debug("Executing action", zap.String("cron", a.cronName), zap.String("at", a.atName))

	spanCtx, stopSpan := hclutil.StartTriggerSpan(context.Background(), a.tracerProvider, "cron", a.cronName+"/"+a.atName)

	evalCtx, err := hclutil.NewEvalContext(spanCtx).
		WithStringAttribute("cron_name", a.cronName).
		WithStringAttribute("at_name", a.atName).
		BuildEvalContext(a.config.EvalCtx())
	if err != nil {
		a.config.Logger.Error("Error building evaluation context", zap.Error(err))
		stopSpan(err)
		return
	}

	value, diags := a.action.Value(evalCtx)
	if diags.HasErrors() {
		a.config.Logger.Error("Error executing action", zap.Error(diags))
		stopSpan(diags)
		return
	}

	stopSpan(nil)
	a.config.Logger.Debug("Action executed", zap.String("cron", a.cronName), zap.String("at", a.atName), zap.Any("result", value))
}

// ZapCronLogger adapts a zap.Logger to implement the cron.Logger interface.
type ZapCronLogger struct {
	logger *zap.Logger
}

// NewZapCronLogger creates a new ZapCronLogger that wraps the given zap.Logger.
func NewZapCronLogger(logger *zap.Logger) *ZapCronLogger {
	return &ZapCronLogger{logger: logger}
}

func (z *ZapCronLogger) Info(msg string, keysAndValues ...interface{}) {
	fields := make([]zap.Field, 0, len(keysAndValues)/2)
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		if key, ok := keysAndValues[i].(string); ok {
			fields = append(fields, zap.Any(key, keysAndValues[i+1]))
		}
	}
	z.logger.Debug(msg, fields...)
}

func (z *ZapCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	fields := make([]zap.Field, 0, len(keysAndValues)/2+1)
	fields = append(fields, zap.Error(err))
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		if key, ok := keysAndValues[i].(string); ok {
			fields = append(fields, zap.Any(key, keysAndValues[i+1]))
		}
	}
	z.logger.Error(msg, fields...)
}
