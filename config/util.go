package config

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/sosodev/duration"
	timecty "github.com/tsarna/time-cty-funcs"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/hclutil"
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

// ParseDurationFromValue converts an already-evaluated cty.Value to a time.Duration.
// Supports numbers (seconds), strings (Go or ISO 8601), and duration capsules.
// Used when the expression must be evaluated against a dynamic context before conversion.
func ParseDurationFromValue(val cty.Value) (time.Duration, error) {
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

func init() {
	RegisterSharedBlockSchema(&ReconnectDefinition{}, reconnectSchema)
}

var reconnectSchema = TypeSchema{
	Summary: "How to retry a lost connection.",
	Doc: `Retries use exponential backoff: the first retry waits ` + "`initial_delay`" + `, and
each subsequent wait is multiplied by ` + "`backoff_factor`" + ` up to ` + "`max_delay`" + `.`,
	Attrs: map[string]AttrMeta{
		"initial_delay": {
			Summary: "Wait before the first retry.",
			Hint:    HintDuration,
		},
		"max_delay": {
			Summary: "Ceiling on the wait between retries.",
			Hint:    HintDuration,
		},
		"backoff_factor": {
			Summary: "Multiplier applied to the wait after each failed attempt.",
		},
		"max_retries": {
			Summary: "Give up after this many attempts.",
			Doc:     "Retries forever when omitted.",
		},
	},
}

// The schedule a `reconnect` block describes when it leaves an attribute out.
//
// One set of numbers for every client, because a user writes one block:
// `reconnect {}` on a vws client and on an mqtt client mean the same thing.
// 60s matches vinculum-rabbitmq's own DefaultReconnectBackoff, so the block
// agrees with a library it configures rather than overriding it.
//
// These apply only when the block is *present*. With no block at all each
// library keeps its own schedule, which is a different question and not ours.
const (
	defaultReconnectInitialDelay  = time.Second
	defaultReconnectMaxDelay      = 60 * time.Second
	defaultReconnectBackoffFactor = 2.0
)

// reconnectSchedule is a reconnect block resolved into concrete numbers, with
// the defaults above filled in for whatever it omitted.
type reconnectSchedule struct {
	initialDelay  time.Duration
	maxDelay      time.Duration
	backoffFactor float64
}

// resolveReconnectSchedule is the single place a reconnect block becomes
// numbers. Both integration points below go through it, which is what keeps them
// from disagreeing about what an omitted attribute means.
func (c *Config) resolveReconnectSchedule(def *ReconnectDefinition) (reconnectSchedule, hcl.Diagnostics) {
	s := reconnectSchedule{
		initialDelay:  defaultReconnectInitialDelay,
		maxDelay:      defaultReconnectMaxDelay,
		backoffFactor: defaultReconnectBackoffFactor,
	}

	if IsExpressionProvided(def.InitialDelay) {
		d, diags := c.ParseDuration(def.InitialDelay)
		if diags.HasErrors() {
			return s, diags
		}
		s.initialDelay = d
	}
	if IsExpressionProvided(def.MaxDelay) {
		d, diags := c.ParseDuration(def.MaxDelay)
		if diags.HasErrors() {
			return s, diags
		}
		s.maxDelay = d
	}
	if def.BackoffFactor != nil {
		s.backoffFactor = *def.BackoffFactor
	}

	return s, nil
}

// delay is the wait before a given 0-based attempt: exponential, clamped.
func (s reconnectSchedule) delay(attempt int) time.Duration {
	d := time.Duration(float64(s.initialDelay) * math.Pow(s.backoffFactor, float64(attempt)))
	if d > s.maxDelay {
		return s.maxDelay
	}
	return d
}

// ReconnectBackoffFunc lowers a reconnect block into the exponential backoff
// function that a protocol client library takes: attempt number in, wait
// duration out. Returns nil when there is no block, which every such library
// reads as "use your own default schedule".
//
// This is one of two ways a reconnect block reaches a client, and what differs
// between them is the integration point, not the schedule. A bus client takes
// a bus.AutoReconnector, which owns the retry loop and can therefore stop —
// that is CreateReconnector below. A protocol client owns its own loop and only
// asks how long to wait next, so a func(int) time.Duration is all it can
// accept.
//
// max_retries does not travel with the schedule, because giving up cannot be
// expressed in a duration. It is passed separately — see ReconnectMaxAttempts.
func (c *Config) ReconnectBackoffFunc(def *ReconnectDefinition) (func(int) time.Duration, hcl.Diagnostics) {
	if def == nil {
		return nil, nil
	}
	schedule, diags := c.resolveReconnectSchedule(def)
	if diags.HasErrors() {
		return nil, diags
	}
	return schedule.delay, nil
}

// ReconnectMaxAttempts is max_retries as a client library's max-attempts field
// wants it: the number of attempts to allow, where zero or negative means
// unlimited. Zero is unlimited rather than "do not reconnect" because that is
// what bus.AutoReconnector has always meant by it (`maxRetries > 0 && ...`), and
// because it makes an omitted attribute mean what it meant before the attribute
// was honored at all.
//
// A companion to ReconnectBackoffFunc rather than part of it: a backoff function
// answers "how long until the next attempt", and stopping is not an answer to
// that question. Clients pass both.
func ReconnectMaxAttempts(def *ReconnectDefinition) int {
	if def == nil || def.MaxRetries == nil || *def.MaxRetries < 0 {
		return 0
	}
	return *def.MaxRetries
}

// CreateReconnector lowers a reconnect block into the bus.AutoReconnector a bus
// client takes. Unlike ReconnectBackoffFunc's consumers, this one is handed the
// whole schedule at once because the reconnector owns the retry loop itself.
//
// Every value is set explicitly rather than left to bus.NewAutoReconnector's
// own defaults, so the block means the same here as it does on a protocol
// client. max_retries goes through ReconnectMaxAttempts for the same reason:
// one attribute, one normalization, whichever client is asking. The builder
// reads anything not greater than zero as unlimited, which is what an absent,
// zero, or negative max_retries resolves to.
func (c *Config) CreateReconnector(def ReconnectDefinition) (*bus.AutoReconnector, hcl.Diagnostics) {
	schedule, diags := c.resolveReconnectSchedule(&def)
	if diags.HasErrors() {
		return nil, diags
	}

	return bus.NewAutoReconnector().
		WithInitialDelay(schedule.initialDelay).
		WithMaxDelay(schedule.maxDelay).
		WithBackoffFactor(schedule.backoffFactor).
		WithMaxRetries(ReconnectMaxAttempts(&def)).
		Build(), nil
}
