package functions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	clientshttp "github.com/tsarna/vinculum/clients/http"
	bytescty "github.com/tsarna/bytes-cty-type"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

func newTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	l, err := zap.NewDevelopment()
	require.NoError(t, err)
	return l
}

// loadClient returns a cty client capsule for an HCL `client "http"` block.
// Callers pass a printf format string ending at the closing brace so the
// test server URL can be interpolated into base_url.
func loadClient(t *testing.T, vcl string) cty.Value {
	t.Helper()
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	v, ok := config.CtyClientMap["api"]
	require.True(t, ok, "no client named 'api' registered")
	return v
}

// ctxVal builds a ctx cty value wrapping the given context.Context.
func ctxVal(ctx context.Context) cty.Value {
	return richcty.NewContextCapsule(ctx)
}

// nullClientVal is a cty null of dynamic type, which the verb functions
// accept as the "use default" client.
var nullClientVal = cty.NullVal(cty.DynamicPseudoType)

// bgCtx is a shortcut for a context.Background-backed ctx argument.
var bgCtx = ctxVal(context.Background())

// callVerb calls a verb function by name with the given args. It returns the
// cty result or an error. Uses the function registry so we also exercise
// registration.
func callVerb(t *testing.T, name string, args ...cty.Value) (cty.Value, error) {
	t.Helper()
	fns := GetHTTPClientFunctions(nil)
	fn, ok := fns[name]
	require.True(t, ok, "unknown verb function %q", name)
	return fn.Call(args)
}

// callVerbWithConfig is like callVerb but uses the function set built from
// the supplied config, so that retry.on_response and other lazy expressions
// have an eval context to resolve against.
func callVerbWithConfig(t *testing.T, config *cfg.Config, name string, args ...cty.Value) (cty.Value, error) {
	t.Helper()
	fns := GetHTTPClientFunctions(config)
	fn, ok := fns[name]
	require.True(t, ok, "unknown verb function %q", name)
	return fn.Call(args)
}

// ── Basic success paths ─────────────────────────────────────────────────────

func TestHTTPGet_Basic(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/ping", r.URL.Path)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "pong")
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/ping"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, cty.BoolVal(true), resp.GetAttr("ok"))
	assert.Equal(t, cty.StringVal("text/plain"), resp.GetAttr("content_type"))

	w, ok := types.GetHTTPClientResponseFromValue(resp)
	require.True(t, ok)
	body, err := w.Get(context.Background(), []cty.Value{cty.StringVal("body")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("pong"), body)
}

func TestHTTPGet_404_NotAnError(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.NotFound(w, r)
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/missing"))
	require.NoError(t, err, "404 should not be a function error")
	assert.Equal(t, cty.NumberIntVal(404), resp.GetAttr("status"))
	assert.Equal(t, cty.BoolVal(false), resp.GetAttr("ok"))
}

// ── Client block + base_url resolution ──────────────────────────────────────

func TestHTTPGet_ClientBaseURL_Relative(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "/v1/things/42", r.URL.Path)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
}
`, srv.URL+"/v1/"))

	// "things/42" is a relative reference that resolves against /v1/.
	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("things/42"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
}

func TestHTTPGet_ClientBaseURL_AbsoluteOverrides(t *testing.T) {
	var hitExpected int32
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		atomic.AddInt32(&hitExpected, 1)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	// base_url points at a bogus host; the absolute URL should win.
	clientVal := loadClient(t, `
client "http" "api" {
    base_url = "http://does-not-exist.invalid/"
}
`)
	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal(srv.URL+"/abs"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, int32(1), atomic.LoadInt32(&hitExpected))
}

// ── base_url query merging ──────────────────────────────────────────────────

func TestHTTPGet_BaseURLQuery_MergedIntoRelativeCall(t *testing.T) {
	// base_url has an api_key; the call has no query of its own. The
	// api_key should be sent on the wire.
	var gotQuery url.Values
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotQuery = r.URL.Query()
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = "%s/v1/?api_key=secret"
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("things"))
	require.NoError(t, err)
	assert.Equal(t, "secret", gotQuery.Get("api_key"))
}

func TestHTTPGet_BaseURLQuery_PreservedWhenCallHasOwnQuery(t *testing.T) {
	// Call URL has its own query string. Base's api_key must still be merged
	// in alongside the call's params.
	var gotQuery url.Values
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotQuery = r.URL.Query()
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = "%s/v1/?api_key=secret"
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("things?page=2"))
	require.NoError(t, err)
	assert.Equal(t, "secret", gotQuery.Get("api_key"))
	assert.Equal(t, "2", gotQuery.Get("page"))
}

func TestHTTPGet_BaseURLQuery_CallWinsOnKeyCollision(t *testing.T) {
	// Both base and call set the same key. The call's value wins.
	var gotQuery url.Values
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotQuery = r.URL.Query()
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = "%s/v1/?fmt=xml"
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("things?fmt=json"))
	require.NoError(t, err)
	assert.Equal(t, []string{"json"}, gotQuery["fmt"], "call value should win, base value dropped")
}

func TestHTTPGet_BaseURLQuery_MergedWithOptsQuery(t *testing.T) {
	// base_url has api_key; opts.query adds another param. Both should
	// reach the wire.
	var gotQuery url.Values
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotQuery = r.URL.Query()
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = "%s/v1/?api_key=secret"
}
`, srv.URL))

	opts := cty.ObjectVal(map[string]cty.Value{
		"query": cty.ObjectVal(map[string]cty.Value{
			"page": cty.StringVal("2"),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("things"), opts)
	require.NoError(t, err)
	assert.Equal(t, "secret", gotQuery.Get("api_key"))
	assert.Equal(t, "2", gotQuery.Get("page"))
}

func TestHTTPGet_BaseURLQuery_NotAppliedToAbsoluteCall(t *testing.T) {
	// When the call URL is absolute, base_url is bypassed entirely — and
	// so are its query params.
	var gotQuery url.Values
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotQuery = r.URL.Query()
	}))
	defer srv.Close()

	// Use a different base_url so we know the absolute one really overrode it.
	clientVal := loadClient(t, `
client "http" "api" {
    base_url = "http://does-not-exist.invalid/?api_key=secret"
}
`)
	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal(srv.URL+"/abs"))
	require.NoError(t, err)
	assert.Empty(t, gotQuery.Get("api_key"), "absolute call URL should not inherit base query")
}

