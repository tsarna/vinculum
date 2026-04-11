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
	clientshttp "github.com/tsarna/vinculum/clients/http"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/ctyutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
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
	return ctyutil.NewContextCapsule(ctx)
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
	fns := GetHTTPClientFunctions()
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

	body := types.BuildBytesObject([]byte("\x89PNG"), "image/png")
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

	body := types.BuildBytesObject([]byte("raw"), "image/png")
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
	assert.Equal(t, types.BytesObjectType, body.Type())
	assert.Equal(t, cty.StringVal("image/png"), body.GetAttr("content_type"))
	b, err := types.GetBytesFromValue(body)
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

// ── Interface sanity ────────────────────────────────────────────────────────

func TestHTTPCallable_NullClientPath(t *testing.T) {
	// Exercise the code path where GetHTTPCallableFromValue is called with
	// a null cty value — must return (nil, nil) so verb functions know to
	// substitute NullClient().
	c, err := clientshttp.GetHTTPCallableFromValue(cty.NullVal(cty.DynamicPseudoType))
	require.NoError(t, err)
	assert.Nil(t, c)
}
