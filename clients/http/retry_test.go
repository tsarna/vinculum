package http

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

// ── Defaults and parsing ────────────────────────────────────────────────────

func TestNoRetryPolicy_Defaults(t *testing.T) {
	p := noRetryPolicy()
	assert.Equal(t, 1, p.MaxAttempts, "no-retry: a single attempt")
	assert.True(t, p.RespectRetryAfter)
	assert.Contains(t, p.RetryOn, 429)
	assert.Contains(t, p.RetryOn, 502)
	assert.Contains(t, p.RetryOn, 503)
	assert.Contains(t, p.RetryOn, 504)
}

func TestRetryEnabledDefaults(t *testing.T) {
	p := retryEnabledDefaults()
	assert.Equal(t, 3, p.MaxAttempts)
	assert.Equal(t, 200*time.Millisecond, p.InitialDelay)
	assert.Equal(t, 30*time.Second, p.MaxDelay)
	assert.Equal(t, 2.0, p.BackoffFactor)
	assert.True(t, p.Jitter)
}

// ── Block parsing via the full client config path ──────────────────────────

func TestParseRetryBlock_AllFields(t *testing.T) {
	config, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {
    retry {
        max_attempts          = 5
        initial_delay         = "100ms"
        max_delay             = "10s"
        backoff_factor        = 1.5
        jitter                = false
        retry_on              = [500, 502]
        respect_retry_after   = false
        allow_non_idempotent  = true
    }
}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	c := config.Clients["http"]["api"].(*HTTPClient)
	p := c.DefaultRetryPolicy()
	assert.Equal(t, 5, p.MaxAttempts)
	assert.Equal(t, 100*time.Millisecond, p.InitialDelay)
	assert.Equal(t, 10*time.Second, p.MaxDelay)
	assert.Equal(t, 1.5, p.BackoffFactor)
	assert.False(t, p.Jitter)
	assert.Contains(t, p.RetryOn, 500)
	assert.Contains(t, p.RetryOn, 502)
	assert.NotContains(t, p.RetryOn, 429, "explicit retry_on replaces defaults")
	assert.False(t, p.RespectRetryAfter)
	assert.True(t, p.AllowNonIdempotent)
}

func TestParseRetryBlock_DefaultsApplied(t *testing.T) {
	// An empty retry { } block should yield the enabled-default policy.
	config, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {
    retry {}
}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	c := config.Clients["http"]["api"].(*HTTPClient)
	p := c.DefaultRetryPolicy()
	assert.Equal(t, 3, p.MaxAttempts, "empty retry block enables retries with default count")
}

func TestParseRetryBlock_NoBlockMeansNoRetry(t *testing.T) {
	config, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	c := config.Clients["http"]["api"].(*HTTPClient)
	assert.Equal(t, 1, c.DefaultRetryPolicy().MaxAttempts)
}

func TestParseRetryBlock_InvalidMaxAttempts(t *testing.T) {
	_, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {
    retry { max_attempts = 0 }
}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.True(t, diags.HasErrors())
}

func TestParseRetryBlock_InvalidBackoffFactor(t *testing.T) {
	_, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {
    retry { backoff_factor = 0.5 }
}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.True(t, diags.HasErrors())
}

// ── Method retryability ─────────────────────────────────────────────────────

func TestMethodRetryable_DefaultIdempotent(t *testing.T) {
	p := retryEnabledDefaults()
	for _, m := range []string{"GET", "HEAD", "OPTIONS", "PUT", "DELETE"} {
		assert.True(t, p.MethodRetryable(m), "method %s should be retryable", m)
	}
	for _, m := range []string{"POST", "PATCH"} {
		assert.False(t, p.MethodRetryable(m), "method %s should NOT be retryable by default", m)
	}
}

func TestMethodRetryable_AllowNonIdempotent(t *testing.T) {
	p := retryEnabledDefaults()
	p.AllowNonIdempotent = true
	for _, m := range []string{"GET", "POST", "PATCH", "PUT", "DELETE", "PROPFIND"} {
		assert.True(t, p.MethodRetryable(m))
	}
}

// ── Backoff math ────────────────────────────────────────────────────────────

func TestDelay_NoJitter(t *testing.T) {
	p := retryEnabledDefaults()
	p.Jitter = false
	p.InitialDelay = 100 * time.Millisecond
	p.MaxDelay = 10 * time.Second
	p.BackoffFactor = 2.0

	// Attempt 1 = the initial request, no delay before it.
	assert.Equal(t, time.Duration(0), p.Delay(1))
	// Attempt 2 = first retry, after initial_delay.
	assert.Equal(t, 100*time.Millisecond, p.Delay(2))
	// Attempt 3 = initial_delay * factor.
	assert.Equal(t, 200*time.Millisecond, p.Delay(3))
	// Attempt 4 = initial_delay * factor^2.
	assert.Equal(t, 400*time.Millisecond, p.Delay(4))
}