// ── Header precedence ───────────────────────────────────────────────────────

func TestHTTPGet_DefaultUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotUA = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL))
	require.NoError(t, err)
	assert.Equal(t, "vinculum", gotUA)
}

func TestHTTPGet_ClientDefaultHeadersSent(t *testing.T) {
	var gotAccept, gotCustom string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotAccept = r.Header.Get("Accept")
		gotCustom = r.Header.Get("X-Custom")
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    headers = {
        "Accept"   = "application/json"
        "X-Custom" = "value"
    }
}
`, srv.URL))
	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, "application/json", gotAccept)
	assert.Equal(t, "value", gotCustom)
}

func TestHTTPGet_PerCallHeadersOverride(t *testing.T) {
	var got nethttp.Header
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    headers = {
        "Accept"   = "application/json"
        "X-Custom" = "client-value"
    }
}
`, srv.URL))

	opts := cty.ObjectVal(map[string]cty.Value{
		"headers": cty.ObjectVal(map[string]cty.Value{
			"X-Custom": cty.StringVal("call-value"),
			"X-New":    cty.StringVal("added"),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Equal(t, "application/json", got.Get("Accept"), "client header that wasn't overridden survives")
	assert.Equal(t, "call-value", got.Get("X-Custom"), "per-call header overrides client header")
	assert.Equal(t, "added", got.Get("X-New"))
}

func TestHTTPGet_PerCallHeaderNullDeletes(t *testing.T) {
	var got nethttp.Header
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    headers = {
        "X-Gone" = "should-be-removed"
    }
}
`, srv.URL))

	opts := cty.ObjectVal(map[string]cty.Value{
		"headers": cty.ObjectVal(map[string]cty.Value{
			"X-Gone": cty.NullVal(cty.String),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Empty(t, got.Values("X-Gone"))
}

// ── POST body handling ──────────────────────────────────────────────────────

func TestHTTPPost_JSONBody(t *testing.T) {
	var gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	body := cty.ObjectVal(map[string]cty.Value{
		"name":  cty.StringVal("frobnicator"),
		"color": cty.StringVal("blue"),
	})
	resp, err := callVerb(t, "http_post", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/widgets"), body)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(201), resp.GetAttr("status"))
	assert.Equal(t, "application/json", gotCT)
	assert.Equal(t, "frobnicator", gotBody["name"])
	assert.Equal(t, "blue", gotBody["color"])
}

func TestHTTPPost_StringBody(t *testing.T) {
	var gotCT string
	var gotBody string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotCT = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := callVerb(t, "http_post", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/echo"), cty.StringVal("hello"))
	require.NoError(t, err)
	assert.Equal(t, "text/plain; charset=utf-8", gotCT)
	assert.Equal(t, "hello", gotBody)
}

func TestHTTPPost_BytesBody_ContentTypeWins(t *testing.T) {
	// bytes body's content_type should win over a client-default Content-Type
	// per header precedence level 4.
	var gotCT string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotCT = r.Header.Get("Content-Type")
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    headers = {
        "Content-Type" = "application/json"
    }
}
`, srv.URL))

	body := bytescty.BuildBytesObject([]byte("\x89PNG"), "image/png")
	_, err := callVerb(t, "http_post", bgCtx, clientVal, cty.StringVal("/upload"), body)
	require.NoError(t, err)
	assert.Equal(t, "image/png", gotCT, "bytes content_type should override client default Content-Type")
}

func TestHTTPPost_BytesBody_OptsHeaderStillWins(t *testing.T) {
	// opts.headers Content-Type is level 5, stronger than bytes body level 4.
	var gotCT string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotCT = r.Header.Get("Content-Type")
	}))
	defer srv.Close()

	body := bytescty.BuildBytesObject([]byte("raw"), "image/png")
	opts := cty.ObjectVal(map[string]cty.Value{
		"headers": cty.ObjectVal(map[string]cty.Value{
			"Content-Type": cty.StringVal("application/x-custom"),
		}),
	})
	_, err := callVerb(t, "http_post", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/upload"), body, opts)
	require.NoError(t, err)
	assert.Equal(t, "application/x-custom", gotCT)
}

// ── opts.query ───────────────────────────────────────────────────────────────

