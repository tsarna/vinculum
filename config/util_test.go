package config

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

func TestConfigParseDuration(t *testing.T) {
	// Create a config instance with an evaluation context
	logger := zap.NewNop()
	config := &Config{
		Logger:  logger,
		evalCtx: &hcl.EvalContext{},
	}

	tests := []struct {
		name        string
		input       string
		expected    time.Duration
		expectError bool
	}{
		// Number inputs (treated as seconds)
		{
			name:     "integer seconds",
			input:    "30",
			expected: 30 * time.Second,
		},
		{
			name:     "float seconds",
			input:    "1.5",
			expected: time.Duration(1.5 * float64(time.Second)),
		},
		{
			name:     "zero seconds",
			input:    "0",
			expected: 0,
		},
		{
			name:        "negative seconds",
			input:       "-5",
			expectError: true,
		},

		// ISO 8601 duration strings (starting with P)
		{
			name:     "ISO 8601 5 minutes",
			input:    `"PT5M"`,
			expected: 5 * time.Minute,
		},
		{
			name:     "ISO 8601 1 hour 30 minutes",
			input:    `"PT1H30M"`,
			expected: time.Hour + 30*time.Minute,
		},
		{
			name:     "ISO 8601 2 days",
			input:    `"P2D"`,
			expected: 48 * time.Hour,
		},
		{
			name:        "invalid ISO 8601",
			input:       `"PXX"`,
			expectError: true,
		},

		// Go duration strings
		{
			name:     "Go duration minutes",
			input:    `"5m"`,
			expected: 5 * time.Minute,
		},
		{
			name:     "Go duration hours",
			input:    `"2h"`,
			expected: 2 * time.Hour,
		},
		{
			name:     "Go duration mixed",
			input:    `"1h30m45s"`,
			expected: time.Hour + 30*time.Minute + 45*time.Second,
		},
		{
			name:     "Go duration milliseconds",
			input:    `"500ms"`,
			expected: 500 * time.Millisecond,
		},
		{
			name:        "invalid Go duration",
			input:       `"5x"`,
			expectError: true,
		},
		{
			name:        "negative Go duration",
			input:       `"-5m"`,
			expectError: true,
		},

		// Edge cases
		{
			name:     "whitespace around string",
			input:    `"  5m  "`,
			expected: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the HCL expression
			expr, diags := hclsyntax.ParseExpression([]byte(tt.input), "test.hcl", hcl.Pos{Line: 1, Column: 1})
			require.False(t, diags.HasErrors(), "Failed to parse HCL expression: %v", diags)

			// Test ParseDuration
			duration, parseDiags := config.ParseDuration(expr)

			if tt.expectError {
				assert.True(t, parseDiags.HasErrors(), "Expected error but got none")
			} else {
				assert.False(t, parseDiags.HasErrors(), "Unexpected error: %v", parseDiags)
				assert.Equal(t, tt.expected, duration)
			}
		})
	}
}

