package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	openailib "github.com/sashabaranov/go-openai"
	"github.com/tsarna/vinculum/clients/llm"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func init() {
	cfg.RegisterClientType("openai", process)
}

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

// OpenAIClient implements config.Client and config.Callable for OpenAI-compatible APIs.
type OpenAIClient struct {
	llm.BaseClient
	openaiClient   *openailib.Client
	maxInputLength *int
}

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	clientDef := OpenAIClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &clientDef)
	if diags.HasErrors() {
		return nil, diags
	}

	openaiCfg := openailib.DefaultConfig(clientDef.APIKey)

	if cfg.IsExpressionProvided(clientDef.BaseURL) {
		baseURLVal, valDiags := clientDef.BaseURL.Value(config.EvalCtx())
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
		openaiCfg.BaseURL = baseURLVal.AsString()
	}

	if cfg.IsExpressionProvided(clientDef.Timeout) {
		timeout, valDiags := config.ParseDuration(clientDef.Timeout)
		if valDiags.HasErrors() {
			return nil, valDiags
		}
		openaiCfg.HTTPClient = &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}
	} else {
		openaiCfg.HTTPClient = &http.Client{Timeout: 120 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)}
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
		BaseClient: llm.BaseClient{
			BaseClient:  cfg.BaseClient{Name: block.Labels[1], DefRange: clientDef.DefRange},
			Model:       clientDef.Model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
		},
		openaiClient:   openailib.NewClientWithConfig(openaiCfg),
		maxInputLength: clientDef.MaxInputLength,
	}

	return c, nil
}

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
			if msg.Role != openailib.ChatMessageRoleSystem {
				total += len(msg.Content)
			}
		}
		if total > *c.maxInputLength {
			return llm.ErrorResponse("input_too_long",
				fmt.Sprintf("input length %d exceeds max_input_length %d", total, *c.maxInputLength)), nil
		}
	}

	req := openailib.ChatCompletionRequest{
		Model:    c.Model,
		Messages: messages,
	}

	if model, ok := llm.GetStringAttr(request, "model"); ok {
		req.Model = model
	}
	if mt, ok := llm.GetIntAttr(request, "max_tokens"); ok {
		req.MaxTokens = mt
	} else if c.MaxTokens != nil {
		req.MaxTokens = *c.MaxTokens
	}
	if temp, ok := llm.GetFloat32Attr(request, "temperature"); ok {
		req.Temperature = temp
	} else if c.Temperature != nil {
		req.Temperature = *c.Temperature
	}

	resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
	if err != nil {
		code, msg := extractOpenAIError(err)
		return llm.ErrorResponse(code, msg), nil
	}

	if len(resp.Choices) == 0 {
		return llm.ErrorResponse("empty_response", "LLM returned no choices"), nil
	}

	choice := resp.Choices[0]
	return llm.SuccessResponse(
		choice.Message.Content,
		mapFinishReason(choice.FinishReason),
		resp.Model,
		resp.Usage.PromptTokens,
		resp.Usage.CompletionTokens,
		resp.Usage.TotalTokens,
	), nil
}

func (c *OpenAIClient) buildMessages(request cty.Value) ([]openailib.ChatCompletionMessage, error) {
	var messages []openailib.ChatCompletionMessage

	if system, ok := llm.GetStringAttr(request, "system"); ok && system != "" {
		messages = append(messages, openailib.ChatCompletionMessage{
			Role:    openailib.ChatMessageRoleSystem,
			Content: system,
		})
	}

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
		role, ok := llm.GetStringAttr(elem, "role")
		if !ok {
			return nil, fmt.Errorf("each message must have a role field")
		}
		content, ok := llm.GetStringAttr(elem, "content")
		if !ok {
			return nil, fmt.Errorf("each message must have a content field")
		}
		messages = append(messages, openailib.ChatCompletionMessage{
			Role:    role,
			Content: content,
		})
	}

	return messages, nil
}

func mapFinishReason(reason openailib.FinishReason) string {
	switch reason {
	case openailib.FinishReasonStop:
		return "stop"
	case openailib.FinishReasonLength:
		return "max_tokens"
	default:
		return "stop"
	}
}

func extractOpenAIError(err error) (code, message string) {
	var apiErr *openailib.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("%d", apiErr.HTTPStatusCode), apiErr.Message
	}
	return "error", err.Error()
}