func TestHTTPGet_Query(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotURL = r.URL.String()
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"query": cty.ObjectVal(map[string]cty.Value{
			"a": cty.StringVal("1"),
			"b": cty.NumberIntVal(42),
			"c": cty.TupleVal([]cty.Value{cty.StringVal("x"), cty.StringVal("y")}),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/path"), opts)
	require.NoError(t, err)
	// Keys are sorted for deterministic ordering in parseOpts; a=1&b=42&c=x&c=y
	assert.Contains(t, gotURL, "a=1")
	assert.Contains(t, gotURL, "b=42")
	assert.Contains(t, gotURL, "c=x")
	assert.Contains(t, gotURL, "c=y")
}

func TestHTTPGet_Query_PreservesExisting(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotQuery = r.URL.RawQuery
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"query": cty.ObjectVal(map[string]cty.Value{
			"b": cty.StringVal("added"),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/path?a=orig"), opts)
	require.NoError(t, err)
	assert.Contains(t, gotQuery, "a=orig")
	assert.Contains(t, gotQuery, "b=added")
}

// ── opts.as pre-decoding ─────────────────────────────────────────────────────

func TestHTTPGet_AsJSON(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"alice","age":30}`)
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"as": cty.StringVal("json"),
	})
	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL), opts)
	require.NoError(t, err)

	body := resp.GetAttr("body")
	require.False(t, body.IsNull())
	assert.Equal(t, cty.StringVal("alice"), body.GetAttr("name"))
}

func TestHTTPGet_AsString(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		fmt.Fprint(w, "plain text")
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"as": cty.StringVal("string"),
	})
	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL), opts)
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("plain text"), resp.GetAttr("body"))
}

func TestHTTPGet_AsBytes(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n"))
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"as": cty.StringVal("bytes"),
	})
	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL), opts)
	require.NoError(t, err)

	body := resp.GetAttr("body")
	assert.Equal(t, bytescty.BytesObjectType, body.Type())
	assert.Equal(t, cty.StringVal("image/png"), body.GetAttr("content_type"))
	b, err := bytescty.GetBytesFromValue(body)
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG\r\n"), b.Data)
}

func TestHTTPGet_AsJSON_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		fmt.Fprint(w, "not json at all")
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"as": cty.StringVal("json"),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL), opts)
	require.Error(t, err)
}

// ── opts.body_limit ─────────────────────────────────────────────────────────

func TestHTTPGet_BodyLimit(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		fmt.Fprint(w, strings.Repeat("x", 1000))
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"body_limit": cty.NumberIntVal(10),
	})
	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL), opts)
	require.NoError(t, err)

	w, ok := types.GetHTTPClientResponseFromValue(resp)
	require.True(t, ok)
	body, err := w.Get(context.Background(), []cty.Value{cty.StringVal("body")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("xxxxxxxxxx"), body)
}

// ── Redirects ───────────────────────────────────────────────────────────────

func TestHTTPGet_FollowsRedirect(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/start" {
			nethttp.Redirect(w, r, "/end", nethttp.StatusFound)
			return
		}
		if r.URL.Path == "/end" {
			fmt.Fprint(w, "landed")
			return
		}
		_ = srv // silence unused warnings on early close
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/start"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, cty.BoolVal(true), resp.GetAttr("redirected"))
	finalURL := resp.GetAttr("final_url")
	assert.Equal(t, cty.StringVal("/end"), finalURL.GetAttr("path"))
}

func TestHTTPGet_PerCallFollowFalse(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/start" {
			nethttp.Redirect(w, r, "/end", nethttp.StatusFound)
			return
		}
		t.Errorf("should not follow to %s", r.URL.Path)
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"follow_redirects": cty.BoolVal(false),
	})
	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/start"), opts)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(302), resp.GetAttr("status"))
	assert.Equal(t, cty.BoolVal(false), resp.GetAttr("redirected"))
}

func TestHTTPGet_PerCallMaxRedirects(t *testing.T) {
	count := 0
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		count++
		nethttp.Redirect(w, r, fmt.Sprintf("/hop%d", count), nethttp.StatusFound)
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"max_redirects": cty.NumberIntVal(2),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/start"), opts)
	require.Error(t, err, "exceeding max_redirects should be a function error")
	assert.Contains(t, err.Error(), "stopped after 2 redirects")
}

// ── opts.timeout ─────────────────────────────────────────────────────────────

func TestHTTPGet_PerCallTimeout(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "slow")
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"timeout": cty.StringVal("50ms"),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL), opts)
	require.Error(t, err)
}

// ── Transport errors return as function errors ──────────────────────────────

func TestHTTPGet_ConnectionRefused(t *testing.T) {
	// Spin up and immediately close a server so the port is open-then-closed.
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {}))
	addr := srv.URL
	srv.Close()

	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(addr+"/"))
	require.Error(t, err)
}

// ── Other verbs ─────────────────────────────────────────────────────────────

func TestHTTPHead(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "HEAD", r.Method)
		w.Header().Set("X-Probe", "yes")
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_head", bgCtx, nullClientVal, cty.StringVal(srv.URL))
	require.NoError(t, err)
	headers := resp.GetAttr("headers")
	assert.Equal(t,
		cty.ListVal([]cty.Value{cty.StringVal("yes")}),
		headers.Index(cty.StringVal("X-Probe")),
	)
}

func TestHTTPDelete(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(204)
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_delete", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/things/1"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(204), resp.GetAttr("status"))
}

func TestHTTPPut(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "PUT", r.Method)
		data, _ := io.ReadAll(r.Body)
		assert.Equal(t, "new content", string(data))
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := callVerb(t, "http_put", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/a"), cty.StringVal("new content"))
	require.NoError(t, err)
}

func TestHTTPPatch(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "PATCH", r.Method)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := callVerb(t, "http_patch", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/a"), cty.ObjectVal(map[string]cty.Value{
		"field": cty.StringVal("val"),
	}))
	require.NoError(t, err)
}

func TestHTTPOptions(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "OPTIONS", r.Method)
		w.Header().Set("Allow", "GET, POST")
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_options", bgCtx, nullClientVal, cty.StringVal(srv.URL))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
}

// ── http_request (generic) ───────────────────────────────────────────────────

func TestHTTPRequest_Generic(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "PROPFIND", r.Method)
		w.WriteHeader(207)
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_request",
		bgCtx, nullClientVal,
		cty.StringVal("PROPFIND"),
		cty.StringVal(srv.URL+"/dav"),
	)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(207), resp.GetAttr("status"))
}

func TestHTTPRequest_GenericWithBody(t *testing.T) {
	var body string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		assert.Equal(t, "DELETE", r.Method)
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := callVerb(t, "http_request",
		bgCtx, nullClientVal,
		cty.StringVal("DELETE"),
		cty.StringVal(srv.URL+"/items"),
		cty.StringVal("bulk-delete-payload"),
	)
	require.NoError(t, err)
	assert.Equal(t, "bulk-delete-payload", body)
}

// ── Redirect: Authorization stripped cross-origin ───────────────────────────

func TestHTTPGet_CrossOriginRedirect_StripsAuth(t *testing.T) {
	// Target server records whether Authorization was received.
	var targetAuth string
	targetSrv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		targetAuth = r.Header.Get("Authorization")
	}))
	defer targetSrv.Close()

	// Redirecting server points at a different-origin target.
	redirectSrv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.Redirect(w, r, targetSrv.URL+"/landed", nethttp.StatusFound)
	}))
	defer redirectSrv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"headers": cty.ObjectVal(map[string]cty.Value{
			"Authorization": cty.StringVal("Bearer secret"),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(redirectSrv.URL+"/start"), opts)
	require.NoError(t, err)
	assert.Empty(t, targetAuth, "Authorization must be stripped on cross-origin redirect")
}

// ── Retry: end-to-end against httptest ──────────────────────────────────────

// flakyHandler returns a handler that responds with `failStatus` for the
// first `failures` requests and then `200 OK` afterward. The total request
// count is recorded via the returned counter pointer.
func flakyHandler(failures int, failStatus int) (nethttp.Handler, *int32) {
	var count int32
	h := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		n := atomic.AddInt32(&count, 1)
		if n <= int32(failures) {
			w.WriteHeader(failStatus)
			return
		}
		w.WriteHeader(200)
	})
	return h, &count
}

func TestHTTPGet_RetryOn503_Succeeds(t *testing.T) {
	h, count := flakyHandler(2, 503)
	srv := httptest.NewServer(h)
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 3
        initial_delay = "1ms"
        jitter        = false
    }
}
`, srv.URL))

	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, int32(3), atomic.LoadInt32(count), "should hit server 3 times")
}

