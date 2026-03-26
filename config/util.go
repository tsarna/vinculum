package config

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/sosodev/duration"
	bus "github.com/tsarna/vinculum-bus"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/tsarna/vinculum/internal/hclutil"
	"github.com/zclconf/go-cty/cty"
)

// IsExpressionProvided checks if an HCL expression was actually provided in the configuration.
// HCL creates empty expression objects for optional fields that aren't specified,
// but empty expressions have Start.Byte == End.Byte (zero-length range).
// Real expressions have End.Byte > Start.Byte (non-zero length range).
func IsExpressionProvided(expr hcl.Expression) bool {
	return hclutil.IsExpressionProvided(expr)
}

// ParseDuration parses a duration from an HCL expression.
// It supports three formats:
// 1. Numbers (interpreted as seconds)
// 2. Strings starting with "P" (ISO 8601 durations using github.com/sosodev/duration)
// 3. Other strings (Go's native duration parsing)
func (c *Config) ParseDuration(expr hcl.Expression) (time.Duration, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	// Evaluate the expression to get the cty.Value
	val, evalDiags := expr.Value(c.evalCtx)
	diags = diags.Extend(evalDiags)
	if evalDiags.HasErrors() {
		return 0, diags
	}

	// Handle different value types
	switch val.Type() {
	case cty.Number:
		// Numbers are treated as seconds
		seconds, accuracy := val.AsBigFloat().Float64()
		if accuracy != big.Exact {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagWarning,
				Summary:  "Duration precision loss",
				Detail:   "The number provided for duration may have lost precision when converted to seconds",
				Subject:  expr.Range().Ptr(),
			})
		}
		if seconds < 0 {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid duration",
				Detail:   "Duration must be positive",
				Subject:  expr.Range().Ptr(),
			})
			return 0, diags
		}
		return time.Duration(seconds * float64(time.Second)), diags

	case cty.String:
		str := val.AsString()
		str = strings.TrimSpace(str)

		if strings.HasPrefix(str, "P") {
			// ISO 8601 duration format
			dur, err := duration.Parse(str)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid ISO 8601 duration",
					Detail:   fmt.Sprintf("Failed to parse ISO 8601 duration '%s': %v", str, err),
					Subject:  expr.Range().Ptr(),
				})
				return 0, diags
			}

			// Convert to time.Duration
			timeDuration := dur.ToTimeDuration()
			if timeDuration < 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid duration",
					Detail:   "Duration must be positive",
					Subject:  expr.Range().Ptr(),
				})
				return 0, diags
			}
			return timeDuration, diags

		} else {
			// Go's native duration parsing
			timeDuration, err := time.ParseDuration(str)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid duration format",
					Detail:   fmt.Sprintf("Failed to parse duration '%s': %v. Expected a number (seconds), ISO 8601 duration (e.g., 'PT5M'), or Go duration (e.g., '5m')", str, err),
					Subject:  expr.Range().Ptr(),
				})
				return 0, diags
			}
			if timeDuration < 0 {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid duration",
					Detail:   "Duration must be positive",
					Subject:  expr.Range().Ptr(),
				})
				return 0, diags
			}
			return timeDuration, diags
		}

	case timecty.DurationCapsuleType:
		d, _ := timecty.GetDuration(val) // type already confirmed by case
		return d, diags

	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid duration type",
			Detail:   fmt.Sprintf("Duration must be a number (seconds), string, or duration value, got %s", val.Type().FriendlyName()),
			Subject:  expr.Range().Ptr(),
		})
		return 0, diags
	}
}

// parseDurationFromValue converts an already-evaluated cty.Value to a time.Duration.
// Supports numbers (seconds), strings (Go or ISO 8601), and duration capsules.
// Used when the expression must be evaluated against a dynamic context before conversion.
func parseDurationFromValue(val cty.Value) (time.Duration, error) {
	switch val.Type() {
	case cty.Number:
		seconds, _ := val.AsBigFloat().Float64()
		if seconds < 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return time.Duration(seconds * float64(time.Second)), nil

	case cty.String:
		str := strings.TrimSpace(val.AsString())
		if strings.HasPrefix(str, "P") {
			dur, err := duration.Parse(str)
			if err != nil {
				return 0, fmt.Errorf("invalid ISO 8601 duration %q: %w", str, err)
			}
			d := dur.ToTimeDuration()
			if d < 0 {
				return 0, fmt.Errorf("duration must be positive")
			}
			return d, nil
		}
		d, err := time.ParseDuration(str)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", str, err)
		}
		if d < 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return d, nil

	case timecty.DurationCapsuleType:
		d, _ := timecty.GetDuration(val)
		return d, nil

	default:
		return 0, fmt.Errorf("duration must be a number, string, or duration value, got %s", val.Type().FriendlyName())
	}
}

// IsConstantExpression checks if an expression is a constant (evaluatable with nil context).
// Returns the value and true if constant, or cty.NilVal and false otherwise.
func IsConstantExpression(expr hcl.Expression) (cty.Value, bool) {
	return hclutil.IsConstantExpression(expr)
}

type ReconnectDefinition struct {
	InitialDelay  hcl.Expression `hcl:"initial_delay,optional"`
	MaxDelay      hcl.Expression `hcl:"max_delay,optional"`
	BackoffFactor *float64       `hcl:"backoff_factor,optional"`
	MaxRetries    *int           `hcl:"max_retries,optional"`
	DefRange      hcl.Range      `hcl:",def_range"`
}

func (c *Config) CreateReconnector(def ReconnectDefinition) (*bus.AutoReconnector, hcl.Diagnostics) {
	builder := bus.NewAutoReconnector()
	if IsExpressionProvided(def.InitialDelay) {
		duration, diags := c.ParseDuration(def.InitialDelay)
		if diags.HasErrors() {
			return nil, diags
		}
		builder = builder.WithInitialDelay(duration)
	}
	if IsExpressionProvided(def.MaxDelay) {
		duration, diags := c.ParseDuration(def.MaxDelay)
		if diags.HasErrors() {
			return nil, diags
		}
		builder = builder.WithMaxDelay(duration)
	}
	if def.BackoffFactor != nil {
		builder = builder.WithBackoffFactor(*def.BackoffFactor)
	}
	if def.MaxRetries != nil {
		builder = builder.WithMaxRetries(*def.MaxRetries)
	}

	return builder.Build(), nil
}
