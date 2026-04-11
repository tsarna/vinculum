// Retry policy for the HTTP client.
//
// Retries are computed and executed by the verb functions in package
// functions, not as a *http.RoundTripper layer: the on_response hook
// needs access to the package's HCL eval context, which is awkward to
// thread through a transport stack but trivial at the verb function
// level. The math, defaults, and per-attempt decision logic live here
// so they're testable in isolation and reusable by mock clients.

package http

import (
	"fmt"
	"math"
	"math/rand"
	nethttp "net/http"
	"strconv"
	"time"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

// RetryPolicy describes how to retry failed HTTP requests.
//
// MaxAttempts == 1 means no retry: the initial attempt is performed and the
// result is returned regardless of outcome. This is the default for clients
// that did not configure a `retry { ... }` block.
type RetryPolicy struct {
	MaxAttempts        int
	InitialDelay       time.Duration
	MaxDelay           time.Duration
	BackoffFactor      float64
	Jitter             bool
	RetryOn            map[int]struct{}
	RespectRetryAfter  bool
	AllowNonIdempotent bool

	// OnResponse is an HCL expression evaluated by the verb function
	// after each attempt that returned a response. nil means no hook
	// is configured. The expression has access to ctx.response (the
	// in-flight response) and ctx.attempt (1-indexed) and may return
	// null/false (stop), true (retry with normal backoff), or a
	// number/duration (wait that long, then retry).
	OnResponse hcl.Expression
}

// retryBlock is the HCL-decoded form of a `retry { ... }` sub-block.
type retryBlock struct {
	MaxAttempts        *int           `hcl:"max_attempts,optional"`
	InitialDelay       hcl.Expression `hcl:"initial_delay,optional"`
	MaxDelay           hcl.Expression `hcl:"max_delay,optional"`
	BackoffFactor      *float64       `hcl:"backoff_factor,optional"`
	Jitter             *bool          `hcl:"jitter,optional"`
	RetryOn            []int          `hcl:"retry_on,optional"`
	RespectRetryAfter  *bool          `hcl:"respect_retry_after,optional"`
	AllowNonIdempotent *bool          `hcl:"allow_non_idempotent,optional"`
	OnResponse         hcl.Expression `hcl:"on_response,optional"`
	DefRange           hcl.Range      `hcl:",def_range"`
}

// noRetryPolicy is the policy used when a client did not configure any
// `retry { ... }` block at all: a single attempt, no retries.
func noRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:       1,
		InitialDelay:      200 * time.Millisecond,
		MaxDelay:          30 * time.Second,
		BackoffFactor:     2.0,
		Jitter:            true,
		RetryOn:           defaultRetryOnSet(),
		RespectRetryAfter: true,
	}
}

// retryEnabledDefaults is the policy used as the starting point when a
// client (or a per-call override) configures a `retry { ... }` block but
// omits some fields.
func retryEnabledDefaults() RetryPolicy {
	p := noRetryPolicy()
	p.MaxAttempts = 3
	return p
}

func defaultRetryOnSet() map[int]struct{} {
	return map[int]struct{}{
		nethttp.StatusTooManyRequests:     {}, // 429
		nethttp.StatusBadGateway:          {}, // 502
		nethttp.StatusServiceUnavailable:  {}, // 503
		nethttp.StatusGatewayTimeout:      {}, // 504
	}
}

// parseRetryBlock decodes a `retry { ... }` sub-block into a RetryPolicy.
// Returns the no-retry policy if b is nil.
func parseRetryBlock(config *cfg.Config, b *retryBlock) (RetryPolicy, hcl.Diagnostics) {
	if b == nil {
		return noRetryPolicy(), nil
	}

	p := retryEnabledDefaults()

	if b.MaxAttempts != nil {
		if *b.MaxAttempts < 1 {
			return p, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "retry.max_attempts must be >= 1",
				Subject:  &b.DefRange,
			}}
		}
		p.MaxAttempts = *b.MaxAttempts
	}

	if cfg.IsExpressionProvided(b.InitialDelay) {
		d, diags := config.ParseDuration(b.InitialDelay)
		if diags.HasErrors() {
			return p, diags
		}
		p.InitialDelay = d
	}

	if cfg.IsExpressionProvided(b.MaxDelay) {
		d, diags := config.ParseDuration(b.MaxDelay)
		if diags.HasErrors() {
			return p, diags
		}
		p.MaxDelay = d
	}

	if b.BackoffFactor != nil {
		if *b.BackoffFactor < 1.0 {
			return p, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "retry.backoff_factor must be >= 1.0",
				Subject:  &b.DefRange,
			}}
		}
		p.BackoffFactor = *b.BackoffFactor
	}

	if b.Jitter != nil {
		p.Jitter = *b.Jitter
	}

	if len(b.RetryOn) > 0 {
		p.RetryOn = make(map[int]struct{}, len(b.RetryOn))
		for _, code := range b.RetryOn {
			p.RetryOn[code] = struct{}{}
		}
	}

	if b.RespectRetryAfter != nil {
		p.RespectRetryAfter = *b.RespectRetryAfter
	}

	if b.AllowNonIdempotent != nil {
		p.AllowNonIdempotent = *b.AllowNonIdempotent
	}

	if cfg.IsExpressionProvided(b.OnResponse) {
		p.OnResponse = b.OnResponse
	}

	return p, nil
}