func TestHTTPGet_RetryExhausted_ReturnsLastResponse(t *testing.T) {
	h, count := flakyHandler(99, 503) // never succeeds
	srv := httptest.NewServer(h)
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 3
        initial_delay = "1ms"
        jitter        = false
    }
}
`, srv.URL))

	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err, "exhausted retries on a status code returns the last response, not an error")
	assert.Equal(t, cty.NumberIntVal(503), resp.GetAttr("status"))
	assert.Equal(t, int32(3), atomic.LoadInt32(count))
}

func TestHTTPPost_NotRetriedByDefault(t *testing.T) {
	h, count := flakyHandler(1, 503)
	srv := httptest.NewServer(h)
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 3
        initial_delay = "1ms"
        jitter        = false
    }
}
`, srv.URL))

	resp, err := callVerb(t, "http_post", bgCtx, clientVal, cty.StringVal("/"), cty.StringVal("data"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(503), resp.GetAttr("status"))
	assert.Equal(t, int32(1), atomic.LoadInt32(count), "POST should not retry without allow_non_idempotent")
}

func TestHTTPPost_RetriedWithAllowNonIdempotent(t *testing.T) {
	h, count := flakyHandler(1, 503)
	srv := httptest.NewServer(h)
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts          = 3
        initial_delay         = "1ms"
        jitter                = false
        allow_non_idempotent  = true
    }
}
`, srv.URL))

	resp, err := callVerb(t, "http_post", bgCtx, clientVal, cty.StringVal("/"), cty.StringVal("data"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, int32(2), atomic.LoadInt32(count))
}

func TestHTTPGet_RetryNotAppliedToNonRetryableStatus(t *testing.T) {
	h, count := flakyHandler(99, 500) // 500 is not in default retry_on
	srv := httptest.NewServer(h)
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 3
        initial_delay = "1ms"
        jitter        = false
    }
}
`, srv.URL))

	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(500), resp.GetAttr("status"))
	assert.Equal(t, int32(1), atomic.LoadInt32(count), "500 should not be retried under default policy")
}

func TestHTTPGet_RetryOnTransportError(t *testing.T) {
	// Stand up a server, immediately close it, and use that URL. The first
	// attempt fails with connection refused; with retry enabled, the
	// function should keep trying and eventually return the transport
	// error after exhausting attempts.
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {}))
	addr := srv.URL
	srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 3
        initial_delay = "1ms"
        jitter        = false
    }
}
`, addr))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.Error(t, err)
}

func TestHTTPGet_RespectRetryAfter_Seconds(t *testing.T) {
	var attempts int32
	var firstAttemptAt, secondAttemptAt time.Time
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		n := atomic.AddInt32(&attempts, 1)
		switch n {
		case 1:
			firstAttemptAt = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(503)
		default:
			secondAttemptAt = time.Now()
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 2
        initial_delay = "1ms"
        jitter        = false
    }
}
`, srv.URL))

	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	gap := secondAttemptAt.Sub(firstAttemptAt)
	assert.GreaterOrEqual(t, gap, 1*time.Second, "Retry-After: 1 should delay >= 1s, got %v", gap)
	assert.Less(t, gap, 3*time.Second, "should not delay much beyond 1s")
}

func TestHTTPGet_OptsRetryFalse_DisablesClientRetry(t *testing.T) {
	h, count := flakyHandler(2, 503)
	srv := httptest.NewServer(h)
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 5
        initial_delay = "1ms"
        jitter        = false
    }
}
`, srv.URL))

	opts := cty.ObjectVal(map[string]cty.Value{
		"retry": cty.BoolVal(false),
	})
	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(503), resp.GetAttr("status"))
	assert.Equal(t, int32(1), atomic.LoadInt32(count), "opts.retry = false should disable client's retry policy")
}

func TestHTTPGet_OptsRetry_OverrideClientPolicy(t *testing.T) {
	h, count := flakyHandler(4, 503)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Client has no retry block (so default = 1 attempt). Per-call retry
	// override should still take effect.
	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
}
`, srv.URL))

	opts := cty.ObjectVal(map[string]cty.Value{
		"retry": cty.ObjectVal(map[string]cty.Value{
			"max_attempts":  cty.NumberIntVal(5),
			"initial_delay": cty.StringVal("1ms"),
			"jitter":        cty.BoolVal(false),
		}),
	})
	resp, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, int32(5), atomic.LoadInt32(count))
}

// ── retry.on_response hook ──────────────────────────────────────────────────

func TestHTTPGet_OnResponse_StopsRetrying(t *testing.T) {
	// Hook returns false on every retry decision → should not retry.
	h, count := flakyHandler(2, 503)
	srv := httptest.NewServer(h)
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 5
        initial_delay = "1ms"
        jitter        = false
        on_response   = false
    }
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	resp, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(503), resp.GetAttr("status"))
	assert.Equal(t, int32(1), atomic.LoadInt32(count), "on_response = false stops after first attempt")
}

func TestHTTPGet_OnResponse_True_ForcesRetry(t *testing.T) {
	// 500 is not in default retry_on, but the hook returns true for it,
	// which should force retries.
	h, count := flakyHandler(2, 500)
	srv := httptest.NewServer(h)
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 5
        initial_delay = "1ms"
        jitter        = false
        on_response   = ctx.response.status == 500
    }
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	resp, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, int32(3), atomic.LoadInt32(count))
}

