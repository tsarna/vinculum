// Authentication hook for the HTTP client.
//
// The `auth` attribute on a `client "http"`
// block is an HCL expression evaluated lazily to produce the value of the
// Authorization header. This file holds the cache state machine, the
// reentrancy marker plumbing, and the parser for the result value.
//
// The actual HCL expression evaluation lives in package functions because
// it needs the package's HCL eval context machinery. The verb function
// builds an AuthEvaluator closure each request and passes it to the
// AuthHandler — the cache decides whether to call it (cache miss) or
// return the cached value.

package http

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
)

// ─── Reentrancy marker (Go context value) ────────────────────────────────────

// authMarkerKey is the context.Context key under which the reentrancy
// marker set is stored. The marker is a map[string]struct{} of client
// names whose auth hooks must not re-run while the context is in scope.
//
// Using a context value (rather than a goroutine-local flag) lets the
// marker survive goroutine boundaries — required by the spec, since hook
// expressions can dispatch HTTP calls across worker pools, bus
// subscriptions, etc.
type authMarkerKey struct{}

// WithAuthMarker returns a derived context that adds clientName to the
// reentrancy marker set. The set is treated as immutable: the returned
// context carries a fresh map containing all previously-marked names plus
// clientName, so concurrent goroutines reading the parent context never
// observe a half-mutated set.
func WithAuthMarker(ctx context.Context, clientName string) context.Context {
	existing, _ := ctx.Value(authMarkerKey{}).(map[string]struct{})
	next := make(map[string]struct{}, len(existing)+1)
	for k := range existing {
		next[k] = struct{}{}
	}
	next[clientName] = struct{}{}
	return context.WithValue(ctx, authMarkerKey{}, next)
}

// IsAuthSuppressed reports whether the given context is currently inside
// the auth hook for the named client. Verb functions check this before
// evaluating the auth hook for a request — if true, the request is sent
// with no Authorization header (or with whatever opts.auth supplies).
func IsAuthSuppressed(ctx context.Context, clientName string) bool {
	set, _ := ctx.Value(authMarkerKey{}).(map[string]struct{})
	_, ok := set[clientName]
	return ok
}

// ─── Auth attempt and result types ───────────────────────────────────────────

// AuthAttempt is the structured value exposed to an auth expression as
// `ctx.auth_attempt`. It describes why the hook is running, so a
// sophisticated hook can treat the first attempt differently from a
// re-auth.
type AuthAttempt struct {
	// Reason is "initial" (first call, or after TTL expiry) or
	// "unauthorized" (after a 401 response).
	Reason string
	// Failures is the number of consecutive prior failures of this hook.
	Failures int
	// PreviousStatus is the HTTP status of the response that triggered
	// this re-evaluation. 0 on the initial call.
	PreviousStatus int
}

// AuthResult is the parsed return value of an auth expression. The Value
// field is the literal Authorization header to send. ExpiresIn, if
// non-zero, gives the lifetime of the credential after which the cache
// should be considered stale and the hook re-run on the next request.
type AuthResult struct {
	Value     string
	ExpiresIn time.Duration
}

// AuthEvaluator is the function the verb function passes to AuthHandler.Get
// to actually evaluate the auth expression. The closure captures the
// HCL expression and config; the cache calls it under the singleflight
// mutex when a fresh value is needed.
type AuthEvaluator func(attempt AuthAttempt) (AuthResult, error)

// ─── AuthBackoff configuration ───────────────────────────────────────────────

// AuthBackoff describes the exponential backoff applied after the auth
// hook has accumulated `MaxFailures` consecutive failures.
type AuthBackoff struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Factor       float64
}

func defaultAuthBackoff() AuthBackoff {
	return AuthBackoff{
		InitialDelay: 1 * time.Second,
		MaxDelay:     60 * time.Second,
		Factor:       2.0,
	}
}

