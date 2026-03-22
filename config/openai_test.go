package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

func newTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func TestOpenAIClientParsing(t *testing.T) {
	vcl := []byte(`
client "openai" "gpt" {
    api_key = "test-key"
    model   = "gpt-4o-mini"
}
`)
	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	assert.Contains(t, cfg.CtyClientMap, "gpt")
	clients, ok := cfg.Clients["openai"]
	require.True(t, ok)
	c, ok := clients["gpt"]
	require.True(t, ok)
	assert.Equal(t, "gpt", c.GetName())

	oai, ok := c.(*OpenAIClient)
	require.True(t, ok)
	assert.Equal(t, "gpt-4o-mini", oai.model)
	assert.Nil(t, oai.maxTokens)
	assert.Nil(t, oai.temperature)
}

func TestOpenAIClientWithOptionals(t *testing.T) {
	vcl := []byte(`
client "openai" "gpt" {
    api_key     = "test-key"
    model       = "gpt-4o"
    max_tokens  = 1024
    temperature = 0.5
    timeout     = "30s"
}
`)
	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oai := cfg.Clients["openai"]["gpt"].(*OpenAIClient)
	assert.Equal(t, "gpt-4o", oai.model)
	require.NotNil(t, oai.maxTokens)
	assert.Equal(t, 1024, *oai.maxTokens)
	require.NotNil(t, oai.temperature)
	assert.InDelta(t, 0.5, *oai.temperature, 0.001)
}

func TestOpenAIClientDuplicate(t *testing.T) {
	vcl := []byte(`
client "openai" "gpt" {
    api_key = "key1"
    model   = "gpt-4o-mini"
}
client "openai" "gpt" {
    api_key = "key2"
    model   = "gpt-4o"
}
`)
	_, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	assert.True(t, diags.HasErrors())
}

// openAIResponseJSON returns a minimal valid OpenAI chat completion JSON response.
func openAIResponseJSON(content, model string, finishReason string) []byte {
	resp := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func buildTestRequest(system, userMsg string) cty.Value {
	attrs := map[string]cty.Value{}
	if system != "" {
		attrs["system"] = cty.StringVal(system)
	}
	attrs["messages"] = cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"role":    cty.StringVal("user"),
			"content": cty.StringVal(userMsg),
		}),
	})
	return cty.ObjectVal(attrs)
}

func TestOpenAICallSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write(openAIResponseJSON("Hello!", "gpt-4o-mini", "stop"))
	}))
	defer srv.Close()

	vcl := []byte(fmt.Sprintf(`
client "openai" "gpt" {
    api_key  = "test-key"
    model    = "gpt-4o-mini"
    base_url = %q
}
`, srv.URL+"/"))

	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oai := cfg.Clients["openai"]["gpt"].(*OpenAIClient)
	result, err := oai.Call(context.Background(), buildTestRequest("", "Say hello"))
	require.NoError(t, err)

	assert.Equal(t, "stop", result.GetAttr("stop_reason").AsString())
	assert.Equal(t, "Hello!", result.GetAttr("content").AsString())
	assert.Equal(t, "gpt-4o-mini", result.GetAttr("model").AsString())
	assert.True(t, result.GetAttr("error").IsNull())

	usage := result.GetAttr("usage")
	promptTokens, _ := usage.GetAttr("input_tokens").AsBigFloat().Int64()
	assert.Equal(t, int64(10), promptTokens)
}

func TestOpenAICallMaxTokensFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(openAIResponseJSON("Truncated...", "gpt-4o-mini", "length"))
	}))
	defer srv.Close()

	vcl := []byte(fmt.Sprintf(`
client "openai" "gpt" {
    api_key  = "test-key"
    model    = "gpt-4o-mini"
    base_url = %q
}
`, srv.URL+"/"))

	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oai := cfg.Clients["openai"]["gpt"].(*OpenAIClient)
	result, err := oai.Call(context.Background(), buildTestRequest("", "Write an essay"))
	require.NoError(t, err)

	assert.Equal(t, "max_tokens", result.GetAttr("stop_reason").AsString())
}

func TestOpenAICallAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	vcl := []byte(fmt.Sprintf(`
client "openai" "gpt" {
    api_key  = "bad-key"
    model    = "gpt-4o-mini"
    base_url = %q
}
`, srv.URL+"/"))

	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oai := cfg.Clients["openai"]["gpt"].(*OpenAIClient)
	result, err := oai.Call(context.Background(), buildTestRequest("", "Hello"))
	require.NoError(t, err, "API errors should be returned as error response, not Go error")

	assert.Equal(t, "error", result.GetAttr("stop_reason").AsString())
	assert.True(t, result.GetAttr("content").IsNull())
	assert.True(t, result.GetAttr("model").IsNull())
	assert.True(t, result.GetAttr("usage").IsNull())

	errObj := result.GetAttr("error")
	assert.False(t, errObj.IsNull())
	assert.NotEmpty(t, errObj.GetAttr("message").AsString())
}

func TestOpenAICallMaxInputLength(t *testing.T) {
	vcl := []byte(`
client "openai" "gpt" {
    api_key          = "test-key"
    model            = "gpt-4o-mini"
    max_input_length = 10
}
`)
	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oai := cfg.Clients["openai"]["gpt"].(*OpenAIClient)
	// 11 chars — should exceed limit without hitting the server
	result, err := oai.Call(context.Background(), buildTestRequest("", "hello world!"))
	require.NoError(t, err)

	assert.Equal(t, "error", result.GetAttr("stop_reason").AsString())
	errObj := result.GetAttr("error")
	assert.Equal(t, "input_too_long", errObj.GetAttr("code").AsString())
}

func TestOpenAICallMaxInputLengthSystemExcluded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(openAIResponseJSON("OK", "gpt-4o-mini", "stop"))
	}))
	defer srv.Close()

	// limit=5, user msg="hi" (2 chars) — passes; long system prompt is excluded
	vcl := []byte(fmt.Sprintf(`
client "openai" "gpt" {
    api_key          = "test-key"
    model            = "gpt-4o-mini"
    base_url         = %q
    max_input_length = 5
}
`, srv.URL+"/"))

	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oai := cfg.Clients["openai"]["gpt"].(*OpenAIClient)
	result, err := oai.Call(context.Background(), buildTestRequest("A very long system prompt that exceeds the limit", "hi"))
	require.NoError(t, err)
	assert.Equal(t, "stop", result.GetAttr("stop_reason").AsString())
}

func TestOpenAICallSystemShorthand(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write(openAIResponseJSON("OK", "gpt-4o-mini", "stop"))
	}))
	defer srv.Close()

	vcl := []byte(fmt.Sprintf(`
client "openai" "gpt" {
    api_key  = "test-key"
    model    = "gpt-4o-mini"
    base_url = %q
}
`, srv.URL+"/"))

	cfg, diags := NewConfig().WithSources(vcl).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oai := cfg.Clients["openai"]["gpt"].(*OpenAIClient)
	_, err := oai.Call(context.Background(), buildTestRequest("You are a helpful assistant.", "Hello"))
	require.NoError(t, err)

	require.NotNil(t, capturedBody)
	messages, ok := capturedBody["messages"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages), 2, "system message should be prepended")

	firstMsg := messages[0].(map[string]any)
	assert.Equal(t, "system", firstMsg["role"])
	assert.Equal(t, "You are a helpful assistant.", firstMsg["content"])

	secondMsg := messages[1].(map[string]any)
	assert.Equal(t, "user", secondMsg["role"])
}