func TestHTTPGet_OnResponse_HasAttemptVar(t *testing.T) {
	// Hook checks that ctx.attempt is populated correctly. Bail out after
	// the second attempt by returning false on attempt >= 2.
	h, count := flakyHandler(99, 503)
	srv := httptest.NewServer(h)
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    retry {
        max_attempts  = 10
        initial_delay = "1ms"
        jitter        = false
        on_response   = ctx.attempt < 2
    }
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	_, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	// Attempt 1: hook sees attempt=1 → true → retry
	// Attempt 2: hook sees attempt=2 → false → stop
	assert.Equal(t, int32(2), atomic.LoadInt32(count))
}

// ── Authentication ──────────────────────────────────────────────────────────

// authServer returns an httptest server that requires the supplied
// Authorization header to return 200; any other Authorization (or none)
// returns 401. The returned counter records how many requests reached
// the server (including 401s).
func authServer(t *testing.T, expected string) (*httptest.Server, *int32) {
	t.Helper()
	var count int32
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		atomic.AddInt32(&count, 1)
		if r.Header.Get("Authorization") == expected {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(401)
	}))
	return srv, &count
}

func TestHTTPGet_StaticAuthHeader_SentOnFirstRequest(t *testing.T) {
	srv, _ := authServer(t, "Bearer secret")
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer secret"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	resp, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
}

func TestHTTPGet_AuthCachedAcrossCalls(t *testing.T) {
	// The auth expression here is a literal string, but it should still
	// only be evaluated once. We can't directly count evaluations of an
	// HCL string literal, but we can verify the cache returns the same
	// value across multiple calls and the snapshot HasCachedValue stays
	// true.
	srv, count := authServer(t, "Bearer secret")
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer secret"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	for i := 0; i < 3; i++ {
		_, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
		require.NoError(t, err)
	}
	assert.Equal(t, int32(3), atomic.LoadInt32(count))
	c := config.Clients["http"]["api"].(*clientshttp.HTTPClient)
	require.NotNil(t, c.AuthHandler())
	snap := c.AuthHandler().Snapshot()
	assert.True(t, snap.HasCachedValue)
	assert.Equal(t, 0, snap.ConsecutiveFailures)
}

func TestHTTPGet_401_TriggersReAuthAndRetry(t *testing.T) {
	// The auth hook is a literal "Bearer wrong"; the server expects
	// "Bearer good". On the first attempt the server returns 401, the
	// cache is invalidated, the hook is re-evaluated (still "Bearer
	// wrong"), the request is retried *once*, and the second 401 is
	// surfaced as the response. This verifies that the verb hits the
	// server exactly twice — once initial, once re-auth — never three
	// times.
	var requestCount int32
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Header.Get("Authorization") == "Bearer good" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer wrong"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	resp, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(401), resp.GetAttr("status"))
	assert.Equal(t, int32(2), atomic.LoadInt32(&requestCount), "401 should trigger exactly one re-auth retry")
}

func TestHTTPGet_401_NoRetry_WithOptsAuth(t *testing.T) {
	// opts.auth should bypass the hook entirely; a 401 must NOT trigger
	// re-auth-and-retry because there is no cache to invalidate.
	srv, count := authServer(t, "Bearer good")
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer good"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	opts := cty.ObjectVal(map[string]cty.Value{
		"auth": cty.StringVal("Bearer wrong"),
	})
	resp, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(401), resp.GetAttr("status"))
	assert.Equal(t, int32(1), atomic.LoadInt32(count), "opts.auth must not trigger re-auth retry on 401")
}

func TestHTTPGet_OptsAuth_BypassesHook(t *testing.T) {
	// Even when the client has an auth hook configured, opts.auth wins.
	var sawAuth string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		sawAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer client-default"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	opts := cty.ObjectVal(map[string]cty.Value{
		"auth": cty.StringVal("Bearer per-call"),
	})
	_, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Equal(t, "Bearer per-call", sawAuth)
}

func TestHTTPGet_OptsAuthNull_SuppressesHookAndAuthorization(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		sawAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer client-default"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	opts := cty.ObjectVal(map[string]cty.Value{
		"auth": cty.NullVal(cty.String),
	})
	_, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Empty(t, sawAuth, "opts.auth = null must send no Authorization")
}

func TestHTTPGet_OptsHeadersAuthorization_TakesPrecedence(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		sawAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer client-default"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	opts := cty.ObjectVal(map[string]cty.Value{
		"headers": cty.ObjectVal(map[string]cty.Value{
			"Authorization": cty.StringVal("Bearer header-wins"),
		}),
	})
	_, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Equal(t, "Bearer header-wins", sawAuth)
}

func TestHTTPGet_AuthMaxFailures_FailFast(t *testing.T) {
	// Static auth hook with a value the server always rejects. Each
	// verb call sends initial + one re-auth = 2 requests, both
	// recorded as 401 failures against the cache. With
	// auth_max_failures = 2, the first call already trips the failed
	// state — the *second* call from the verb function fails fast
	// before reaching the network.
	srv, count := authServer(t, "Bearer good")
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url          = %q
    auth              = "Bearer wrong"
    auth_max_failures = 2

    auth_retry_backoff {
        initial_delay = "10s"
    }
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	// First call: 401 + re-auth + 401 → 2 wire requests, cache enters
	// failed state.
	resp, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(401), resp.GetAttr("status"))
	assert.Equal(t, int32(2), atomic.LoadInt32(count))

	// Second call: client is now in failed state and should error
	// before hitting the network.
	_, err = callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed state")
	assert.Equal(t, int32(2), atomic.LoadInt32(count), "fail-fast call must not hit the server")
}

