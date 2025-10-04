package config

import (
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
