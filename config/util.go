package config

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/sosodev/duration"
	"github.com/zclconf/go-cty/cty"
)

// IsExpressionProvided checks if an HCL expression was actually provided in the configuration.
// HCL creates empty expression objects for optional fields that aren't specified,
// but empty expressions have Start.Byte == End.Byte (zero-length range).
// Real expressions have End.Byte > Start.Byte (non-zero length range).
func IsExpressionProvided(expr hcl.Expression) bool {
	return expr != nil && expr.Range().End.Byte > expr.Range().Start.Byte
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

	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid duration type",
			Detail:   fmt.Sprintf("Duration must be a number (seconds) or string, got %s", val.Type().FriendlyName()),
			Subject:  expr.Range().Ptr(),
		})
		return 0, diags
	}
}

// If the expression is a constant, return the value and true, otherwise return false
func IsConstantExpression(expr hcl.Expression) (cty.Value, bool) {
	// Try to evaluate with nil context - only works for literals/constants
	val, diags := expr.Value(nil)
	if diags.HasErrors() {
		return cty.NilVal, false
	}

	return val, true
}