func TestHTTPGet_AuthHook_ReentrancySuppressed(t *testing.T) {
	// The auth hook for client "api" makes its own http_get against the
	// SAME client. Without reentrancy suppression that would loop
	// forever (or stack-overflow). With suppression, the inner call
	// sends no Authorization header and the marker prevents re-entering
	// the hook.
	//
	// We verify by serving two paths:
	//   /token  → returns "tok-123" plain text
	//   /data   → requires "Bearer tok-123", else 401
	// The /token path should be reachable from inside the hook with no
	// Authorization header (so it returns 200), and the /data path should
	// then succeed using the value the hook constructed.
	var tokenHits, dataHits, dataAuthSeen int32
	var lastDataAuth atomic.Value // string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		switch r.URL.Path {
		case "/token":
			atomic.AddInt32(&tokenHits, 1)
			// Inside the hook reentrancy must suppress Authorization.
			// If the test sees Authorization here, the marker logic
			// is broken.
			if r.Header.Get("Authorization") != "" {
				w.WriteHeader(500)
				return
			}
			fmt.Fprint(w, "tok-123")
		case "/data":
			atomic.AddInt32(&dataHits, 1)
			lastDataAuth.Store(r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") == "Bearer tok-123" {
				atomic.AddInt32(&dataAuthSeen, 1)
				w.WriteHeader(200)
				return
			}
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()

	vcl := fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    auth     = "Bearer ${tostring(http_get(ctx, client.api, "/token"))}"
}
`, srv.URL)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(newTestLogger(t)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	clientVal := config.CtyClientMap["api"]

	resp, err := callVerbWithConfig(t, config, "http_get", bgCtx, clientVal, cty.StringVal("/data"))
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(200), resp.GetAttr("status"))
	assert.Equal(t, int32(1), atomic.LoadInt32(&tokenHits), "token endpoint should be hit exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&dataHits), "data endpoint should be hit exactly once")
	assert.Equal(t, "Bearer tok-123", lastDataAuth.Load())
}

// ── Cookie jar ──────────────────────────────────────────────────────────────

func TestHTTPGet_NoCookieJar_DoesNotResendSetCookie(t *testing.T) {
	// Without a cookies block the client has no jar; Set-Cookie from the
	// first response should NOT be echoed back on the second request.
	var seenCookie string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/login" {
			nethttp.SetCookie(w, &nethttp.Cookie{Name: "session", Value: "abc123", Path: "/"})
			w.WriteHeader(200)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			seenCookie = c.Value
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/login"))
	require.NoError(t, err)
	_, err = callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/whoami"))
	require.NoError(t, err)
	assert.Empty(t, seenCookie, "no jar means no automatic Set-Cookie persistence")
}

func TestHTTPGet_CookieJar_PersistsAcrossCalls(t *testing.T) {
	// With cookies enabled, a Set-Cookie from /login should be sent
	// back automatically on subsequent matching requests.
	var seenCookie string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/login" {
			nethttp.SetCookie(w, &nethttp.Cookie{Name: "session", Value: "abc123", Path: "/"})
			w.WriteHeader(200)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			seenCookie = c.Value
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    cookies {
        enabled = true
    }
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/login"))
	require.NoError(t, err)
	_, err = callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/whoami"))
	require.NoError(t, err)
	assert.Equal(t, "abc123", seenCookie)
}

func TestHTTPGet_CookiesBlockEmpty_EnablesJar(t *testing.T) {
	// `cookies {}` should default to enabled = true.
	var seenCookie string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/login" {
			nethttp.SetCookie(w, &nethttp.Cookie{Name: "session", Value: "abc", Path: "/"})
			w.WriteHeader(200)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			seenCookie = c.Value
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    cookies {}
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/login"))
	require.NoError(t, err)
	_, err = callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/whoami"))
	require.NoError(t, err)
	assert.Equal(t, "abc", seenCookie)
}

func TestHTTPGet_CookiesEnabledFalse_NoJar(t *testing.T) {
	// `cookies { enabled = false }` should be a no-op, equivalent to
	// omitting the block entirely.
	var seenCookie string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/login" {
			nethttp.SetCookie(w, &nethttp.Cookie{Name: "session", Value: "abc", Path: "/"})
			w.WriteHeader(200)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			seenCookie = c.Value
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    cookies {
        enabled = false
    }
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/login"))
	require.NoError(t, err)
	_, err = callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/whoami"))
	require.NoError(t, err)
	assert.Empty(t, seenCookie)
}

func TestHTTPGet_PerCallCookies_Map(t *testing.T) {
	// opts.cookies as a name → value map should be sent on the
	// request, in addition to the (empty) jar.
	var seenSession, seenTracking string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if c, err := r.Cookie("session"); err == nil {
			seenSession = c.Value
		}
		if c, err := r.Cookie("tracking"); err == nil {
			seenTracking = c.Value
		}
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"cookies": cty.ObjectVal(map[string]cty.Value{
			"session":  cty.StringVal("abc123"),
			"tracking": cty.StringVal("xyz789"),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/"), opts)
	require.NoError(t, err)
	assert.Equal(t, "abc123", seenSession)
	assert.Equal(t, "xyz789", seenTracking)
}

func TestHTTPGet_PerCallCookies_List(t *testing.T) {
	// opts.cookies as a list of cookie objects should also work, with
	// the rich attribute set (path, secure, etc.) parsed.
	var seenSession string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if c, err := r.Cookie("session"); err == nil {
			seenSession = c.Value
		}
	}))
	defer srv.Close()

	opts := cty.ObjectVal(map[string]cty.Value{
		"cookies": cty.TupleVal([]cty.Value{
			cty.ObjectVal(map[string]cty.Value{
				"name":      cty.StringVal("session"),
				"value":     cty.StringVal("abc123"),
				"path":      cty.StringVal("/"),
				"secure":    cty.BoolVal(false),
				"http_only": cty.BoolVal(true),
			}),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/"), opts)
	require.NoError(t, err)
	assert.Equal(t, "abc123", seenSession)
}

func TestHTTPGet_PerCallCookies_DoNotModifyJar(t *testing.T) {
	// Per-call cookies should not enter the jar — a follow-up call
	// with no opts.cookies should not see them.
	var seenOnSecond string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/second" {
			if c, err := r.Cookie("ephemeral"); err == nil {
				seenOnSecond = c.Value
			}
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    cookies {}
}
`, srv.URL))

	opts := cty.ObjectVal(map[string]cty.Value{
		"cookies": cty.ObjectVal(map[string]cty.Value{
			"ephemeral": cty.StringVal("once"),
		}),
	})
	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/first"), opts)
	require.NoError(t, err)
	_, err = callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/second"))
	require.NoError(t, err)
	assert.Empty(t, seenOnSecond, "per-call cookies must not enter the jar")
}

