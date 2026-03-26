package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	openai "github.com/sashabaranov/go-openai"
	"github.com/zclconf/go-cty/cty"
)

// OpenAIClientDefinition holds the parsed HCL config for an openai client block.
type OpenAIClientDefinition struct {
	APIKey         string         `hcl:"api_key"`
	Model          string         `hcl:"model"`
	BaseURL        hcl.Expression `hcl:"base_url,optional"`
	MaxTokens      *int           `hcl:"max_tokens,optional"`
	Temperature    *float64       `hcl:"temperature,optional"`
	Timeout        hcl.Expression `hcl:"timeout,optional"`
	MaxInputLength *int           `hcl:"max_input_length,optional"`
	DefRange       hcl.Range      `hcl:",def_range"`
}

// OpenAIClient implements Client and Callable for OpenAI-compatible APIs.
type OpenAIClient struct {
	BaseLLMClient
	openaiClient   *openai.Client
	maxInputLength *int
}

// ProcessOpenAIClientBlock parses an `client "openai" "<name>"` block and
// returns an OpenAIClient ready for use.
func ProcessOpenAIClientBlock(config *Config, block *hcl.Block, remainingBody hcl.Body) (Client, hcl.Diagnostics) {
	clientDef := OpenAIClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.evalCtx, &clientDef)
	if diags.HasErrors() {
		return nil, diags
	}

	openaiClients, ok := config.Clients["openai"]
	if !ok {
		openaiClients = make(map[string]Client)
		config.Clients["openai"] = openaiClients
	}

	if existing, ok := openaiClients[block.Labels[1]]; ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "OpenAI client already defined",
				Detail:   fmt.Sprintf("OpenAI client %s already defined at %s", block.Labels[1], existing.GetDefRange()),
				Subject:  &clientDef.DefRange,
			},
		}
	}

	cfg := openai.DefaultConfig(clientDef.APIKey)

	if IsExpressionProvided(clientDef.BaseURL) {
		baseURLVal, valDiags := clientDef.BaseURL.Value(config.evalCtx)
		if valDiags.HasErrors() {
			return nil, valDiags
		}
		if baseURLVal.Type() != cty.String {
			r := clientDef.BaseURL.Range()
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "base_url must be a string",
				Subject:  &r,
			}}
		}
		cfg.BaseURL = baseURLVal.AsString()
	}

	if IsExpressionProvided(clientDef.Timeout) {
		timeout, valDiags := config.ParseDuration(clientDef.Timeout)
		if valDiags.HasErrors() {
			return nil, valDiags
		}
		cfg.HTTPClient = &http.Client{Timeout: timeout}
	} else {
		cfg.HTTPClient = &http.Client{Timeout: 120 * time.Second}
	}

	var maxTokens *int
	if clientDef.MaxTokens != nil {
		maxTokens = clientDef.MaxTokens
	}

	var temperature *float32
	if clientDef.Temperature != nil {
		t := float32(*clientDef.Temperature)
		temperature = &t
	}

	c := &OpenAIClient{
		BaseLLMClient: BaseLLMClient{
			BaseClient: BaseClient{
				Name:     block.Labels[1],
				DefRange: clientDef.DefRange,
			},
			model:       clientDef.Model,
			maxTokens:   maxTokens,
			temperature: temperature,
		},
		openaiClient:   openai.NewClientWithConfig(cfg),
		maxInputLength: clientDef.MaxInputLength,
	}

	return c, nil
}