// MethodRetryable returns true if the given HTTP method is allowed to retry
// under this policy. Idempotent methods (GET, HEAD, OPTIONS, PUT, DELETE)
// are always retryable; POST, PATCH, and any other method only when
// AllowNonIdempotent is set.
func (p RetryPolicy) MethodRetryable(method string) bool {
	switch method {
	case nethttp.MethodGet, nethttp.MethodHead, nethttp.MethodOptions,
		nethttp.MethodPut, nethttp.MethodDelete:
		return true
	}
	return p.AllowNonIdempotent
}

// ShouldRetryStatus returns true if a response with this status code
// matches the policy's RetryOn set.
func (p RetryPolicy) ShouldRetryStatus(status int) bool {
	_, ok := p.RetryOn[status]
	return ok
}

// Delay computes the wait duration before performing the given attempt
// number (1-indexed). Attempt 1 has zero delay (it's the initial request);
// attempt N >= 2 has delay InitialDelay * BackoffFactor^(N-2), capped at
// MaxDelay, with full jitter applied if Jitter is true.
func (p RetryPolicy) Delay(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	base := float64(p.InitialDelay) * math.Pow(p.BackoffFactor, float64(attempt-2))
	if base > float64(p.MaxDelay) {
		base = float64(p.MaxDelay)
	}
	if p.Jitter {
		base = rand.Float64() * base
	}
	return time.Duration(base)
}

// ParseRetryAfter parses an HTTP Retry-After header value. Both the
// delta-seconds and HTTP-date forms are honored. Returns 0 if the value
// is empty, malformed, or in the past.
func ParseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := nethttp.ParseTime(header); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

// RetryAfterApplies reports whether Retry-After should be honored on the
// given response status. Per spec, only 429 and 503.
func RetryAfterApplies(status int) bool {
	return status == nethttp.StatusTooManyRequests ||
		status == nethttp.StatusServiceUnavailable
}

// ParseRetryPolicyFromValue parses a per-call opts.retry override from a
// cty value. Unlike parseRetryBlock, this version accepts an already-
// evaluated object (because opts.retry is an inline literal in an action
// expression, not a sub-block of a client definition).
//
// The on_response field is intentionally not supported in per-call
// overrides: HCL has already evaluated the object by the time we see it,
// so a hook expression would be reduced to whatever value the (then-empty)
// response context produced — useless. Per-call retry overrides are for
// counts, delays, and status sets; complex hooks belong on the client.
func ParseRetryPolicyFromValue(val cty.Value) (RetryPolicy, error) {
	p := retryEnabledDefaults()

	if val.IsNull() {
		return p, nil
	}
	if !val.Type().IsObjectType() && !val.Type().IsMapType() {
		return p, fmt.Errorf("retry override must be an object or map, got %s", val.Type().FriendlyName())
	}
	attrs := val.AsValueMap()

	if v, ok := attrs["max_attempts"]; ok && !v.IsNull() {
		if v.Type() != cty.Number {
			return p, fmt.Errorf("max_attempts must be a number, got %s", v.Type().FriendlyName())
		}
		n, _ := v.AsBigFloat().Int64()
		if n < 1 {
			return p, fmt.Errorf("max_attempts must be >= 1")
		}
		p.MaxAttempts = int(n)
	}
	if v, ok := attrs["initial_delay"]; ok && !v.IsNull() {
		d, err := cfg.ParseDurationFromValue(v)
		if err != nil {
			return p, fmt.Errorf("initial_delay: %w", err)
		}
		p.InitialDelay = d
	}
	if v, ok := attrs["max_delay"]; ok && !v.IsNull() {
		d, err := cfg.ParseDurationFromValue(v)
		if err != nil {
			return p, fmt.Errorf("max_delay: %w", err)
		}
		p.MaxDelay = d
	}
	if v, ok := attrs["backoff_factor"]; ok && !v.IsNull() {
		if v.Type() != cty.Number {
			return p, fmt.Errorf("backoff_factor must be a number, got %s", v.Type().FriendlyName())
		}
		f, _ := v.AsBigFloat().Float64()
		if f < 1.0 {
			return p, fmt.Errorf("backoff_factor must be >= 1.0")
		}
		p.BackoffFactor = f
	}
	if v, ok := attrs["jitter"]; ok && !v.IsNull() {
		if v.Type() != cty.Bool {
			return p, fmt.Errorf("jitter must be a bool, got %s", v.Type().FriendlyName())
		}
		p.Jitter = v.True()
	}
	if v, ok := attrs["retry_on"]; ok && !v.IsNull() {
		if !v.Type().IsListType() && !v.Type().IsTupleType() {
			return p, fmt.Errorf("retry_on must be a list of numbers, got %s", v.Type().FriendlyName())
		}
		set := make(map[int]struct{})
		for it := v.ElementIterator(); it.Next(); {
			_, elem := it.Element()
			if elem.IsNull() || elem.Type() != cty.Number {
				return p, fmt.Errorf("retry_on values must be numbers")
			}
			n, _ := elem.AsBigFloat().Int64()
			set[int(n)] = struct{}{}
		}
		p.RetryOn = set
	}
	if v, ok := attrs["respect_retry_after"]; ok && !v.IsNull() {
		if v.Type() != cty.Bool {
			return p, fmt.Errorf("respect_retry_after must be a bool, got %s", v.Type().FriendlyName())
		}
		p.RespectRetryAfter = v.True()
	}
	if v, ok := attrs["allow_non_idempotent"]; ok && !v.IsNull() {
		if v.Type() != cty.Bool {
			return p, fmt.Errorf("allow_non_idempotent must be a bool, got %s", v.Type().FriendlyName())
		}
		p.AllowNonIdempotent = v.True()
	}

	return p, nil
}
