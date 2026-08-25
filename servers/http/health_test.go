package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// mountedServer builds a config and returns the named server's handler, without
// binding anything.
func mountedServer(t *testing.T, src string, features ...string) http.Handler {
	t.Helper()
	builder := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop())
	for i := 0; i+1 < len(features); i += 2 {
		builder = builder.WithFeature(features[i], features[i+1])
	}
	config, diags := builder.Build()
	require.False(t, diags.HasErrors(), "%v", diags)
	config.Health.SetBooted()

	srv, ok := config.Servers["http"]["s"]
	require.True(t, ok, "server.s was not created")
	// Start() wraps this in otelhttp and friends; the bare mux is what the
	// routing assertions are about.
	return srv.(*HttpServer).Server.Handler
}

func probeGet(t *testing.T, h http.Handler, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	resp := rec.Result()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func TestHealthEndpointsAreOffByDefault(t *testing.T) {
	// Turning them on by default would silently claim three paths in
	// configurations that already work, and put an unauthenticated endpoint on
	// whichever listener the author declared.
	h := mountedServer(t, `
server "http" "s" {
    listen = ":0"

    handle "GET /hello" { action = "hi" }
}
`)

	for _, path := range cfg.HealthEndpointPaths {
		code, _ := probeGet(t, h, path)
		assert.Equal(t, http.StatusNotFound, code, path)
	}
}

func TestHealthEndpointsOnServesAllThreeTersely(t *testing.T) {
	h := mountedServer(t, `
server "http" "s" {
    listen           = ":0"
    health_endpoints = "on"
}

check "broker" {
    input  = false
    reason = "not connected to bus.internal"
}
`)

	code, body := probeGet(t, h, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "not ready\n", body)

	// "on" ignores ?verbose: the verbose body names your components and quotes
	// their errors, which is an information leak on a public listener.
	_, body = probeGet(t, h, "/readyz?verbose")
	assert.Equal(t, "not ready\n", body)
	assert.NotContains(t, body, "bus.internal")

	// A readiness check must not fail liveness.
	code, _ = probeGet(t, h, "/livez")
	assert.Equal(t, http.StatusOK, code)
	code, _ = probeGet(t, h, "/healthz")
	assert.Equal(t, http.StatusOK, code)
}

func TestHealthEndpointsVerboseHonorsTheQuery(t *testing.T) {
	h := mountedServer(t, `
server "http" "s" {
    listen           = ":0"
    health_endpoints = "verbose"
}

check "broker" {
    input  = false
    reason = "not connected to bus.internal"
}
`)

	_, body := probeGet(t, h, "/readyz?verbose")
	assert.Contains(t, body, "[-]check.broker failed: not connected to bus.internal")
	assert.Contains(t, body, "readyz check failed")

	// Still terse without asking.
	_, body = probeGet(t, h, "/readyz")
	assert.Equal(t, "not ready\n", body)
}

func TestAnExplicitRouteWinsOverTheMountedEndpoint(t *testing.T) {
	h := mountedServer(t, `
server "http" "s" {
    listen           = ":0"
    health_endpoints = "on"

    handle "GET /livez" { action = "mine" }
}
`)

	// Compared by path, not by pattern: `handle "GET /livez"` is a different
	// ServeMux pattern from `/livez` and would not collide at registration, but
	// splitting one path across two owners by method is worse than yielding it.
	_, body := probeGet(t, h, "/livez")
	assert.Equal(t, "mine", body)

	// The paths the author did not claim are still served.
	code, body := probeGet(t, h, "/readyz")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok\n", body)
	code, _ = probeGet(t, h, "/healthz")
	assert.Equal(t, http.StatusOK, code)
}

func TestAFilesBlockAlsoClaimsItsPath(t *testing.T) {
	dir := t.TempDir()
	h := mountedServer(t, `
server "http" "s" {
    listen           = ":0"
    health_endpoints = "on"

    files "/healthz" { directory = "`+dir+`" }
}
`, "readfiles", dir)

	// Not a 200 "ok": the files block owns the path.
	_, body := probeGet(t, h, "/healthz")
	assert.NotEqual(t, "ok\n", body)

	code, _ := probeGet(t, h, "/readyz")
	assert.Equal(t, http.StatusOK, code)
}

func TestInvalidHealthEndpointsIsReported(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources([]byte(`
server "http" "s" {
    listen           = ":0"
    health_endpoints = "yes"
}
`)).WithLogger(zap.NewNop()).Build()

	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "Invalid health_endpoints")
	assert.Contains(t, diags.Error(), `got "yes"`)
}