func TestHTTPGet_PerCallCookies_AddedAlongsideJarCookies(t *testing.T) {
	// When the jar already holds a cookie from a prior response and
	// the call also supplies opts.cookies, both should be sent.
	var seenSession, seenExtra string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/login" {
			nethttp.SetCookie(w, &nethttp.Cookie{Name: "session", Value: "fromjar", Path: "/"})
			w.WriteHeader(200)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			seenSession = c.Value
		}
		if c, err := r.Cookie("extra"); err == nil {
			seenExtra = c.Value
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    cookies {}
}
`, srv.URL))

	_, err := callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/login"))
	require.NoError(t, err)

	opts := cty.ObjectVal(map[string]cty.Value{
		"cookies": cty.ObjectVal(map[string]cty.Value{
			"extra": cty.StringVal("frompercall"),
		}),
	})
	_, err = callVerb(t, "http_get", bgCtx, clientVal, cty.StringVal("/data"), opts)
	require.NoError(t, err)
	assert.Equal(t, "fromjar", seenSession)
	assert.Equal(t, "frompercall", seenExtra)
}

// ── OpenTelemetry: propagation ──────────────────────────────────────────────

// withGlobalTracerProvider installs an in-memory tracer + W3C
// propagator for the duration of the test, then restores them.
func withGlobalTracerProvider(t *testing.T) (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	t.Cleanup(func() {
		tp.Shutdown(context.Background()) //nolint:errcheck
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevPropagator)
	})
	return exporter, tp
}

// ctxWithSpan starts a real span and returns a ctx cty value wrapping
// the resulting Go context. The span is ended by the test cleanup. The
// returned span context is what the conditional propagator should
// serialize into the outbound traceparent header.
func ctxWithSpan(t *testing.T, tp trace.TracerProvider) (cty.Value, trace.SpanContext) {
	t.Helper()
	tracer := tp.Tracer("test")
	goCtx, span := tracer.Start(context.Background(), "test-parent")
	t.Cleanup(func() { span.End() })
	return ctxVal(goCtx), span.SpanContext()
}

func TestHTTPGet_OTelPropagation_DefaultOn(t *testing.T) {
	_, tp := withGlobalTracerProvider(t)

	var sawTraceparent string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		sawTraceparent = r.Header.Get("traceparent")
	}))
	defer srv.Close()

	ctx, sc := ctxWithSpan(t, tp)
	_, err := callVerb(t, "http_get", ctx, nullClientVal, cty.StringVal(srv.URL+"/"))
	require.NoError(t, err)
	assert.NotEmpty(t, sawTraceparent, "default-on propagation should inject traceparent")
	assert.Contains(t, sawTraceparent, sc.TraceID().String(),
		"injected traceparent should carry the parent span's trace ID")
}

func TestHTTPGet_OTelPropagation_PerClientOff(t *testing.T) {
	_, tp := withGlobalTracerProvider(t)

	var sawTraceparent string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		sawTraceparent = r.Header.Get("traceparent")
	}))
	defer srv.Close()

	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    otel {
        propagate = false
    }
}
`, srv.URL))

	ctx, _ := ctxWithSpan(t, tp)
	_, err := callVerb(t, "http_get", ctx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)
	assert.Empty(t, sawTraceparent, "per-client propagate=false should suppress traceparent")
}

func TestHTTPGet_OTelPropagation_PerCallOff(t *testing.T) {
	_, tp := withGlobalTracerProvider(t)

	var sawTraceparent string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		sawTraceparent = r.Header.Get("traceparent")
	}))
	defer srv.Close()

	// Client has default-on propagation; per-call override should win.
	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
}
`, srv.URL))

	ctx, _ := ctxWithSpan(t, tp)
	opts := cty.ObjectVal(map[string]cty.Value{
		"otel": cty.ObjectVal(map[string]cty.Value{
			"propagate": cty.BoolVal(false),
		}),
	})
	_, err := callVerb(t, "http_get", ctx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.Empty(t, sawTraceparent, "per-call opts.otel.propagate=false should suppress traceparent")
}

func TestHTTPGet_OTelPropagation_PerCallOn_OverridesClientOff(t *testing.T) {
	_, tp := withGlobalTracerProvider(t)

	var sawTraceparent string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		sawTraceparent = r.Header.Get("traceparent")
	}))
	defer srv.Close()

	// Client default OFF, per-call ON should re-enable.
	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
    otel {
        propagate = false
    }
}
`, srv.URL))

	ctx, _ := ctxWithSpan(t, tp)
	opts := cty.ObjectVal(map[string]cty.Value{
		"otel": cty.ObjectVal(map[string]cty.Value{
			"propagate": cty.BoolVal(true),
		}),
	})
	_, err := callVerb(t, "http_get", ctx, clientVal, cty.StringVal("/"), opts)
	require.NoError(t, err)
	assert.NotEmpty(t, sawTraceparent, "per-call propagate=true should override client default off")
}

// ── OpenTelemetry: span recording ──────────────────────────────────────────

func TestHTTPGet_OTelSpan_Recorded(t *testing.T) {
	exporter, tp := withGlobalTracerProvider(t)

	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Configure the client to use our test tracer provider explicitly.
	// (otelhttp falls back to the global if WithTracerProvider is not
	// set, but being explicit makes the test independent of global
	// state for the recorder.)
	_ = tp
	clientVal := loadClient(t, fmt.Sprintf(`
client "http" "api" {
    base_url = %q
}
`, srv.URL))

	// Force a parent span so otelhttp creates a child.
	ctx, _ := ctxWithSpan(t, tp)
	_, err := callVerb(t, "http_get", ctx, clientVal, cty.StringVal("/"))
	require.NoError(t, err)

	// Flush by ending parent span via cleanup, then read.
	// otelhttp's child span ends synchronously when the response body
	// is closed, so it should be in the recorder already.
	spans := exporter.GetSpans()
	require.NotEmpty(t, spans, "otelhttp should have recorded an HTTP client span")

	// Find the HTTP span — it has a method attribute matching "GET".
	var found bool
	for _, s := range spans {
		for _, attr := range s.Attributes {
			if attr.Key == "http.request.method" && attr.Value.AsString() == "GET" {
				found = true
				break
			}
		}
	}
	assert.True(t, found, "expected a span with http.request.method=GET")
}

