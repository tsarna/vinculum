package functions

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

// probeResponse calls one of the probe builders and unwraps the response.
func probeResponse(t *testing.T, config *cfg.Config, name string, args ...cty.Value) *types.HTTPResponseWrapper {
	t.Helper()
	val := call(t, config, name, args...)
	resp, ok := types.GetHTTPResponseFromValue(val)
	require.True(t, ok, "%s should return an http_response", name)
	return resp
}

func TestProbeFunctionsBuildAResponse(t *testing.T) {
	config, ctx := buildHealth(t, `
check "broker" {
    input  = false
    reason = "not connected to bus.internal"
}
`)

	ready := probeResponse(t, config, "http::readyz", ctx)
	assert.Equal(t, 503, ready.Status)
	assert.Equal(t, "not ready\n", string(ready.Body))
	assert.Equal(t, "no-store", ready.Headers.Get("Cache-Control"))

	// A readiness failure must not fail liveness.
	live := probeResponse(t, config, "http::livez", ctx)
	assert.Equal(t, 200, live.Status)
	assert.Equal(t, "ok\n", string(live.Body))
}

func TestProbeFunctionWithoutARequestIsTerse(t *testing.T) {
	config, ctx := buildHealth(t, `
check "broker" {
    input  = false
    reason = "not connected to bus.internal"
}
`)

	resp := probeResponse(t, config, "http::readyz", ctx)
	assert.NotContains(t, string(resp.Body), "bus.internal")
}

func TestProbeFunctionNegotiatesFromARequest(t *testing.T) {
	config, ctx := buildHealth(t, `
check "broker" {
    input  = false
    reason = "not connected to bus.internal"
}
`)

	// A hand-written handle always honors ?verbose: that is the author's
	// choice to make, unlike the built-in endpoints.
	req := types.BuildHTTPRequestObject(httptest.NewRequest("GET", "/readyz?verbose", nil), nil)
	resp := probeResponse(t, config, "http::readyz", ctx, req)
	assert.Contains(t, string(resp.Body), "[-]check.broker failed: not connected to bus.internal")

	// Passing the request also gives the function the method, so a HEAD gets
	// the status with no body without relying on the server to strip it.
	head := types.BuildHTTPRequestObject(httptest.NewRequest("HEAD", "/readyz", nil), nil)
	resp = probeResponse(t, config, "http::readyz", ctx, head)
	assert.Equal(t, 503, resp.Status)
	assert.Empty(t, resp.Body)
}

func TestProbeFunctionTakesAnOptionsObject(t *testing.T) {
	config, ctx := buildHealth(t, `
check "broker" {
    input  = false
    reason = "not connected to bus.internal"
}
`)

	verbose := cty.ObjectVal(map[string]cty.Value{"verbose": cty.True})
	resp := probeResponse(t, config, "http::readyz", ctx, verbose)
	assert.Contains(t, string(resp.Body), "not connected to bus.internal")

	asJSON := cty.ObjectVal(map[string]cty.Value{
		"format":  cty.StringVal("json"),
		"verbose": cty.True,
	})
	resp = probeResponse(t, config, "http::readyz", ctx, asJSON)
	assert.Equal(t, "application/json", resp.ContentType)
	assert.Contains(t, string(resp.Body), `"component":"check.broker"`)
}

func TestProbeFunctionRejectsABadOption(t *testing.T) {
	config, ctx := buildHealth(t, `check "c" { input = true }`)

	fn := config.EvalCtx().Functions["http::readyz"]

	_, err := fn.Call([]cty.Value{ctx, cty.ObjectVal(map[string]cty.Value{"verbse": cty.True})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown option "verbse"`)

	_, err = fn.Call([]cty.Value{ctx, cty.ObjectVal(map[string]cty.Value{"format": cty.StringVal("xml")})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `got "xml"`)

	_, err = fn.Call([]cty.Value{ctx, cty.StringVal("verbose")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected ctx.request or an options object")
}
