package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	openailib "github.com/sashabaranov/go-openai"
	richcty "github.com/tsarna/rich-cty-types"
	"github.com/tsarna/vinculum/clients/llm"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
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
	Tracing        hcl.Expression `hcl:"tracing,optional"`
	Metrics        hcl.Expression `hcl:"metrics,optional"`
	DefRange       hcl.Range      `hcl:",def_range"`
}

// OpenAIClient implements config.Client and config.Callable for OpenAI-compatible APIs.
type OpenAIClient struct {
	llm.BaseClient
	openaiClient   *openailib.Client
	maxInputLength *int
	tracerProvider trace.TracerProvider
	tracer         trace.Tracer
	metrics        *openaiMetrics
	serverAddress  string
	serverPort     int
}

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	clientDef := OpenAIClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &clientDef)
	if diags.HasErrors() {
		return nil, diags
	}
	clientDef.DefRange = block.DefRange

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

	tracerProvider, tracingDiags := config.ResolveTracerProvider(clientDef.Tracing)
	if tracingDiags.HasErrors() {
		return nil, tracingDiags
	}

	meterProvider, metricsDiags := cfg.ResolveMeterProvider(config, clientDef.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}

	var otelOpts []otelhttp.Option
	if tracerProvider != nil {
		otelOpts = append(otelOpts, otelhttp.WithTracerProvider(tracerProvider))
	}

	var timeout time.Duration
	if cfg.IsExpressionProvided(clientDef.Timeout) {
		var valDiags hcl.Diagnostics
		timeout, valDiags = config.ParseDuration(clientDef.Timeout)
		if valDiags.HasErrors() {
			return nil, valDiags
		}
	} else {
		timeout = 120 * time.Second
	}
	openaiCfg.HTTPClient = &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport, otelOpts...),
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

	// Derive a tracer for the gen_ai.inference span (always present; no-op when
	// tracing is not configured). The HTTP-level otelhttp transport span will
	// nest under it.
	tp := tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	serverAddress, serverPort := serverAddrPort(openaiCfg.BaseURL)

	c := &OpenAIClient{
		BaseClient: llm.BaseClient{
			BaseClient:  cfg.BaseClient{Name: block.Labels[1], DefRange: clientDef.DefRange},
			Model:       clientDef.Model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
		},
		openaiClient:   openailib.NewClientWithConfig(openaiCfg),
		maxInputLength: clientDef.MaxInputLength,
		tracerProvider: tracerProvider,
		tracer:         tp.Tracer(instrumentationScope),
		metrics:        newOpenAIMetrics(meterProvider),
		serverAddress:  serverAddress,
		serverPort:     serverPort,
	}

	return c, nil
}

// serverAddrPort extracts the host and port from the configured base URL for
// use as server.address / server.port telemetry attributes. Returns ("", 0)
// when the URL cannot be parsed.
func serverAddrPort(rawURL string) (string, int) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "", 0
	}
	port := 0
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	} else {
		switch u.Scheme {
		case "https":
			port = 443
		case "http":
			port = 80
		}
	}
	return u.Hostname(), port
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

	if model, ok := richcty.GetStringAttr(request, "model"); ok {
		req.Model = model
	}
	if mt, ok := richcty.GetIntAttr(request, "max_tokens"); ok {
		req.MaxTokens = mt
	} else if c.MaxTokens != nil {
		req.MaxTokens = *c.MaxTokens
	}
	if temp, ok := richcty.GetFloat32Attr(request, "temperature"); ok {
		req.Temperature = temp
	} else if c.Temperature != nil {
		req.Temperature = *c.Temperature
	}

	// Start a gen_ai.inference client span (OTel GenAI semantic conventions).
	// The otelhttp transport span for the underlying HTTP POST nests under it.
	reqAttrs := []attribute.KeyValue{
		attribute.String(attrGenAIOperationName, operationChat),
		attribute.String(attrGenAIProviderName, providerOpenAI),
		attribute.String(attrGenAIRequestModel, req.Model),
	}
	if c.serverAddress != "" {
		reqAttrs = append(reqAttrs, attribute.String(attrServerAddress, c.serverAddress))
		if c.serverPort != 0 {
			reqAttrs = append(reqAttrs, attribute.Int(attrServerPort, c.serverPort))
		}
	}
	if req.MaxTokens > 0 {
		reqAttrs = append(reqAttrs, attribute.Int(attrGenAIRequestMaxTokens, req.MaxTokens))
	}
	if req.Temperature != 0 {
		reqAttrs = append(reqAttrs, attribute.Float64(attrGenAIRequestTemperature, float64(req.Temperature)))
	}

	ctx, span := c.tracer.Start(ctx, operationChat+" "+req.Model,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(reqAttrs...),
	)

	start := time.Now()
	resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
	elapsed := time.Since(start).Seconds()

	// Low-cardinality metric attributes shared by duration and token usage.
	metricAttrs := []attribute.KeyValue{
		attribute.String(attrGenAIOperationName, operationChat),
		attribute.String(attrGenAIProviderName, providerOpenAI),
		attribute.String(attrGenAIRequestModel, req.Model),
	}
	if c.serverAddress != "" {
		metricAttrs = append(metricAttrs, attribute.String(attrServerAddress, c.serverAddress))
		if c.serverPort != 0 {
			metricAttrs = append(metricAttrs, attribute.Int(attrServerPort, c.serverPort))
		}
	}

	if err != nil {
		code, msg := extractOpenAIError(err)
		span.SetAttributes(attribute.String(attrErrorType, code))
		span.SetStatus(codes.Error, msg)
		span.End()
		c.metrics.opDuration.Record(ctx, elapsed,
			otelmetric.WithAttributes(withAttrs(metricAttrs, attribute.String(attrErrorType, code))...))
		return llm.ErrorResponse(code, msg), nil
	}

	// Enrich span and metric attributes with response details.
	if resp.Model != "" {
		metricAttrs = append(metricAttrs, attribute.String(attrGenAIResponseModel, resp.Model))
		span.SetAttributes(attribute.String(attrGenAIResponseModel, resp.Model))
	}
	if resp.ID != "" {
		span.SetAttributes(attribute.String(attrGenAIResponseID, resp.ID))
	}
	span.SetAttributes(
		attribute.Int(attrGenAIUsageInputTokens, resp.Usage.PromptTokens),
		attribute.Int(attrGenAIUsageOutputTokens, resp.Usage.CompletionTokens),
	)
	if len(resp.Choices) > 0 {
		span.SetAttributes(attribute.StringSlice(attrGenAIFinishReasons,
			[]string{string(resp.Choices[0].FinishReason)}))
	}

	c.metrics.opDuration.Record(ctx, elapsed, otelmetric.WithAttributes(metricAttrs...))
	c.metrics.tokenUsage.Record(ctx, int64(resp.Usage.PromptTokens),
		otelmetric.WithAttributes(withAttrs(metricAttrs, attribute.String(attrGenAITokenType, tokenTypeInput))...))
	c.metrics.tokenUsage.Record(ctx, int64(resp.Usage.CompletionTokens),
		otelmetric.WithAttributes(withAttrs(metricAttrs, attribute.String(attrGenAITokenType, tokenTypeOutput))...))

	span.End()

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

	if system, ok := richcty.GetStringAttr(request, "system"); ok && system != "" {
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
		role, ok := richcty.GetStringAttr(elem, "role")
		if !ok {
			return nil, fmt.Errorf("each message must have a role field")
		}
		content, ok := richcty.GetStringAttr(elem, "content")
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