// authBackoffBlock is the HCL-decoded form of an `auth_retry_backoff { ... }`
// sub-block.
type authBackoffBlock struct {
	InitialDelay  hcl.Expression `hcl:"initial_delay,optional"`
	MaxDelay      hcl.Expression `hcl:"max_delay,optional"`
	BackoffFactor *float64       `hcl:"backoff_factor,optional"`
	DefRange      hcl.Range      `hcl:",def_range"`
}

func parseAuthBackoffBlock(config *cfg.Config, b *authBackoffBlock) (AuthBackoff, hcl.Diagnostics) {
	bo := defaultAuthBackoff()
	if b == nil {
		return bo, nil
	}
	if cfg.IsExpressionProvided(b.InitialDelay) {
		d, diags := config.ParseDuration(b.InitialDelay)
		if diags.HasErrors() {
			return bo, diags
		}
		bo.InitialDelay = d
	}
	if cfg.IsExpressionProvided(b.MaxDelay) {
		d, diags := config.ParseDuration(b.MaxDelay)
		if diags.HasErrors() {
			return bo, diags
		}
		bo.MaxDelay = d
	}
	if b.BackoffFactor != nil {
		if *b.BackoffFactor < 1.0 {
			return bo, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "auth_retry_backoff.backoff_factor must be >= 1.0",
				Subject:  &b.DefRange,
			}}
		}
		bo.Factor = *b.BackoffFactor
	}
	return bo, nil
}

// ─── AuthHandler interface ───────────────────────────────────────────────────

// AuthHandler is the per-client interface the verb functions use to
// fetch and update Authorization header state. Real HTTPClients return
// an *authCache; mock clients are free to provide any implementation.
type AuthHandler interface {
	// ClientName returns the name of the client this handler belongs
	// to. Used by verb functions to manage the reentrancy marker.
	ClientName() string

	// Expression returns the HCL expression that should be evaluated to
	// produce the Authorization header value. The verb function uses it
	// to build the AuthEvaluator closure.
	Expression() hcl.Expression

	// Get returns a cached or freshly-evaluated Authorization header.
	//
	// If reentrancy suppression applies (the client's name is in the
	// marker set on ctx), Get returns ("", nil) — the verb function
	// must send the request with no Authorization header.
	//
	// If the cache holds a value that has not expired, Get returns it
	// without calling eval.
	//
	// If the cache is empty or expired, Get calls eval under a
	// singleflight mutex (concurrent callers wait on the same
	// evaluation). The reason argument seeds AuthAttempt.Reason; pass
	// "initial" for the normal first-time / TTL-expired case and
	// "unauthorized" for a re-eval after a 401.
	//
	// If the handler is in failed state (consecutive failures exceeded
	// MaxFailures and the backoff has not yet elapsed), Get returns an
	// error without calling eval.
	Get(ctx context.Context, reason string, previousStatus int, eval AuthEvaluator) (string, error)

	// Invalidate marks the cached value as stale. The next call to Get
	// will re-evaluate. Used after a 401 response to force a fresh
	// credential on the next attempt.
	Invalidate()

	// RecordResult updates the consecutive-failure counter based on the
	// status of a request that consumed the handler's value:
	//   - 401 → failure (eligible for failed-state entry)
	//   - any other status → success, counter reset
	//
	// Note: 401 caused by a hook *evaluation* error is recorded
	// internally by Get itself — RecordResult is for wire results.
	RecordResult(status int)

	// Snapshot returns a copy of the handler's internal counters for
	// observability and tests. Implementations should make this safe to
	// call concurrently with other methods.
	Snapshot() AuthSnapshot
}

// AuthSnapshot is a read-only view of an AuthHandler's internal state.
type AuthSnapshot struct {
	HasCachedValue      bool
	ConsecutiveFailures int
	InFailedState       bool
}

// ─── authCache implementation ────────────────────────────────────────────────

