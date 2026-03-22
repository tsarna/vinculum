package config

import (
	"github.com/zclconf/go-cty/cty"
)

// llmErrorType is the cty type for LLM error objects.
var llmErrorType = cty.Object(map[string]cty.Type{
	"code":    cty.String,
	"message": cty.String,
})

// llmUsageType is the cty type for LLM token usage objects.
var llmUsageType = cty.Object(map[string]cty.Type{
	"input_tokens":  cty.Number,
	"output_tokens": cty.Number,
	"total_tokens":  cty.Number,
})

// llmResponseType is the uniform cty type for all LLM response objects,
// shared across all provider implementations (OpenAI, Anthropic, etc.).
// Both success and error responses use this same type.
var llmResponseType = cty.Object(map[string]cty.Type{
	"content":     cty.String,
	"stop_reason": cty.String,
	"model":       cty.String,
	"usage":       llmUsageType,
	"error":       llmErrorType,
})

// llmSuccessResponse builds a successful LLM response cty.Value.
func llmSuccessResponse(content, stopReason, model string, inputTokens, outputTokens, totalTokens int) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"content":     cty.StringVal(content),
		"stop_reason": cty.StringVal(stopReason),
		"model":       cty.StringVal(model),
		"usage": cty.ObjectVal(map[string]cty.Value{
			"input_tokens":  cty.NumberIntVal(int64(inputTokens)),
			"output_tokens": cty.NumberIntVal(int64(outputTokens)),
			"total_tokens":  cty.NumberIntVal(int64(totalTokens)),
		}),
		"error": cty.NullVal(llmErrorType),
	})
}

// llmErrorResponse builds an error LLM response cty.Value.
// Call() returns this instead of a Go error for all API-level failures.
func llmErrorResponse(code, message string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"content":     cty.NullVal(cty.String),
		"stop_reason": cty.StringVal("error"),
		"model":       cty.NullVal(cty.String),
		"usage":       cty.NullVal(llmUsageType),
		"error": cty.ObjectVal(map[string]cty.Value{
			"code":    cty.StringVal(code),
			"message": cty.StringVal(message),
		}),
	})
}

// BaseLLMClient holds fields common to all LLM provider clients.
type BaseLLMClient struct {
	BaseClient
	model       string
	maxTokens   *int
	temperature *float32
}
