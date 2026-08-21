package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// The external URL is a loopback address nothing listens on. It is never dialled
// — it only has to be the string a client would have been configured with, since
// that is what the document's `resource` is checked against. Loopback because a
// client refuses plain http anywhere else.
const externalBase = "http://127.0.0.1:9/vinculum"

// A resource is published beside the route it names, and the issuer is
// unreachable throughout: every assertion here is about discovery, which is what
// a client does *before* it has a token and therefore before any issuer is
// consulted.
const publishedResource = `
auth "oidc" "corp" {
  issuer   = "http://127.0.0.1:9/idp"
  resource = "/mcp"
}

server "http" "main" {
  listen       = "127.0.0.1:0"
  external_url = "` + externalBase + `"

  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}
`

// TestMetadataDocumentSatisfiesAClient runs the published document through the
// same reader a real MCP client uses, rather than through assertions of our own
// about what we think it wants. That reader enforces RFC 9728 §3.3 — the
// document's resource must equal the URL the client was configured with — which
// is the requirement most easily broken by a plausible-looking change.
func TestMetadataDocumentSatisfiesAClient(t *testing.T) {
	ts := httptest.NewServer(buildFrom(t, publishedResource))
	defer ts.Close()

	prm, err := oauthex.GetProtectedResourceMetadata(
		context.Background(),
		ts.URL+"/.well-known/oauth-protected-resource/vinculum/mcp",
		externalBase+"/mcp",
		ts.Client(),
	)
	require.NoError(t, err)
	assert.Equal(t, externalBase+"/mcp", prm.Resource)
	assert.Equal(t, []string{"http://127.0.0.1:9/idp"}, prm.AuthorizationServers)
	assert.Equal(t, []string{"header"}, prm.BearerMethodsSupported)
}