// authCache is the production AuthHandler used by *HTTPClient. The
// mutex serializes everything: get, evaluate, store, record. This is the
// "singleflight" the spec calls for — concurrent callers all queue at
// the mutex, the first one populates the cache (or determines failed
// state), the rest read the result.
type authCache struct {
	mu sync.Mutex

	clientName string
	expr       hcl.Expression

	maxLifetime time.Duration // 0 = no client-level cap
	maxFailures int           // 0 = use default
	backoff     AuthBackoff

	// Cache state.
	valueSet  bool
	value     string
	expiresAt time.Time // zero = no expiry

	// Failure tracking.
	consecutiveFailures int
	failedUntil         time.Time // zero = not in failed state
}

// newAuthCache constructs an authCache for an HTTPClient. expr must be
// non-nil; callers should not construct an authCache for a client with no
// auth configured.
func newAuthCache(clientName string, expr hcl.Expression, maxLifetime time.Duration, maxFailures int, backoff AuthBackoff) *authCache {
	if maxFailures <= 0 {
		maxFailures = 5
	}
	return &authCache{
		clientName:  clientName,
		expr:        expr,
		maxLifetime: maxLifetime,
		maxFailures: maxFailures,
		backoff:     backoff,
	}
}

// Compile-time check.
var _ AuthHandler = (*authCache)(nil)

func (c *authCache) ClientName() string         { return c.clientName }
func (c *authCache) Expression() hcl.Expression { return c.expr }

func (c *authCache) Get(ctx context.Context, reason string, previousStatus int, eval AuthEvaluator) (string, error) {
	// Reentrancy suppression: send the request with no Authorization
	// header. The hook for this client is currently in flight on a
	// parent context.
	if IsAuthSuppressed(ctx, c.clientName) {
		return "", nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	// Failed-state check: fail fast until the backoff elapses.
	if !c.failedUntil.IsZero() && now.Before(c.failedUntil) {
		return "", fmt.Errorf("auth: client %q is in failed state, fail-fast until %s", c.clientName, c.failedUntil.Format(time.RFC3339))
	}

	// Cache hit.
	if c.valueSet && (c.expiresAt.IsZero() || now.Before(c.expiresAt)) {
		return c.value, nil
	}

	// Cache miss → evaluate.
	attempt := AuthAttempt{
		Reason:         reason,
		Failures:       c.consecutiveFailures,
		PreviousStatus: previousStatus,
	}
	result, err := eval(attempt)
	if err != nil {
		c.recordFailureLocked(now)
		return "", fmt.Errorf("auth: %w", err)
	}

	// Compute the effective expiry: the earlier of the expression's TTL
	// and the client's auth_max_lifetime, if either is set.
	var effExpires time.Time
	if result.ExpiresIn > 0 {
		effExpires = now.Add(result.ExpiresIn)
	}
	if c.maxLifetime > 0 {
		clientExp := now.Add(c.maxLifetime)
		if effExpires.IsZero() || clientExp.Before(effExpires) {
			effExpires = clientExp
		}
	}

	c.valueSet = true
	c.value = result.Value
	c.expiresAt = effExpires
	// Note: we don't reset consecutiveFailures here — the caller still
	// needs to send the request and confirm a non-401 response before
	// we know the value is good. RecordResult handles that.

	return c.value, nil
}

func (c *authCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valueSet = false
	c.value = ""
	c.expiresAt = time.Time{}
}

func (c *authCache) RecordResult(status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if status == 401 {
		c.recordFailureLocked(now)
		// 401 always invalidates the cache so the next Get will re-eval.
		c.valueSet = false
		c.value = ""
		c.expiresAt = time.Time{}
		return
	}
	// Any other status (including 4xx and 5xx that aren't 401) is
	// considered authentication success — the credential reached the
	// upstream and was accepted. Reset failure tracking.
	c.consecutiveFailures = 0
	c.failedUntil = time.Time{}
}

// recordFailureLocked must be called with c.mu held.
func (c *authCache) recordFailureLocked(now time.Time) {
	c.consecutiveFailures++
	if c.consecutiveFailures < c.maxFailures {
		return
	}
	// Enter failed state. Backoff grows exponentially with each
	// failure beyond the threshold.
	excess := c.consecutiveFailures - c.maxFailures
	delay := time.Duration(float64(c.backoff.InitialDelay) * pow(c.backoff.Factor, excess))
	if c.backoff.MaxDelay > 0 && delay > c.backoff.MaxDelay {
		delay = c.backoff.MaxDelay
	}
	c.failedUntil = now.Add(delay)
}

func (c *authCache) Snapshot() AuthSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return AuthSnapshot{
		HasCachedValue:      c.valueSet,
		ConsecutiveFailures: c.consecutiveFailures,
		InFailedState:       !c.failedUntil.IsZero() && time.Now().Before(c.failedUntil),
	}
}

