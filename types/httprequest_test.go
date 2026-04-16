package types

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	timecty "github.com/tsarna/time-cty-funcs"
	urlcty "github.com/tsarna/url-cty-funcs"
	"github.com/zclconf/go-cty/cty"
)

// newTestRequest is a helper that builds an *http.Request for testing.
func newTestRequest(t *testing.T, method, rawURL, body string) *http.Request {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, rawURL, bodyReader)
	require.NoError(t, err)
	return r
}

// --- BuildHTTPRequestObject tests ---

func TestBuildHTTPRequestObject_BasicFields(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/path", "")
	r.Proto = "HTTP/1.1"
	r.ProtoMajor = 1
	r.ProtoMinor = 1
	r.Host = "example.com"
	r.RemoteAddr = "127.0.0.1:12345"

	obj := BuildHTTPRequestObject(r)

	assert.Equal(t, cty.StringVal("GET"), obj.GetAttr("method"))
	assert.Equal(t, cty.StringVal("HTTP/1.1"), obj.GetAttr("proto"))
	assert.Equal(t, cty.NumberIntVal(1), obj.GetAttr("proto_major"))
	assert.Equal(t, cty.NumberIntVal(1), obj.GetAttr("proto_minor"))
	assert.Equal(t, cty.StringVal("example.com"), obj.GetAttr("host"))
	assert.Equal(t, cty.StringVal("127.0.0.1:12345"), obj.GetAttr("remote_addr"))
}

func TestBuildHTTPRequestObject_URL(t *testing.T) {
	r := newTestRequest(t, "GET", "https://api.example.com:8443/v1/users?page=2", "")
	obj := BuildHTTPRequestObject(r)
	urlObj := obj.GetAttr("url")

	assert.Equal(t, urlcty.URLObjectType, urlObj.Type())
	assert.Equal(t, cty.StringVal("https"), urlObj.GetAttr("scheme"))
	assert.Equal(t, cty.StringVal("api.example.com"), urlObj.GetAttr("hostname"))
	assert.Equal(t, cty.StringVal("8443"), urlObj.GetAttr("port"))
	assert.Equal(t, cty.StringVal("/v1/users"), urlObj.GetAttr("path"))
}

func TestBuildHTTPRequestObject_Headers(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Add("Accept", "text/html")
	r.Header.Add("Accept", "application/json")

	obj := BuildHTTPRequestObject(r)
	headers := obj.GetAttr("headers")

	assert.True(t, headers.Type().IsMapType())

	ctList := headers.Index(cty.StringVal("Content-Type"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("application/json")}), ctList)

	acceptList := headers.Index(cty.StringVal("Accept"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("text/html"), cty.StringVal("application/json")}), acceptList)
}

func TestBuildHTTPRequestObject_EmptyHeaders(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	// Clear any default headers
	r.Header = http.Header{}
	obj := BuildHTTPRequestObject(r)
	headers := obj.GetAttr("headers")
	assert.True(t, headers.RawEquals(cty.MapValEmpty(cty.List(cty.String))))
}

func TestBuildHTTPRequestObject_BasicAuth(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	r.SetBasicAuth("alice", "s3cret")

	obj := BuildHTTPRequestObject(r)
	assert.Equal(t, cty.StringVal("alice"), obj.GetAttr("user"))
	assert.Equal(t, cty.StringVal("s3cret"), obj.GetAttr("password"))
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("password_set"))
}

func TestBuildHTTPRequestObject_NoBasicAuth(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	obj := BuildHTTPRequestObject(r)
	assert.Equal(t, cty.StringVal(""), obj.GetAttr("user"))
	assert.Equal(t, cty.StringVal(""), obj.GetAttr("password"))
	assert.Equal(t, cty.BoolVal(false), obj.GetAttr("password_set"))
}

func TestBuildHTTPRequestObject_HasCapsule(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	obj := BuildHTTPRequestObject(r)
	cap := obj.GetAttr("_capsule")
	assert.Equal(t, HTTPRequestCapsuleType, cap.Type())
}

