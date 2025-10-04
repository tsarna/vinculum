package config

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

type CronDefinition struct {
	Name     string             `hcl:",label"`
	Timezone string             `hcl:"timezone,optional"`
	At       []CronAtDefinition `hcl:"at,block"`
}

type CronAtDefinition struct {
	Schedule string         `hcl:"schedule,label"`
	Name     string         `hcl:"name,label"`
	Action   hcl.Expression `hcl:"action"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type CronBlockHandler struct {
	BlockHandlerBase
}

func NewCronBlockHandler() *CronBlockHandler {
	return &CronBlockHandler{}
}

func (h *CronBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	cronDef := CronDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &cronDef)
	if diags.HasErrors() {
		return diags
	}

	// Manually set the name from the block label since DecodeBody doesn't handle labels
	if len(block.Labels) > 0 {
		cronDef.Name = block.Labels[0]
	}

	cronObj, addDiags := h.BuildCron(config, block, &cronDef)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	config.Crons[cronDef.Name] = cronObj
	config.Startables = append(config.Startables, NewErrorlessStartable(cronObj))

	return diags
}

func (h *CronBlockHandler) BuildCron(config *Config, block *hcl.Block, cronDef *CronDefinition) (*cron.Cron, hcl.Diagnostics) {
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
			config:   config,
			action:   action,
			cronName: cronDef.Name,
			atName:   atBlock.Name,
		}

		cronObj.AddJob(atBlock.Schedule, atAction)
	}

	return cronObj, diags
}

type AtAction struct {
	config   *Config
	action   hcl.Expression
	cronName string
	atName   string
}

func (a *AtAction) Run() {
	a.config.Logger.Debug("Executing action", zap.String("cron", a.cronName), zap.String("at", a.atName))

	evalCtx, diags := NewContext(context.Background()).
		WithStringAttribute("cron_name", a.cronName).
		WithStringAttribute("at_name", a.atName).
		BuildEvalContext(a.config.evalCtx)

	if diags.HasErrors() {
		a.config.Logger.Error("Error building evaluation context", zap.Error(diags))
		return
	}

	value, addDiags := a.action.Value(evalCtx)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		a.config.Logger.Error("Error executing action", zap.Error(diags))
		return
	}

	a.config.Logger.Debug("Action executed", zap.String("cron", a.cronName), zap.String("at", a.atName), zap.Any("result", value))
}

/// ZapCronLogger

// ZapCronLogger adapts a zap.Logger to implement the cron.Logger interface
type ZapCronLogger struct {
	logger *zap.Logger
}

// NewZapCronLogger creates a new ZapCronLogger that wraps the given zap.Logger
func NewZapCronLogger(logger *zap.Logger) *ZapCronLogger {
	return &ZapCronLogger{logger: logger}
}

// Info logs informational messages about cron's operation using zap's Info level
func (z *ZapCronLogger) Info(msg string, keysAndValues ...interface{}) {
	// Convert keysAndValues to zap fields
	fields := make([]zap.Field, 0, len(keysAndValues)/2)
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		if key, ok := keysAndValues[i].(string); ok {
			fields = append(fields, zap.Any(key, keysAndValues[i+1]))
		}
	}
	z.logger.Debug(msg, fields...)
}

// Error logs error conditions using zap's Error level
func (z *ZapCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	// Convert keysAndValues to zap fields and add the error
	fields := make([]zap.Field, 0, len(keysAndValues)/2+1)
	fields = append(fields, zap.Error(err))

	for i := 0; i < len(keysAndValues)-1; i += 2 {
		if key, ok := keysAndValues[i].(string); ok {
			fields = append(fields, zap.Any(key, keysAndValues[i+1]))
		}
	}
	z.logger.Error(msg, fields...)
}
