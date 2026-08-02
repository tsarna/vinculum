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
	cfg.RegisterTriggerType("cron", cfg.TriggerRegistration{Process: processCronTrigger, HasDependencyId: false},
		cfg.WithSchema(cronTriggerSchema))

	// One cron block holds many rules, so the context names the rule that
	// fired rather than the block — there is no ctx.trigger or ctx.name here.
	cfg.RegisterContextSchema("trigger-cron", cfg.ContextSchema{
		Summary: "Evaluated each time an `at` rule's schedule fires.",
		Fields: []cfg.ContextField{
			{Name: "cron_name", Type: "string", Summary: "Name of the enclosing `trigger \"cron\"` block."},
			{Name: "at_name", Type: "string", Summary: "Name of the `at` rule that fired."},
		},
	})
}

var cronTriggerSchema = cfg.TypeSchema{
	Sample:  &CronDefinition{},
	Summary: "A cron-style scheduler holding one or more scheduled rules.",
	Doc: `Unlike the other trigger types, one block carries many schedules: each ` + "`at`" + `
sub-block is an independent rule with its own schedule and action. Declaring
several ` + "`trigger \"cron\"`" + ` blocks is useful mainly to run rules in different
time zones.

Does **not** create a ` + "`trigger.<name>`" + ` value.`,
	Attrs: map[string]cfg.AttrMeta{
		"timezone": {
			Summary: "IANA time zone the schedules are interpreted in.",
			Doc:     "For example `\"UTC\"` or `\"America/New_York\"`. Defaults to the host's local time zone.",
		},
	},
	Blocks: map[string]cfg.TypeSchema{
		"at": {
			Summary: "One scheduled rule.",
			Doc: `The first label is the schedule and the second names the rule. A schedule is
standard five-field cron (minute, hour, day-of-month, month, day-of-week), the
six-field form where the first field is seconds, or a descriptor such as
` + "`@hourly`" + ` or ` + "`@every 30s`" + `.`,
			Attrs: map[string]cfg.AttrMeta{
				"action": {
					Summary: "Evaluated each time this rule's schedule fires.",
					Hint:    cfg.HintActionExpression,
					Context: "trigger-cron",
				},
			},
		},
	},
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
		a.config.UserLogger.Error("Error building evaluation context", zap.Error(err))
		stopSpan(err)
		return
	}

	value, diags := a.action.Value(evalCtx)
	if diags.HasErrors() {
		a.config.UserLogger.Error("Error executing action", a.config.ActionError(diags))
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