// Call implements Callable. It makes a synchronous chat completion request and
// always returns an llmResponseType cty.Value — API errors become error responses
// rather than Go errors. args[0] is the request object.
func (c *OpenAIClient) Call(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("call: request argument is required")
	}
	request := args[0]
	if request.IsNull() || !request.IsKnown() {
		return cty.NilVal, fmt.Errorf("call: request must not be null")
	}

	messages, err := c.buildMessages(request)
	if err != nil {
		return cty.NilVal, fmt.Errorf("call: %w", err)
	}

	if c.maxInputLength != nil {
		total := 0
		for _, msg := range messages {
			if msg.Role != openai.ChatMessageRoleSystem {
				total += len(msg.Content)
			}
		}
		if total > *c.maxInputLength {
			return llmErrorResponse("input_too_long",
				fmt.Sprintf("input length %d exceeds max_input_length %d", total, *c.maxInputLength)), nil
		}
	}

	req := openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: messages,
	}

	// Per-call model override
	if model, ok := getStringAttr(request, "model"); ok {
		req.Model = model
	}

	// Per-call max_tokens override, falling back to client default
	if mt, ok := getIntAttr(request, "max_tokens"); ok {
		req.MaxTokens = mt
	} else if c.maxTokens != nil {
		req.MaxTokens = *c.maxTokens
	}

	// Per-call temperature override, falling back to client default
	if temp, ok := getFloat32Attr(request, "temperature"); ok {
		req.Temperature = temp
	} else if c.temperature != nil {
		req.Temperature = *c.temperature
	}

	resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
	if err != nil {
		code, msg := extractOpenAIError(err)
		return llmErrorResponse(code, msg), nil
	}

	if len(resp.Choices) == 0 {
		return llmErrorResponse("empty_response", "LLM returned no choices"), nil
	}

	choice := resp.Choices[0]
	return llmSuccessResponse(
		choice.Message.Content,
		mapFinishReason(choice.FinishReason),
		resp.Model,
		resp.Usage.PromptTokens,
		resp.Usage.CompletionTokens,
		resp.Usage.TotalTokens,
	), nil
}

// buildMessages converts the HCL request value into an openai.ChatCompletionMessage slice.
func (c *OpenAIClient) buildMessages(request cty.Value) ([]openai.ChatCompletionMessage, error) {
	var messages []openai.ChatCompletionMessage

	// system shorthand: prepend as role=system message
	if system, ok := getStringAttr(request, "system"); ok && system != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: system,
		})
	}

	// required messages array
	if !request.Type().HasAttribute("messages") {
		return nil, fmt.Errorf("request must include a messages field")
	}
	msgList := request.GetAttr("messages")
	if msgList.IsNull() || !msgList.IsKnown() {
		return nil, fmt.Errorf("messages must not be null")
	}
	if !msgList.CanIterateElements() {
		return nil, fmt.Errorf("messages must be a list or tuple")
	}

	for it := msgList.ElementIterator(); it.Next(); {
		_, elem := it.Element()
		if !elem.Type().IsObjectType() {
			return nil, fmt.Errorf("each message must be an object with role and content fields")
		}
		role, ok := getStringAttr(elem, "role")
		if !ok {
			return nil, fmt.Errorf("each message must have a role field")
		}
		content, ok := getStringAttr(elem, "content")
		if !ok {
			return nil, fmt.Errorf("each message must have a content field")
		}
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    role,
			Content: content,
		})
	}

	return messages, nil
}

// mapFinishReason converts an OpenAI finish reason to Vinculum's stop_reason strings.
func mapFinishReason(reason openai.FinishReason) string {
	switch reason {
	case openai.FinishReasonStop:
		return "stop"
	case openai.FinishReasonLength:
		return "max_tokens"
	default:
		return "stop"
	}
}

// extractOpenAIError pulls a code and message from an OpenAI API error.
func extractOpenAIError(err error) (code, message string) {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("%d", apiErr.HTTPStatusCode), apiErr.Message
	}
	return "error", err.Error()
}

// getStringAttr returns an attribute value as a string if it exists and is a string.
func getStringAttr(val cty.Value, attr string) (string, bool) {
	if !val.Type().HasAttribute(attr) {
		return "", false
	}
	a := val.GetAttr(attr)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.String {
		return "", false
	}
	return a.AsString(), true
}

// getIntAttr returns an attribute value as an int if it exists and is a number.
func getIntAttr(val cty.Value, attr string) (int, bool) {
	if !val.Type().HasAttribute(attr) {
		return 0, false
	}
	a := val.GetAttr(attr)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.Number {
		return 0, false
	}
	bf := a.AsBigFloat()
	i, _ := bf.Int64()
	return int(i), true
}

// getFloat32Attr returns an attribute value as a float32 if it exists and is a number.
func getFloat32Attr(val cty.Value, attr string) (float32, bool) {
	if !val.Type().HasAttribute(attr) {
		return 0, false
	}
	a := val.GetAttr(attr)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.Number {
		return 0, false
	}
	bf := a.AsBigFloat()
	f, _ := bf.Float32()
	return f, true
}