// ── http_must ───────────────────────────────────────────────────────────────

// fetchOnce calls http_get against srv and returns the response cty value.
// Helper for the http_must tests so each one isn't fifteen lines of setup.
func fetchOnce(t *testing.T, srv *httptest.Server, path string) cty.Value {
	t.Helper()
	resp, err := callVerb(t, "http_get", bgCtx, nullClientVal, cty.StringVal(srv.URL+path))
	require.NoError(t, err)
	return resp
}

// callMust invokes http_must directly via the function registry.
func callMust(t *testing.T, args ...cty.Value) (cty.Value, error) {
	t.Helper()
	return callVerb(t, "http_must", args...)
}

func TestHTTPMust_Default2xx_PassesThrough(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(204)
		fmt.Fprint(w, "")
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	got, err := callMust(t, resp)
	require.NoError(t, err)
	// http_must returns the response unchanged.
	assert.Equal(t, cty.NumberIntVal(204), got.GetAttr("status"))
}

func TestHTTPMust_Default2xx_RejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, "not found")
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), "any 2xx")
	assert.Contains(t, err.Error(), "not found", "body excerpt should be in the error")
}

func TestHTTPMust_SingleStatus_Match(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(201)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.NumberIntVal(201))
	require.NoError(t, err)
}

func TestHTTPMust_SingleStatus_Mismatch(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.NumberIntVal(201))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[201]")
	assert.Contains(t, err.Error(), "200")
}

func TestHTTPMust_StatusList_Match(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.TupleVal([]cty.Value{
		cty.NumberIntVal(200),
		cty.NumberIntVal(204),
		cty.NumberIntVal(404),
	}))
	require.NoError(t, err)
}

func TestHTTPMust_StatusList_Mismatch(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.TupleVal([]cty.Value{
		cty.NumberIntVal(200),
		cty.NumberIntVal(204),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[200, 204]")
}

func TestHTTPMust_Range_Match(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(207)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.TupleVal([]cty.Value{
		cty.TupleVal([]cty.Value{cty.NumberIntVal(200), cty.NumberIntVal(299)}),
	}))
	require.NoError(t, err)
}

func TestHTTPMust_Range_BoundariesInclusive(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		// Hit the lower bound exactly.
		w.WriteHeader(200)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.TupleVal([]cty.Value{
		cty.TupleVal([]cty.Value{cty.NumberIntVal(200), cty.NumberIntVal(299)}),
	}))
	require.NoError(t, err)
}

func TestHTTPMust_MixedNumbersAndRanges(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	// Accept any 2xx OR a literal 404.
	_, err := callMust(t, resp,
		cty.TupleVal([]cty.Value{
			cty.NumberIntVal(404),
			cty.TupleVal([]cty.Value{cty.NumberIntVal(200), cty.NumberIntVal(299)}),
		}),
	)
	require.NoError(t, err)
}

func TestHTTPMust_Range_InvalidLoGreaterThanHi(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.TupleVal([]cty.Value{
		cty.TupleVal([]cty.Value{cty.NumberIntVal(500), cty.NumberIntVal(400)}),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lo")
}

func TestHTTPMust_ErrorMessage_IncludesMethodAndURL(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(422)
		fmt.Fprint(w, `{"error":"validation failed"}`)
	}))
	defer srv.Close()

	resp, err := callVerb(t, "http_post", bgCtx, nullClientVal, cty.StringVal(srv.URL+"/widgets"), cty.StringVal("payload"))
	require.NoError(t, err)
	_, err = callMust(t, resp, cty.NumberIntVal(201))
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "POST")
	assert.Contains(t, msg, "/widgets")
	assert.Contains(t, msg, "422")
	assert.Contains(t, msg, "Unprocessable Entity")
	assert.Contains(t, msg, "[201]")
	assert.Contains(t, msg, `validation failed`)
}

func TestHTTPMust_BodyExcerpt_TruncatedAt512Bytes(t *testing.T) {
	// Build a 1KB body of a distinctive character (one that won't
	// appear in the surrounding error template) and verify exactly
	// 512 bytes appear in the error, terminated by "...".
	bigBody := strings.Repeat("Q", 1024)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, bigBody)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "...")
	qCount := strings.Count(msg, "Q")
	assert.Equal(t, 512, qCount, "exactly 512 bytes of body should appear in the error")
}

func TestHTTPMust_BinaryBody_Summarized(t *testing.T) {
	// PNG content type with binary bytes — should produce a count
	// summary, not the raw bytes.
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(500)
		w.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00})
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "image/png")
	assert.Contains(t, msg, "10 bytes")
}

func TestHTTPMust_BinaryBody_NoContentType_DefaultsToText(t *testing.T) {
	// No Content-Type at all — the heuristic treats this as text and
	// includes the body inline.
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "boom")
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestHTTPMust_NotAResponse(t *testing.T) {
	// Passing something that isn't an httpclientresponse should fail.
	_, err := callMust(t, cty.StringVal("not a response"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "httpclientresponse")
}

func TestHTTPMust_NullExpected_DefaultsTo2xx(t *testing.T) {
	// Explicit null is the same as omitting expected.
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.NullVal(cty.DynamicPseudoType))
	require.NoError(t, err)
}

func TestHTTPMust_EmptyList_IsAnError(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	resp := fetchOnce(t, srv, "/")
	_, err := callMust(t, resp, cty.EmptyTupleVal)
	require.Error(t, err)
}

// ── Interface sanity ────────────────────────────────────────────────────────

func TestHTTPCallable_NullClientPath(t *testing.T) {
	// Exercise the code path where GetHTTPCallableFromValue is called with
	// a null cty value — must return (nil, nil) so verb functions know to
	// substitute NullClient().
	c, err := clientshttp.GetHTTPCallableFromValue(cty.NullVal(cty.DynamicPseudoType))
	require.NoError(t, err)
	assert.Nil(t, c)
}