// --- Get tests ---

func TestHTTPRequestWrapper_Get_Body(t *testing.T) {
	r := newTestRequest(t, "POST", "http://example.com/", "hello world")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("body")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("hello world"), result)
}

func TestHTTPRequestWrapper_Get_BodyBytes(t *testing.T) {
	r := newTestRequest(t, "POST", "http://example.com/", "binary data")
	r.Header.Set("Content-Type", "application/octet-stream; charset=utf-8")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("body_bytes")})
	require.NoError(t, err)
	assert.Equal(t, BytesObjectType, result.Type())
	// charset param should be stripped
	assert.Equal(t, cty.StringVal("application/octet-stream"), result.GetAttr("content_type"))
}

func TestHTTPRequestWrapper_Get_BodyBytes_NoContentType(t *testing.T) {
	r := newTestRequest(t, "POST", "http://example.com/", "data")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("body_bytes")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal(""), result.GetAttr("content_type"))
}

func TestHTTPRequestWrapper_Get_BodyJSON(t *testing.T) {
	body := `{"name":"alice","age":30}`
	r := newTestRequest(t, "POST", "http://example.com/", body)
	r.Header.Set("Content-Type", "application/json")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("body_json")})
	require.NoError(t, err)
	assert.True(t, result.Type().IsObjectType())
	assert.Equal(t, cty.StringVal("alice"), result.GetAttr("name"))
	assert.True(t, result.GetAttr("age").AsBigFloat() != nil)
}

func TestHTTPRequestWrapper_Get_BodyJSON_InvalidJSON(t *testing.T) {
	r := newTestRequest(t, "POST", "http://example.com/", "not json")
	w := &HTTPRequestWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("body_json")})
	assert.Error(t, err)
}

func TestHTTPRequestWrapper_Get_Header(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	r.Header.Set("Authorization", "Bearer token123")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header"), cty.StringVal("Authorization")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("Bearer token123"), result)
}

func TestHTTPRequestWrapper_Get_Header_Absent(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header"), cty.StringVal("X-Missing")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal(""), result)
}

func TestHTTPRequestWrapper_Get_HeaderAll(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	r.Header.Add("Accept", "text/html")
	r.Header.Add("Accept", "application/json")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header_all"), cty.StringVal("Accept")})
	require.NoError(t, err)
	assert.Equal(t, cty.ListVal([]cty.Value{
		cty.StringVal("text/html"),
		cty.StringVal("application/json"),
	}), result)
}

func TestHTTPRequestWrapper_Get_HeaderAll_Absent(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header_all"), cty.StringVal("X-Missing")})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.ListValEmpty(cty.String)))
}

func TestHTTPRequestWrapper_Get_Cookie(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	r.AddCookie(&http.Cookie{Name: "session", Value: "abc123"})
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("cookie"), cty.StringVal("session")})
	require.NoError(t, err)
	assert.Equal(t, CookieObjectType, result.Type())
	assert.Equal(t, cty.StringVal("session"), result.GetAttr("name"))
	assert.Equal(t, cty.StringVal("abc123"), result.GetAttr("value"))
}

func TestHTTPRequestWrapper_Get_Cookie_NotFound(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	w := &HTTPRequestWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("cookie"), cty.StringVal("missing")})
	assert.Error(t, err)
}

