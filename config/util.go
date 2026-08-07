package config

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
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

	// BackoffFactorRange underlines the attribute itself when its value is
	// rejected. A *float64 carries no range of its own, and pointing at the
	// whole block would leave a reader of a five-attribute block looking for
	// which one was meant.
	BackoffFactorRange hcl.Range `hcl:"backoff_factor,attr_range"`
}

func init() {
	RegisterSharedBlockSchema(&ReconnectDefinition{}, reconnectSchema)
}

// The schedule a `reconnect` block describes when it leaves an attribute out.
//
// One set of numbers for every client, because a user writes one block:
// `reconnect {}` on a vws client and on an mqtt client mean the same thing.
// 60s matches vinculum-rabbitmq's own DefaultReconnectBackoff, so the block
// agrees with a library it configures rather than overriding it.
//
// ReconnectAttrs states these as the block's documented defaults, so a change
// here reaches `vinculum man` and the generated pages.
//
// These apply only when the block is *present*. With no block at all each
// library keeps its own schedule, which is a different question and not ours.
const (
	defaultReconnectInitialDelay  = time.Second
	defaultReconnectMaxDelay      = 60 * time.Second
	defaultReconnectBackoffFactor = 2.0
)

// ReconnectAttrs documents the shared `reconnect` block. Exported so a host can
// layer over it with MergeAttrs rather than restating the descriptions, though
// no host needs to: one block means one schedule on every client, so there is
// nothing to specialize.
//
// The stated defaults are the constants above rather than literals repeating
// them. A wrong default is worse than none, since `vinculum man` and the
// generated pages present it as fact, and two copies of a number are one edit
// away from disagreeing.
var ReconnectAttrs = map[string]AttrMeta{
	"initial_delay": {
		Summary: "Wait before the first retry.",
		Doc: "Must be greater than zero: every later wait is this one multiplied by " +
			"`backoff_factor`, so a zero here stays zero however many times it is " +
			"multiplied, and the client retries continuously.",
		Hint:    HintDuration,
		Default: durationDefault(defaultReconnectInitialDelay),
	},
	"max_delay": {
		Summary: "Ceiling on the wait between retries.",
		Hint:    HintDuration,
		Default: durationDefault(defaultReconnectMaxDelay),
	},
	"backoff_factor": {
		Summary: "Multiplier applied to the wait after each failed attempt.",
		Doc: "Must be at least 1. Use `1` for a constant delay between retries; a " +
			"factor below 1 would shorten the wait after every failure rather than " +
			"lengthen it, reaching zero and retrying continuously, so it is rejected.",
		Default: floatDefault(defaultReconnectBackoffFactor),
	},
	"max_retries": {
		Summary: "Give up after this many attempts.",
		Doc: "Retries forever when omitted, and also when set to zero or a " +
			"negative number. Counts attempts to recover a *lost* connection; the " +
			"initial connection is retried regardless. Giving up is quiet and " +
			"final — the client logs an error and stays down, and the process keeps " +
			"running.",
	},
}

// durationDefault writes a duration the way a configuration would, which is not
// always how Go prints one: (60 * time.Second).String() is "1m0s", and a default
// that does not look like the value a reader would type is a poor default to
// show them.
func durationDefault(d time.Duration) string {
	if d >= time.Second && d%time.Second == 0 {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return d.String()
}

// floatDefault keeps a whole-numbered float looking like a float, since Go
// prints 2.0 as "2" and a multiplier documented as `2` invites the reader to
// wonder whether the slot takes an integer.
func floatDefault(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

var reconnectSchema = TypeSchema{
	Summary: "How to retry a lost connection.",
	Doc: `Retries use exponential backoff: the first retry waits ` + "`initial_delay`" + `, and
each subsequent wait is multiplied by ` + "`backoff_factor`" + ` up to ` + "`max_delay`" + `.

The defaults below apply once the block is present. Omit it entirely and the
underlying client library's own reconnect behaviour is used instead.`,
	Attrs: ReconnectAttrs,
}

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
//
// It is also where a schedule that cannot back off is rejected. A wait that
// reaches zero is a reconnect loop with nothing in it, spinning against a
// service that is already down, and there are two ways to write one: a factor
// below 1, which shrinks the wait until it underflows to zero, and a zero
// initial delay, which can never grow because zero times anything is zero.
// Neither is a schedule anyone means to ask for, and both are quiet at runtime
// — the client simply retries forever at full speed — so they are worth an
// error at load rather than a discovery in production.
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
		if d <= 0 {
			return s, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid reconnect initial_delay",
				Detail: "initial_delay must be greater than zero. Every later wait is " +
					"this one multiplied by backoff_factor, so a zero here stays zero and " +
					"the client retries continuously.",
				Subject: def.InitialDelay.Range().Ptr(),
			}}
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
		// NaN fails this too, since it compares false against everything.
		if !(*def.BackoffFactor >= 1) {
			return s, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid reconnect backoff_factor",
				Detail: fmt.Sprintf("backoff_factor must be at least 1, got %s. A factor "+
					"below 1 shortens the wait after every failure instead of lengthening "+
					"it, reaching zero and retrying continuously; use 1 for a constant "+
					"delay between retries.", floatDefault(*def.BackoffFactor)),
				Subject: def.BackoffFactorRange.Ptr(),
			}}
		}
		s.backoffFactor = *def.BackoffFactor
	}

	return s, nil
}

// delay is the wait before a given 0-based attempt: exponential, clamped.
//
// The clamp is applied to the float, before the conversion, and has to be.
// initial_delay × factor^attempt passes math.MaxInt64 at attempt 34 on the
// default schedule, and Go leaves an out-of-range float-to-integer conversion
// implementation-defined: amd64 yields math.MinInt64 while arm64 saturates to
// math.MaxInt64. A clamp on the far side of that conversion therefore holds on
// one architecture and inverts on the other, returning a negative wait — which
// is a retry loop with no wait in it.
//
// The bus integration point has no such problem because it multiplies and
// clamps once per attempt, so its running delay never leaves the range. Only a
// stateless func(int) duration has to raise a power, and only raising a power
// can overshoot this far.
func (s reconnectSchedule) delay(attempt int) time.Duration {
	d := float64(s.initialDelay) * math.Pow(s.backoffFactor, float64(attempt))
	// Written as a rejected "below the ceiling" rather than an accepted "at or
	// above" it so that a NaN — which compares false against everything — lands
	// on the ceiling with every other unusable value instead of being converted.
	if !(d < float64(s.maxDelay)) {
		return s.maxDelay
	}
	return time.Duration(d)
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