func TestConfigParseDurationInvalidTypes(t *testing.T) {
	// Create a config instance with an evaluation context
	logger := zap.NewNop()
	config := &Config{
		Logger:  logger,
		evalCtx: &hcl.EvalContext{},
	}

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "boolean",
			input: "true",
		},
		{
			name:  "list",
			input: "[1, 2, 3]",
		},
		{
			name:  "object",
			input: `{foo = "bar"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the HCL expression
			expr, diags := hclsyntax.ParseExpression([]byte(tt.input), "test.hcl", hcl.Pos{Line: 1, Column: 1})
			require.False(t, diags.HasErrors(), "Failed to parse HCL expression: %v", diags)

			// Test ParseDuration
			_, parseDiags := config.ParseDuration(expr)
			assert.True(t, parseDiags.HasErrors(), "Expected error for invalid type")

			// Check that the error message mentions the type issue
			errorText := strings.ToLower(parseDiags.Error())
			assert.Contains(t, errorText, "type", "Error should mention type issue")
		})
	}
}

func TestConfigParseDurationCapsule(t *testing.T) {
	logger := zap.NewNop()
	cfg := &Config{
		Logger:  logger,
		evalCtx: &hcl.EvalContext{},
	}

	expected := 5 * time.Millisecond
	capsuleVar := timecty.NewDurationCapsule(expected)

	cfg.evalCtx = &hcl.EvalContext{
		Variables: map[string]cty.Value{"d": capsuleVar},
	}

	expr, diags := hclsyntax.ParseExpression([]byte("d"), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors())

	got, parseDiags := cfg.ParseDuration(expr)
	assert.False(t, parseDiags.HasErrors(), "Unexpected error: %v", parseDiags)
	assert.Equal(t, expected, got)
}

func TestConfigParseDurationWithVariables(t *testing.T) {
	// Create a config instance with variables in the evaluation context
	logger := zap.NewNop()
	config := &Config{
		Logger: logger,
		evalCtx: &hcl.EvalContext{
			Variables: map[string]cty.Value{
				"timeout": cty.NumberIntVal(60),
			},
		},
	}

	// Test with variable reference
	expr, diags := hclsyntax.ParseExpression([]byte("timeout"), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors(), "Failed to parse HCL expression: %v", diags)

	duration, parseDiags := config.ParseDuration(expr)
	assert.False(t, parseDiags.HasErrors(), "Unexpected error: %v", parseDiags)
	assert.Equal(t, 60*time.Second, duration)
}

// --- reconnect backoff ------------------------------------------------------

func reconnectTestConfig() *Config {
	return &Config{Logger: zap.NewNop(), evalCtx: &hcl.EvalContext{}}
}

func durationExpr(t *testing.T, src string) hcl.Expression {
	t.Helper()
	expr, diags := hclsyntax.ParseExpression([]byte(src), "test.hcl", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors(), "parse %s: %v", src, diags)
	return expr
}

// No block means no function, which is what every protocol client reads as
// "use your own default schedule". Returning a function here would silently
// replace the library's behaviour for someone who never asked to configure it.
func TestReconnectBackoffFuncIsNilWithoutABlock(t *testing.T) {
	fn, diags := reconnectTestConfig().ReconnectBackoffFunc(nil)
	assert.False(t, diags.HasErrors())
	assert.Nil(t, fn)
}

func TestReconnectBackoffFuncDefaults(t *testing.T) {
	fn, diags := reconnectTestConfig().ReconnectBackoffFunc(&ReconnectDefinition{})
	require.False(t, diags.HasErrors())
	require.NotNil(t, fn)

	// 1s doubling, clamped at 60s — and clamped for good, not wrapping.
	assert.Equal(t, time.Second, fn(0))
	assert.Equal(t, 2*time.Second, fn(1))
	assert.Equal(t, 32*time.Second, fn(5))
	assert.Equal(t, 60*time.Second, fn(6))
	assert.Equal(t, 60*time.Second, fn(100))
}

func TestReconnectBackoffFuncHonorsEveryAttribute(t *testing.T) {
	factor := 3.0
	fn, diags := reconnectTestConfig().ReconnectBackoffFunc(&ReconnectDefinition{
		InitialDelay:  durationExpr(t, `"500ms"`),
		MaxDelay:      durationExpr(t, `"2s"`),
		BackoffFactor: &factor,
	})
	require.False(t, diags.HasErrors())

	assert.Equal(t, 500*time.Millisecond, fn(0))
	assert.Equal(t, 1500*time.Millisecond, fn(1))
	assert.Equal(t, 2*time.Second, fn(2), "clamped by max_delay")
}

// A factor of 1 is a constant schedule rather than a degenerate one, which is
// the natural way to ask for no backoff at all.
func TestReconnectBackoffFuncWithAConstantSchedule(t *testing.T) {
	factor := 1.0
	fn, diags := reconnectTestConfig().ReconnectBackoffFunc(&ReconnectDefinition{
		InitialDelay:  durationExpr(t, `"5s"`),
		BackoffFactor: &factor,
	})
	require.False(t, diags.HasErrors())

	assert.Equal(t, 5*time.Second, fn(0))
	assert.Equal(t, 5*time.Second, fn(9))
}

func TestReconnectBackoffFuncReportsABadDuration(t *testing.T) {
	_, diags := reconnectTestConfig().ReconnectBackoffFunc(&ReconnectDefinition{
		InitialDelay: durationExpr(t, `"not a duration"`),
	})
	assert.True(t, diags.HasErrors())

	_, diags = reconnectTestConfig().ReconnectBackoffFunc(&ReconnectDefinition{
		MaxDelay: durationExpr(t, `"also not"`),
	})
	assert.True(t, diags.HasErrors())
}

// The invariant the shared resolution exists to protect: one block means one
// schedule, whichever client it lands in. The two paths reach different client
// libraries by different means, so nothing but this holds them together.
func TestBothReconnectPathsAgreeOnTheSchedule(t *testing.T) {
	c := reconnectTestConfig()

	for _, def := range []ReconnectDefinition{
		{}, // block present, everything omitted — the case defaults decide
		{MaxDelay: durationExpr(t, `"5s"`)},
		{InitialDelay: durationExpr(t, `"250ms"`), MaxDelay: durationExpr(t, `"90s"`)},
	} {
		fn, diags := c.ReconnectBackoffFunc(&def)
		require.False(t, diags.HasErrors())
		require.NotNil(t, fn)

		reconnector, diags := c.CreateReconnector(def)
		require.False(t, diags.HasErrors())
		require.NotNil(t, reconnector)

		// AutoReconnector does not expose its schedule, so compare against the
		// resolution both sides consume rather than against its internals.
		schedule, diags := c.resolveReconnectSchedule(&def)
		require.False(t, diags.HasErrors())
		for attempt := 0; attempt < 12; attempt++ {
			assert.Equal(t, schedule.delay(attempt), fn(attempt))
		}
	}
}

// The schema states these constants as the block's defaults, and
// doc/client-*.md and `vinculum man` repeat what the schema says. The two are
// the same expression, so they cannot disagree; this parses the documented
// string back to confirm the formatting helpers produce what the constant means.
func TestReconnectDefaultsAreTheOnesDocumented(t *testing.T) {
	initial, err := time.ParseDuration(ReconnectAttrs["initial_delay"].Default)
	require.NoError(t, err, "a documented duration default must be a duration")
	assert.Equal(t, time.Duration(defaultReconnectInitialDelay), initial)

	maxDelay, err := time.ParseDuration(ReconnectAttrs["max_delay"].Default)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(defaultReconnectMaxDelay), maxDelay)

	factor, err := strconv.ParseFloat(ReconnectAttrs["backoff_factor"].Default, 64)
	require.NoError(t, err, "a documented number default must be a number")
	assert.Equal(t, float64(defaultReconnectBackoffFactor), factor)

	// Spelled the way a config would spell them, not the way Go prints them:
	// (60 * time.Second).String() is "1m0s", which nobody types into a duration
	// slot, and FormatFloat drops the ".0" off a whole-numbered multiplier.
	assert.Equal(t, "60s", ReconnectAttrs["max_delay"].Default)
	assert.Equal(t, "2.0", ReconnectAttrs["backoff_factor"].Default)
}

// max_retries has no documented default: unlimited is what an absent attribute
// means, and "no default worth stating" is exactly the case an empty Default is
// for. It also must not claim to be unhonored any more — it is honored on every
// client now, and a schema that says otherwise sends a reader looking for a
// workaround they do not need.
func TestMaxRetriesIsDocumentedAsHonored(t *testing.T) {
	meta := ReconnectAttrs["max_retries"]
	assert.Empty(t, meta.Default)
	assert.NotContains(t, meta.Doc, "Not honored")
	assert.Contains(t, meta.Doc, "forever when omitted")
}

// max_retries travels beside the schedule rather than inside it, because a
// backoff function's whole vocabulary is a duration and stopping is not a
// duration. Both consumers get it, by different means.
func TestReconnectMaxAttempts(t *testing.T) {
	attempts := func(n int) *int { return &n }

	// Zero is unlimited, and so is a negative number and an absent attribute.
	// Zero must mean unlimited because it is the value a client library's field
	// holds when nothing is passed, and an unconfigured client reconnects.
	assert.Equal(t, 0, ReconnectMaxAttempts(nil))
	assert.Equal(t, 0, ReconnectMaxAttempts(&ReconnectDefinition{}))
	assert.Equal(t, 0, ReconnectMaxAttempts(&ReconnectDefinition{MaxRetries: attempts(0)}))
	assert.Equal(t, 0, ReconnectMaxAttempts(&ReconnectDefinition{MaxRetries: attempts(-1)}))

	assert.Equal(t, 5, ReconnectMaxAttempts(&ReconnectDefinition{MaxRetries: attempts(5)}))
}

// The schedule is indifferent to the limit: it keeps answering "how long" for
// any attempt number, and it is the client's loop that stops asking.
func TestTheScheduleIsIndifferentToTheLimit(t *testing.T) {
	retries := 3
	def := ReconnectDefinition{MaxRetries: &retries}

	fn, diags := reconnectTestConfig().ReconnectBackoffFunc(&def)
	require.False(t, diags.HasErrors())
	assert.Equal(t, 60*time.Second, fn(100))

	reconnector, diags := reconnectTestConfig().CreateReconnector(def)
	require.False(t, diags.HasErrors())
	require.NotNil(t, reconnector)
}

// Every spelling of "forever" — absent, zero, or any negative — reaches the
// reconnector through ReconnectMaxAttempts as one value.
//
// AutoReconnector keeps maxRetries unexported with no accessor, so this asserts
// that each spelling is accepted and builds; the normalization itself is pinned
// by TestReconnectMaxAttempts.
func TestCreateReconnectorAcceptsEverySpellingOfForever(t *testing.T) {
	attempts := func(n int) *int { return &n }

	for _, def := range []ReconnectDefinition{
		{},
		{MaxRetries: attempts(0)},
		{MaxRetries: attempts(-1)},
		{MaxRetries: attempts(-5)}, // below the builder's own floor of -1
		{MaxRetries: attempts(3)},  // and an actual limit, for contrast
	} {
		reconnector, diags := reconnectTestConfig().CreateReconnector(def)
		require.False(t, diags.HasErrors())
		require.NotNil(t, reconnector)
	}
}