// TestMetadataPathInsertsTheResourcePath pins the URL the document lives at.
// A client that gets no challenge still finds it by path insertion, so the
// location is part of the contract even when the header is correct.
//
// The path inserted is the *external* one, prefix included: the proxy that
// mounts this server under /vinculum has to route the well-known path through
// too, since it sits at the host root rather than under the prefix.
func TestMetadataPathInsertsTheResourcePath(t *testing.T) {
	handler := buildFrom(t, publishedResource)

	resp := get(t, handler, "/.well-known/oauth-protected-resource/vinculum/mcp", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Not at the root, where the document would claim an identifier no client
	// asked about.
	resp = get(t, handler, "/.well-known/oauth-protected-resource", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestMetadataIsServedUnauthenticated is the invariant the whole feature rests
// on. The document exists for a client that cannot authenticate yet, so putting
// it behind the auth middleware would make it unreachable by everyone it is for.
func TestMetadataIsServedUnauthenticated(t *testing.T) {
	handler := buildFrom(t, publishedResource)

	// The route it describes is protected...
	resp := get(t, handler, "/mcp", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// ...and the document describing it is not.
	resp = get(t, handler, "/.well-known/oauth-protected-resource/vinculum/mcp", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

// TestChallengePointsAtTheDocument closes the bootstrap loop: the 401 a client
// gets first has to name the document, and name it in the form the client
// parses.
func TestChallengePointsAtTheDocument(t *testing.T) {
	resp := get(t, buildFrom(t, publishedResource), "/mcp", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	require.NoError(t, err)
	require.Len(t, challenges, 1)
	assert.Equal(t, "bearer", challenges[0].Scheme) // the parser lowercases it

	// At the host root, not under the /vinculum prefix: RFC 9728 inserts the
	// resource's whole path after the well-known prefix, so the document lives
	// outside the path the proxy forwards and the proxy needs its own rule for it.
	assert.Equal(t,
		"http://127.0.0.1:9/.well-known/oauth-protected-resource/vinculum/mcp",
		challenges[0].Params["resource_metadata"])
}

// TestRejectedTokenAlsoPointsAtTheDocument covers RFC 9728 §5.1's "every 401",
// not just the credential-less one. An expired token is how a working client
// ordinarily arrives here, and it has to re-find the issuer to renew.
//
// The issuer is unreachable, so a syntactically valid token cannot be verified;
// what matters is that the rejection carries the pointer, whatever the reason.
func TestRejectedTokenAlsoPointsAtTheDocument(t *testing.T) {
	resp := get(t, buildFrom(t, publishedResource), "/mcp", map[string]string{
		"Authorization": "Bearer not-a-real-token",
	})
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusServiceUnavailable}, resp.StatusCode)

	if resp.StatusCode == http.StatusUnauthorized {
		assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "resource_metadata=")
	}
}

// TestAbsoluteResourceNeedsNoExternalURL: external_url exists only to resolve a
// relative resource, so writing the identifier out in full must not require it.
func TestAbsoluteResourceNeedsNoExternalURL(t *testing.T) {
	handler := buildFrom(t, `
auth "oidc" "corp" {
  issuer   = "http://127.0.0.1:9/idp"
  resource = "http://127.0.0.1:9/mcp"
}

server "http" "main" {
  listen = "127.0.0.1:0"
  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}
`)

	resp := get(t, handler, "/.well-known/oauth-protected-resource/mcp", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestNoResourceServesNoDocument: the feature is opt-in. An OIDC block that
// publishes nothing must behave exactly as it did before, challenge included.
func TestNoResourceServesNoDocument(t *testing.T) {
	handler := buildFrom(t, `
auth "oidc" "corp" {
  issuer = "http://127.0.0.1:9/idp"
}

server "http" "main" {
  listen = "127.0.0.1:0"
  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}
`)

	resp := get(t, handler, "/.well-known/oauth-protected-resource/mcp", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp = get(t, handler, "/mcp", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))
}

func TestRelativeResourceNeedsExternalURL(t *testing.T) {
	msg := buildFails(t, `
auth "oidc" "corp" {
  issuer   = "http://127.0.0.1:9/idp"
  resource = "/mcp"
}

server "http" "main" {
  listen = "127.0.0.1:0"
  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}
`)
	assert.Contains(t, msg, "external_url")
}

// TestResourceRequiresIssuer: authorization_servers carries the issuer
// identifier, and a jwks_url names a key endpoint that cannot be turned back
// into one. Rejected at load rather than published as an empty list.
func TestResourceRequiresIssuer(t *testing.T) {
	msg := buildFails(t, `
auth "oidc" "corp" {
  jwks_url = "http://127.0.0.1:9/jwks.json"
  resource = "/mcp"
}

server "http" "main" {
  listen       = "127.0.0.1:0"
  external_url = "`+externalBase+`"
  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}
`)
	assert.Contains(t, msg, "issuer")
}

// TestOneRelativeResourceCannotSpanTwoServers is the identity rule: a block is
// one protected resource, so a relative resource resolved against two different
// external URLs would be two resources sharing a name, and a client dialling one
// of them would be told about the other.
func TestOneRelativeResourceCannotSpanTwoServers(t *testing.T) {
	msg := buildFails(t, `
auth "oidc" "corp" {
  issuer   = "http://127.0.0.1:9/idp"
  resource = "/mcp"
}

server "http" "main" {
  listen       = "127.0.0.1:0"
  external_url = "http://127.0.0.1:9"
  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}

server "http" "other" {
  listen       = "127.0.0.1:0"
  external_url = "http://127.0.0.2:9"
  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}
`)
	assert.Contains(t, msg, "two resource identities")
}

// TestInsecureResourceIsRejected: a client refuses a non-loopback plain-http
// metadata URL, and reports it as a failure of the resource rather than of the
// deployment. Catch it where the fix is visible instead.
func TestInsecureResourceIsRejected(t *testing.T) {
	msg := buildFails(t, `
auth "oidc" "corp" {
  issuer   = "https://idp.example.com"
  resource = "/mcp"
}

server "http" "main" {
  listen       = "127.0.0.1:0"
  external_url = "http://api.example.com"
  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }
}
`)
	assert.Contains(t, msg, "loopback")
}

func TestInvalidExternalURL(t *testing.T) {
	for name, external := range map[string]string{
		"no scheme": "api.example.com",
		"not http":  "ftp://api.example.com",
		"no host":   "https:///mcp",
		"has query": "https://api.example.com?x=1",
	} {
		t.Run(name, func(t *testing.T) {
			msg := buildFails(t, `
server "http" "main" {
  listen       = "127.0.0.1:0"
  external_url = "`+external+`"
  handle "/x" { action = "ok" }
}
`)
			assert.Contains(t, msg, "external_url")
		})
	}
}

// TestOneDocumentPerBlockNotPerRoute: the resource identifies the block, so two
// routes naming it share one document rather than racing to register two
// patterns — which, being the same pattern, would be a mux collision.
func TestOneDocumentPerBlockNotPerRoute(t *testing.T) {
	handler := buildFrom(t, `
auth "oidc" "corp" {
  issuer   = "http://127.0.0.1:9/idp"
  resource = "/mcp"
}

server "http" "main" {
  listen       = "127.0.0.1:0"
  external_url = "http://127.0.0.1:9"

  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }

  handle "/api/" {
    auth   = auth.corp
    action = "ok"
  }
}
`)

	resp := get(t, handler, "/.well-known/oauth-protected-resource/mcp", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), `"resource":"http://127.0.0.1:9/mcp"`)
}

// TestExtraProtectedRoutesWarn: discovery works from the route the identifier
// names and fails from every other route the block guards, while a client that
// already holds a token is served by all of them. That split is quiet enough to
// need saying out loud, and legitimate enough not to be an error.
func TestExtraProtectedRoutesWarn(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)

	_, diags := cfg.NewConfig().
		WithSources([]byte(`
auth "oidc" "corp" {
  issuer   = "http://127.0.0.1:9/idp"
  resource = "/mcp"
}

server "http" "main" {
  listen       = "127.0.0.1:0"
  external_url = "http://127.0.0.1:9"

  handle "/mcp" {
    auth   = auth.corp
    action = "ok"
  }

  handle "/api/" {
    auth   = auth.corp
    action = "ok"
  }
}
`)).
		WithLogger(zap.New(core)).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	entries := logs.FilterMessageSnippet("does not name every route").All()
	require.Len(t, entries, 1)
	assert.Equal(t, []any{"/api/"}, entries[0].ContextMap()["routes"])
}

// TestUnreferencedResourceWarns covers the case nothing at request time can
// reveal: a block configured for discovery that no `server "http"` route
// references publishes nothing, and looks identical to a block that never asked.
//
// It is also what the whole-config pass exists for — until every block has been
// processed, "no server referenced it" cannot be told from "no server has
// referenced it yet".
func TestUnreferencedResourceWarns(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)

	_, diags := cfg.NewConfig().
		WithSources([]byte(`
auth "oidc" "corp" {
  issuer   = "http://127.0.0.1:9/idp"
  resource = "/mcp"
}

server "http" "main" {
  listen = "127.0.0.1:0"

  handle "/open" {
    action = "ok"
  }
}
`)).
		WithLogger(zap.New(core)).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	entries := logs.FilterMessageSnippet("declares a resource that nothing publishes").All()
	require.Len(t, entries, 1)
	assert.Equal(t, "/mcp", entries[0].ContextMap()["resource"])
}

// TestPublishedResourceDoesNotWarn guards the other side of it: the warning must
// stay quiet for the configuration it is meant to contrast with, or it trains
// people to ignore it.
func TestPublishedResourceDoesNotWarn(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)

	_, diags := cfg.NewConfig().
		WithSources([]byte(publishedResource)).
		WithLogger(zap.New(core)).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())
	assert.Zero(t, logs.FilterMessageSnippet("nothing publishes").Len())
}

// TestMetadataRejectsNonGET: the document is a read, and CORS preflight has to
// work for a browser-based client to reach it at all.
func TestMetadataRejectsNonGET(t *testing.T) {
	handler := buildFrom(t, publishedResource)
	const path = "/.well-known/oauth-protected-resource/vinculum/mcp"

	req := httptest.NewRequest(http.MethodOptions, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Result().StatusCode)

	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Result().StatusCode)
}