func TestDelay_CapAtMax(t *testing.T) {
	p := retryEnabledDefaults()
	p.Jitter = false
	p.InitialDelay = 1 * time.Second
	p.MaxDelay = 5 * time.Second
	p.BackoffFactor = 10.0

	// Attempt 2: 1s
	assert.Equal(t, 1*time.Second, p.Delay(2))
	// Attempt 3: 1s * 10 = 10s, capped to 5s
	assert.Equal(t, 5*time.Second, p.Delay(3))
	// Attempt 4: capped
	assert.Equal(t, 5*time.Second, p.Delay(4))
}

func TestDelay_JitterIsBoundedByBase(t *testing.T) {
	p := retryEnabledDefaults()
	p.Jitter = true
	p.InitialDelay = 100 * time.Millisecond
	p.MaxDelay = 10 * time.Second
	p.BackoffFactor = 2.0

	// Full jitter ⇒ each draw is in [0, base). Sample many times and
	// verify the bound holds; also verify variance to make sure it's
	// not always returning 0.
	var max time.Duration
	for i := 0; i < 200; i++ {
		d := p.Delay(2)
		assert.LessOrEqual(t, d, 100*time.Millisecond)
		if d > max {
			max = d
		}
	}
	assert.Greater(t, max, time.Duration(0), "jitter should produce non-zero values")
}

// ── Retry-After parsing ─────────────────────────────────────────────────────

func TestParseRetryAfter_Seconds(t *testing.T) {
	assert.Equal(t, 5*time.Second, ParseRetryAfter("5"))
	assert.Equal(t, 0*time.Second, ParseRetryAfter("0"))
	assert.Equal(t, time.Duration(0), ParseRetryAfter("-1"), "negative seconds → 0")
}

func TestParseRetryAfter_HTTPDate_Future(t *testing.T) {
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	d := ParseRetryAfter(future)
	// Allow some slack for clock skew between Now() calls.
	assert.InDelta(t, float64(2*time.Second), float64(d), float64(500*time.Millisecond))
}

func TestParseRetryAfter_HTTPDate_Past(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
	assert.Equal(t, time.Duration(0), ParseRetryAfter(past))
}

func TestParseRetryAfter_Invalid(t *testing.T) {
	assert.Equal(t, time.Duration(0), ParseRetryAfter(""))
	assert.Equal(t, time.Duration(0), ParseRetryAfter("not a number"))
}

func TestRetryAfterApplies(t *testing.T) {
	assert.True(t, RetryAfterApplies(429))
	assert.True(t, RetryAfterApplies(503))
	assert.False(t, RetryAfterApplies(502))
	assert.False(t, RetryAfterApplies(500))
	assert.False(t, RetryAfterApplies(200))
}

// ── ShouldRetryStatus ───────────────────────────────────────────────────────

func TestShouldRetryStatus_Default(t *testing.T) {
	p := retryEnabledDefaults()
	assert.True(t, p.ShouldRetryStatus(429))
	assert.True(t, p.ShouldRetryStatus(502))
	assert.True(t, p.ShouldRetryStatus(503))
	assert.True(t, p.ShouldRetryStatus(504))
	assert.False(t, p.ShouldRetryStatus(200))
	assert.False(t, p.ShouldRetryStatus(404))
	assert.False(t, p.ShouldRetryStatus(500))
}

// ── ParseRetryPolicyFromValue (per-call override path) ─────────────────────

func TestParseRetryPolicyFromValue_AllFields(t *testing.T) {
	val := cty.ObjectVal(map[string]cty.Value{
		"max_attempts":         cty.NumberIntVal(7),
		"initial_delay":        cty.StringVal("50ms"),
		"max_delay":            cty.StringVal("2s"),
		"backoff_factor":       cty.NumberFloatVal(3.0),
		"jitter":               cty.BoolVal(false),
		"retry_on":             cty.TupleVal([]cty.Value{cty.NumberIntVal(418)}),
		"respect_retry_after":  cty.BoolVal(false),
		"allow_non_idempotent": cty.BoolVal(true),
	})
	p, err := ParseRetryPolicyFromValue(val)
	require.NoError(t, err)
	assert.Equal(t, 7, p.MaxAttempts)
	assert.Equal(t, 50*time.Millisecond, p.InitialDelay)
	assert.Equal(t, 2*time.Second, p.MaxDelay)
	assert.Equal(t, 3.0, p.BackoffFactor)
	assert.False(t, p.Jitter)
	assert.Contains(t, p.RetryOn, 418)
	assert.False(t, p.RespectRetryAfter)
	assert.True(t, p.AllowNonIdempotent)
}

func TestParseRetryPolicyFromValue_Empty(t *testing.T) {
	val := cty.EmptyObjectVal
	p, err := ParseRetryPolicyFromValue(val)
	require.NoError(t, err)
	assert.Equal(t, 3, p.MaxAttempts, "empty override yields enabled defaults")
}

func TestParseRetryPolicyFromValue_InvalidMaxAttempts(t *testing.T) {
	val := cty.ObjectVal(map[string]cty.Value{
		"max_attempts": cty.NumberIntVal(0),
	})
	_, err := ParseRetryPolicyFromValue(val)
	assert.Error(t, err)
}
