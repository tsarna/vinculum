package httpserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "github.com/tsarna/vinculum/ambient"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"
)

// buildFrom builds a config from inline VCL and returns the main server's handler.
func buildFrom(t *testing.T, vcl string) http.Handler {
	t.Helper()

	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	return c.Servers["http"]["main"].(*httpserver.HttpServer).Server.Handler
}

// buildFails builds a config expected to be rejected, and returns the message.
func buildFails(t *testing.T, vcl string) string {
	t.Helper()

	_, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors(), "config was accepted, want an error")
	return diags.Error()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func get(t *testing.T, handler http.Handler, path string, headers map[string]string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Result()
}

// The issuer is deliberately unreachable — every assertion here is about which
// mechanism judges a request, not about what a working issuer says. Its address
// is one nothing is listening on, so connecting is refused rather than left to
// time out.
const twoMechanisms = `
auth "oidc" "corp" {
  jwks_url = "${env.VINCULUM_TEST_DEAD_URL}/jwks.json"
}

auth "basic" "break_glass" {
  credentials = { ops = "letmein" }
}

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/admin" {
    auth   = [auth.corp, auth.break_glass]
    action = "ok ${ctx.auth.method}"
  }
}
`

// TestFirstClaimantDecides is the core of accepting several mechanisms: the one
// that recognizes the credential judges the request, and its rejection is final.
//
// Falling through on a bad credential would let a caller grind against every
// mechanism a route accepts, and would make which one rejected them unknowable.
func TestFirstClaimantDecides(t *testing.T) {
	t.Setenv("VINCULUM_TEST_DEAD_URL", unreachableURL(t))
	handler := buildFrom(t, twoMechanisms)

	t.Run("basic credentials reach the basic mechanism", func(t *testing.T) {
		resp := get(t, handler, "/admin", map[string]string{
			"Authorization": basicAuthHeader("ops", "letmein"),
		})
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("a bad bearer token is rejected, not passed on", func(t *testing.T) {
		// The issuer is unreachable, so oidc cannot verify anything — but it
		// claimed the request, so the answer is its 503 rather than a fallthrough
		// to basic auth. A token holder must not be able to reach the password
		// prompt by presenting a broken token.
		resp := get(t, handler, "/admin", map[string]string{
			"Authorization": "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig",
		})
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})

	t.Run("no credential is offered every challenge", func(t *testing.T) {
		resp := get(t, handler, "/admin", nil)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.ElementsMatch(t,
			[]string{"Bearer", `Basic realm="break_glass"`},
			resp.Header.Values("WWW-Authenticate"),
			"a 401 should name every mechanism the route accepts")
	})
}

// TestAuthAnonymousLast covers the wiki case: anonymous readers welcome, bad
// credentials still rejected.
func TestAuthAnonymousLast(t *testing.T) {
	handler := buildFrom(t, `
auth "basic" "web" {
  credentials = { alice = "secret" }
}

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/wiki" {
    auth   = [auth.web, auth.anonymous]
    action = ctx.auth == null ? "anonymous" : "hello ${ctx.auth.username}"
  }
}
`)

	t.Run("no credential proceeds anonymously", func(t *testing.T) {
		resp := get(t, handler, "/wiki", nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "anonymous", readBody(t, resp))
	})

	t.Run("a good credential authenticates", func(t *testing.T) {
		resp := get(t, handler, "/wiki", map[string]string{
			"Authorization": basicAuthHeader("alice", "secret"),
		})
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "hello alice", readBody(t, resp))
	})

	t.Run("a bad credential is still rejected", func(t *testing.T) {
		// This is what makes auth.anonymous safe in a list: it admits requests that
		// carry nothing, not requests that carry something wrong.
		resp := get(t, handler, "/wiki", map[string]string{
			"Authorization": basicAuthHeader("alice", "wrong"),
		})
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// TestAuthAnonymousMustBeLast rejects an ordering that reads as though anonymous
// access wins when it does not.
func TestAuthAnonymousMustBeLast(t *testing.T) {
	msg := buildFails(t, `
auth "basic" "web" { credentials = { alice = "secret" } }

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/x" {
    auth   = [auth.anonymous, auth.web]
    action = "ok"
  }
}
`)
	assert.Contains(t, msg, "auth.anonymous must be last")
}

// TestDisabledMechanismLeavesOthersEnforcing is the reason auth.disabled is a
// separate sentinel from auth.anonymous. Aliasing the two would turn this route
// anonymous, which is the opposite of what disabling one of two mechanisms means.
func TestDisabledMechanismLeavesOthersEnforcing(t *testing.T) {
	handler := buildFrom(t, `
auth "oidc" "corp" {
  disabled = true
  issuer   = "https://accounts.example.com"
}

auth "basic" "web" {
  credentials = { alice = "secret" }
}

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/x" {
    auth   = [auth.corp, auth.web]
    action = "ok"
  }
}
`)

	resp := get(t, handler, "/x", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"disabling one of two mechanisms must not open the route")

	resp = get(t, handler, "/x", map[string]string{
		"Authorization": basicAuthHeader("alice", "secret"),
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestDisablingTheOnlyMechanismOpensTheRoute pins the other half: this has
// always been what disabling auth means, and it is the intended dev/prod toggle.
func TestDisablingTheOnlyMechanismOpensTheRoute(t *testing.T) {
	handler := buildFrom(t, `
auth "basic" "web" {
  disabled = true
}

server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.web

  handle "/x" { action = "ok" }
}
`)

	resp := get(t, handler, "/x", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestSameNameSelectsTheEnabledOne covers the "declare both, let the environment
// choose" idiom. The two declarations deliberately use different mechanisms.
func TestSameNameSelectsTheEnabledOne(t *testing.T) {
	handler := buildFrom(t, `
auth "oidc" "site" {
  disabled = true
  issuer   = "https://accounts.example.com"
}

auth "basic" "site" {
  credentials = { dev = "dev" }
}

server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.site

  handle "/x" { action = "ok ${ctx.auth.method}" }
}
`)

	resp := get(t, handler, "/x", map[string]string{
		"Authorization": basicAuthHeader("dev", "dev"),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok basic", readBody(t, resp),
		"the enabled declaration should be the one that authenticated")
}

// TestAuthResolvesRegardlessOfDeclarationOrder pins something invisible: a
// server picks up its `auth.<name>` reference as a dependency through the
// generic reference extraction every block handler inherits, and the auth
// handler chains same-name declarations so a dependent waits for all of them,
// not just whichever registered the shared id last.
//
// Nothing in either mechanism is specific to auth, so nothing about auth would
// fail loudly if either stopped working — the name would simply resolve to
// whatever had been bound so far, which for a half-processed name is the
// disabled sentinel. That is a config that silently serves unauthenticated.
func TestAuthResolvesRegardlessOfDeclarationOrder(t *testing.T) {
	// The server is written first, and the declaration that wins is written last.
	handler := buildFrom(t, `
server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.site

  handle "/x" { action = "ok ${ctx.auth.method}" }
}

auth "oidc" "site" {
  disabled = true
  issuer   = "https://accounts.example.com"
}

auth "basic" "site" {
  credentials = { dev = "dev" }
}
`)

	resp := get(t, handler, "/x", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"the route must be protected by the enabled declaration, not left open "+
			"because the name was read before that declaration was processed")

	resp = get(t, handler, "/x", map[string]string{
		"Authorization": basicAuthHeader("dev", "dev"),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok basic", readBody(t, resp))
}

func TestTwoEnabledDeclarationsOfOneNameIsAnError(t *testing.T) {
	msg := buildFails(t, `
auth "basic" "site" { credentials = { a = "1" } }
auth "basic" "site" { credentials = { b = "2" } }

server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.site
  handle "/x" { action = "ok" }
}
`)
	assert.Contains(t, msg, "Duplicate auth name")
}

func TestReservedAuthNames(t *testing.T) {
	for _, name := range []string{"anonymous", "disabled"} {
		t.Run(name, func(t *testing.T) {
			msg := buildFails(t, `
auth "basic" "`+name+`" { credentials = { a = "1" } }

server "http" "main" {
  listen = "127.0.0.1:0"
  handle "/x" { action = "ok" }
}
`)
			assert.Contains(t, msg, "Reserved auth name")
		})
	}
}

// TestOneAuthenticatorPerBlock is the fix for authenticator amplification: a
// server-level mechanism inherited by many routes is one instance, not one per
// route. It is observable through Startables, since each OIDC authenticator
// registers one.
func TestOneAuthenticatorPerBlock(t *testing.T) {
	t.Setenv("VINCULUM_TEST_DEAD_URL", unreachableURL(t))

	c, diags := cfg.NewConfig().WithSources([]byte(`
auth "oidc" "corp" {
  jwks_url = "${env.VINCULUM_TEST_DEAD_URL}/jwks.json"
}

server "http" "main" {
  listen = "127.0.0.1:0"
  auth   = auth.corp

  handle "/a" { action = "a" }
  handle "/b" { action = "b" }
  handle "/c" { action = "c" }
}
`)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// One http server plus one OIDC authenticator — not one authenticator per
	// route that inherited it.
	assert.Len(t, c.Startables, 2)
}

// TestCustomClaimsSelectsMechanism checks that a custom mechanism can say which
// requests are its own, which is the only way to order it against another.
func TestCustomClaimsSelectsMechanism(t *testing.T) {
	handler := buildFrom(t, `
auth "custom" "apikey" {
  claims = get(ctx.request, "header", "X-Api-Key") != ""
  action = get(ctx.request, "header", "X-Api-Key") == "s3cret" ? { subject = "svc" } : null
}

auth "basic" "web" {
  credentials = { alice = "secret" }
}

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/x" {
    auth   = [auth.apikey, auth.web]
    action = "${ctx.auth.method}:${ctx.auth.subject}"
  }
}
`)

	t.Run("the api key claims its own requests", func(t *testing.T) {
		resp := get(t, handler, "/x", map[string]string{"X-Api-Key": "s3cret"})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "custom:svc", readBody(t, resp))
	})

	t.Run("a bad api key is rejected rather than falling through", func(t *testing.T) {
		resp := get(t, handler, "/x", map[string]string{"X-Api-Key": "wrong"})
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("without the header, basic auth judges", func(t *testing.T) {
		resp := get(t, handler, "/x", map[string]string{
			"Authorization": basicAuthHeader("alice", "secret"),
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "basic:alice", readBody(t, resp))
	})
}
