package http

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

func newTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func loadConfig(t *testing.T, vcl string) *cfg.Config {
	t.Helper()
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	return config
}

func getHTTPClient(t *testing.T, config *cfg.Config, name string) *HTTPClient {
	t.Helper()
	clients, ok := config.Clients["http"]
	require.True(t, ok, "no http clients registered")
	c, ok := clients[name]
	require.True(t, ok, "no http client named %q", name)
	hc, ok := c.(*HTTPClient)
	require.True(t, ok, "client %q is not an *HTTPClient", name)
	return hc
}

// ── Block parsing ────────────────────────────────────────────────────────────

func TestHTTPClient_Minimal(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {}
`)
	c := getHTTPClient(t, config, "api")
	assert.Equal(t, "api", c.GetName())
	assert.Nil(t, c.BaseURL())
	assert.Empty(t, c.DefaultHeaders())
	assert.Equal(t, time.Duration(0), c.DefaultRequestTimeout())
	assert.Equal(t, 10, c.maxRedirects)
	assert.True(t, c.followRedirect)
	assert.False(t, c.keepAuthOnRdir)
}

func TestHTTPClient_BaseURL(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    base_url = "https://api.example.com/v1/"
}
`)
	c := getHTTPClient(t, config, "api")
	require.NotNil(t, c.BaseURL())
	assert.Equal(t, "https", c.BaseURL().Scheme)
	assert.Equal(t, "api.example.com", c.BaseURL().Host)
	assert.Equal(t, "/v1/", c.BaseURL().Path)
}

func TestHTTPClient_InvalidBaseURL(t *testing.T) {
	_, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {
    base_url = "::::not a url"
}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.True(t, diags.HasErrors())
}

func TestHTTPClient_Headers(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    headers = {
        "Accept"    = "application/json"
        "X-Custom"  = "value"
    }
}
`)
	c := getHTTPClient(t, config, "api")
	assert.Equal(t, "application/json", c.DefaultHeaders().Get("Accept"))
	assert.Equal(t, "value", c.DefaultHeaders().Get("X-Custom"))
}

func TestHTTPClient_Headers_MultiValue(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    headers = {
        "X-Multi" = ["a", "b", "c"]
    }
}
`)
	c := getHTTPClient(t, config, "api")
	assert.Equal(t, []string{"a", "b", "c"}, c.DefaultHeaders().Values("X-Multi"))
}

func TestHTTPClient_UserAgent(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    user_agent = "my-app/1.0"
}
`)
	c := getHTTPClient(t, config, "api")
	assert.Equal(t, "my-app/1.0", c.DefaultHeaders().Get("User-Agent"))
}

func TestHTTPClient_UserAgent_HeaderWins(t *testing.T) {
	// Explicit User-Agent header takes precedence over user_agent shorthand.
	config := loadConfig(t, `
client "http" "api" {
    user_agent = "shorthand/1.0"
    headers = {
        "User-Agent" = "explicit/2.0"
    }
}
`)
	c := getHTTPClient(t, config, "api")
	assert.Equal(t, "explicit/2.0", c.DefaultHeaders().Get("User-Agent"))
}

func TestHTTPClient_TimeoutShorthand(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    timeout = "45s"
}
`)
	c := getHTTPClient(t, config, "api")
	assert.Equal(t, 45*time.Second, c.DefaultRequestTimeout())
}

func TestHTTPClient_Redirects(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    redirects {
        follow                 = false
        max                    = 3
        keep_auth_on_redirect  = true
    }
}
`)
	c := getHTTPClient(t, config, "api")
	assert.False(t, c.followRedirect)
	assert.Equal(t, 3, c.maxRedirects)
	assert.True(t, c.keepAuthOnRdir)
}

func TestHTTPClient_Redirects_NegativeMax(t *testing.T) {
	_, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {
    redirects { max = -1 }
}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.True(t, diags.HasErrors())
}

func TestHTTPClient_ConnectionPool(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    http2                    = false
    max_connections_per_host = 50
    max_idle_connections     = 20
    disable_keep_alives      = true
}
`)
	c := getHTTPClient(t, config, "api")
	// These live on the transport, exercised indirectly — we just verify the
	// config loaded without error.
	assert.NotNil(t, c.httpClient.Transport)
}

func TestHTTPClient_DeferredBlocksAccepted(t *testing.T) {
	// Spec-conformant blocks from later phases should parse without error,
	// even though their behavior is not yet implemented.
	config := loadConfig(t, `
client "http" "api" {
    base_url = "https://api.example.com"

    auth              = "Bearer token"
    auth_max_lifetime = "55m"
    auth_max_failures = 3

    auth_retry_backoff {
        initial_delay = "1s"
    }

    retry {
        max_attempts = 3
    }

    cookies {
        enabled = true
    }

    otel {
        propagate = false
    }
}
`)
	c := getHTTPClient(t, config, "api")
	assert.Equal(t, "api", c.GetName())
}

func TestHTTPClient_Disabled(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {
    disabled = true
    base_url = "https://nope"
}
`)
	// Disabled clients are not registered at all.
	_, ok := config.Clients["http"]
	assert.False(t, ok)
}

func TestHTTPClient_Duplicate(t *testing.T) {
	_, diags := cfg.NewConfig().
		WithSources([]byte(`
client "http" "api" {}
client "http" "api" {}
`)).
		WithLogger(newTestLogger(t)).
		Build()
	require.True(t, diags.HasErrors())
}

// ── NullClient ───────────────────────────────────────────────────────────────

func TestNullClient_Defaults(t *testing.T) {
	c := NullClient()
	require.NotNil(t, c)
	assert.Nil(t, c.BaseURL())
	assert.Empty(t, c.DefaultHeaders())
	assert.Equal(t, 30*time.Second, c.DefaultRequestTimeout())
	assert.True(t, c.followRedirect)
	assert.Equal(t, 10, c.maxRedirects)
	assert.False(t, c.keepAuthOnRdir)
	assert.NotNil(t, c.httpClient)
}

func TestNullClient_Shared(t *testing.T) {
	// NullClient returns a shared instance; no per-call allocation.
	c1 := NullClient()
	c2 := NullClient()
	assert.Same(t, c1, c2)
}

// ── HTTPCallable interface ───────────────────────────────────────────────────

func TestHTTPClient_SatisfiesHTTPCallable(t *testing.T) {
	var _ HTTPCallable = (*HTTPClient)(nil)
}

// ── CtyValue ─────────────────────────────────────────────────────────────────

func TestHTTPClient_CtyValue_IsClientCapsule(t *testing.T) {
	config := loadConfig(t, `
client "http" "api" {}
`)
	v, ok := config.CtyClientMap["api"]
	require.True(t, ok)
	assert.Equal(t, cfg.ClientCapsuleType, v.Type())
}
