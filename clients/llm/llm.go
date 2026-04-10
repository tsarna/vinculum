// Package llm provides shared types and helpers for LLM provider client implementations.
package llm

import (
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

// ─── cty types ────────────────────────────────────────────────────────────────

var errorType = cty.Object(map[string]cty.Type{
	"code":    cty.String,
	"message": cty.String,
})

var usageType = cty.Object(map[string]cty.Type{
	"input_tokens":  cty.Number,
	"output_tokens": cty.Number,
	"total_tokens":  cty.Number,
})

// ResponseType is the uniform cty type for all LLM response objects.
// Both success and error responses use this same type.
var ResponseType = cty.Object(map[string]cty.Type{
	"content":     cty.String,
	"stop_reason": cty.String,
	"model":       cty.String,
	"usage":       usageType,
	"error":       errorType,
})

// ─── Response constructors ────────────────────────────────────────────────────

// SuccessResponse builds a successful LLM response cty.Value.
func SuccessResponse(content, stopReason, model string, inputTokens, outputTokens, totalTokens int) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"content":     cty.StringVal(content),
		"stop_reason": cty.StringVal(stopReason),
		"model":       cty.StringVal(model),
		"usage": cty.ObjectVal(map[string]cty.Value{
			"input_tokens":  cty.NumberIntVal(int64(inputTokens)),
			"output_tokens": cty.NumberIntVal(int64(outputTokens)),
			"total_tokens":  cty.NumberIntVal(int64(totalTokens)),
		}),
		"error": cty.NullVal(errorType),
	})
}

// ErrorResponse builds an error LLM response cty.Value.
// Call() returns this instead of a Go error for all API-level failures.
func ErrorResponse(code, message string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"content":     cty.NullVal(cty.String),
		"stop_reason": cty.StringVal("error"),
		"model":       cty.NullVal(cty.String),
		"usage":       cty.NullVal(usageType),
		"error": cty.ObjectVal(map[string]cty.Value{
			"code":    cty.StringVal(code),
			"message": cty.StringVal(message),
		}),
	})
}

// ─── Base struct ──────────────────────────────────────────────────────────────

// BaseClient holds fields common to all LLM provider clients.
type BaseClient struct {
	cfg.BaseClient
	Model       string
	MaxTokens   *int
	Temperature *float32
}