func TestHTTPRequestWrapper_Get_PathValue(t *testing.T) {
	// PathValue requires a request processed by a ServeMux pattern match.
	// We test via an httptest server with a registered handler.
	var captured *http.Request

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(_ http.ResponseWriter, r *http.Request) {
		captured = r
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/items/42")
	require.NoError(t, err)
	resp.Body.Close()
	require.NotNil(t, captured)

	w := &HTTPRequestWrapper{R: captured}
	result, err := w.Get(bg, []cty.Value{cty.StringVal("path_value"), cty.StringVal("id")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("42"), result)
}

func TestHTTPRequestWrapper_Get_FormValue(t *testing.T) {
	r := newTestRequest(t, "POST", "http://example.com/", "name=alice&age=30")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := &HTTPRequestWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("form_value"), cty.StringVal("name")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("alice"), result)
}

func TestHTTPRequestWrapper_Get_PostFormValue(t *testing.T) {
	r := newTestRequest(t, "POST", "http://example.com/?name=query", "name=body")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := &HTTPRequestWrapper{R: r}

	// post_form_value returns body value only, not query param
	result, err := w.Get(bg, []cty.Value{cty.StringVal("post_form_value"), cty.StringVal("name")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("body"), result)
}

func TestHTTPRequestWrapper_Get_UnknownField(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	w := &HTTPRequestWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("bogus")})
	assert.Error(t, err)
}

func TestHTTPRequestWrapper_Get_NoArgs(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	w := &HTTPRequestWrapper{R: r}

	_, err := w.Get(bg, nil)
	assert.Error(t, err)
}

func TestHTTPRequestWrapper_Get_MissingKeyArg(t *testing.T) {
	r := newTestRequest(t, "GET", "http://example.com/", "")
	w := &HTTPRequestWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("header")})
	assert.Error(t, err)
}

// --- convertCookieObject tests ---

func TestConvertCookieObject_Fields(t *testing.T) {
	expires := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	cookie := &http.Cookie{
		Name:        "sid",
		Value:       "xyz",
		Quoted:      true,
		Path:        "/app",
		Domain:      "example.com",
		Expires:     expires,
		RawExpires:  "Wed, 15 Jan 2030 12:00:00 GMT",
		MaxAge:      3600,
		Secure:      true,
		HttpOnly:    true,
		SameSite:    http.SameSiteStrictMode,
		Partitioned: true,
		Raw:         "sid=xyz; Path=/app",
	}

	obj := convertCookieObject(cookie)
	assert.Equal(t, CookieObjectType, obj.Type())
	assert.Equal(t, cty.StringVal("sid"), obj.GetAttr("name"))
	assert.Equal(t, cty.StringVal("xyz"), obj.GetAttr("value"))
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("quoted"))
	assert.Equal(t, cty.StringVal("/app"), obj.GetAttr("path"))
	assert.Equal(t, cty.StringVal("example.com"), obj.GetAttr("domain"))
	assert.Equal(t, timecty.NewTimeCapsule(expires), obj.GetAttr("expires"))
	assert.Equal(t, cty.StringVal("Wed, 15 Jan 2030 12:00:00 GMT"), obj.GetAttr("raw_expires"))
	assert.Equal(t, cty.NumberIntVal(3600), obj.GetAttr("max_age"))
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("secure"))
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("http_only"))
	assert.Equal(t, cty.StringVal("Strict"), obj.GetAttr("same_site"))
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("partitioned"))
	assert.Equal(t, cty.StringVal("sid=xyz; Path=/app"), obj.GetAttr("raw"))
}

func TestConvertCookieObject_ZeroExpires(t *testing.T) {
	obj := convertCookieObject(&http.Cookie{Name: "x", Value: "y"})
	assert.Equal(t, cty.NullVal(timecty.TimeCapsuleType), obj.GetAttr("expires"))
}

func TestConvertCookieObject_SameSiteVariants(t *testing.T) {
	cases := []struct {
		mode     http.SameSite
		expected string
	}{
		{http.SameSiteDefaultMode, "Default"},
		{http.SameSiteLaxMode, "Lax"},
		{http.SameSiteStrictMode, "Strict"},
		{http.SameSiteNoneMode, "None"},
		{0, "Default"}, // zero value
	}
	for _, tc := range cases {
		obj := convertCookieObject(&http.Cookie{Name: "x", Value: "y", SameSite: tc.mode})
		assert.Equal(t, cty.StringVal(tc.expected), obj.GetAttr("same_site"), "SameSite mode %v", tc.mode)
	}
}