// pow returns x^n for non-negative integer n. Used for the auth backoff
// exponent — small enough that a tight loop beats math.Pow's float
// rounding artifacts.
func pow(x float64, n int) float64 {
	r := 1.0
	for i := 0; i < n; i++ {
		r *= x
	}
	return r
}

// ─── parseAuthResult ─────────────────────────────────────────────────────────

// ParseAuthResult interprets the value returned by the auth expression.
// Accepted forms (per HTTP-CLIENT-SPEC.md):
//
//   - string → AuthResult{Value: s}
//   - object {value, expires_in?, expires_at?, refresh_early?} → parsed
//
// expires_in and expires_at are mutually exclusive. refresh_early is
// subtracted from the computed expiry to refresh proactively. The result's
// ExpiresIn field is the time *from now* at which the credential should be
// considered stale, or 0 if no TTL was provided.
func ParseAuthResult(val cty.Value) (AuthResult, error) {
	if val.IsNull() {
		return AuthResult{}, fmt.Errorf("auth expression returned null")
	}
	if val.Type() == cty.String {
		return AuthResult{Value: val.AsString()}, nil
	}
	if !val.Type().IsObjectType() && !val.Type().IsMapType() {
		return AuthResult{}, fmt.Errorf("auth expression must return a string or object, got %s", val.Type().FriendlyName())
	}

	attrs := val.AsValueMap()

	valueAttr, ok := attrs["value"]
	if !ok || valueAttr.IsNull() {
		return AuthResult{}, fmt.Errorf("auth expression result object missing required 'value' attribute")
	}
	if valueAttr.Type() != cty.String {
		return AuthResult{}, fmt.Errorf("auth expression result 'value' must be a string, got %s", valueAttr.Type().FriendlyName())
	}

	out := AuthResult{Value: valueAttr.AsString()}

	hasExpiresIn := false
	if v, ok := attrs["expires_in"]; ok && !v.IsNull() {
		d, err := cfg.ParseDurationFromValue(v)
		if err != nil {
			return AuthResult{}, fmt.Errorf("auth expires_in: %w", err)
		}
		out.ExpiresIn = d
		hasExpiresIn = true
	}

	if v, ok := attrs["expires_at"]; ok && !v.IsNull() {
		if hasExpiresIn {
			return AuthResult{}, fmt.Errorf("auth result must not specify both expires_in and expires_at")
		}
		t, err := timecty.GetTime(v)
		if err != nil {
			return AuthResult{}, fmt.Errorf("auth expires_at: %w", err)
		}
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		out.ExpiresIn = d
	}

	if v, ok := attrs["refresh_early"]; ok && !v.IsNull() {
		d, err := cfg.ParseDurationFromValue(v)
		if err != nil {
			return AuthResult{}, fmt.Errorf("auth refresh_early: %w", err)
		}
		if out.ExpiresIn > 0 {
			out.ExpiresIn -= d
			if out.ExpiresIn < 0 {
				// refresh_early larger than expires_in: refresh
				// immediately on next call.
				out.ExpiresIn = 1 // smallest non-zero so the cache *is* set with a TTL
			}
		}
	}

	return out, nil
}
