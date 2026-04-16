package types

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	richcty "github.com/tsarna/rich-cty-types"
	urlcty "github.com/tsarna/url-cty-funcs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// newTestResponse builds an *http.Response suitable for wrapping in a
// *HTTPClientResponseWrapper for tests. The returned response's Request field
// is populated so that final_url resolves.
func newTestResponse(statusCode int, status, body string) *http.Response {
	reqURL, _ := url.Parse("https://api.example.com/v1/things/42")
	return &http.Response{
		StatusCode:    statusCode,
		Status:        status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request: &http.Request{
			Method: "GET",
			URL:    reqURL,
		},
	}
}

// --- BuildHTTPClientResponseObject tests ---

func TestBuildHTTPClientResponseObject_BasicFields(t *testing.T) {
	r := newTestResponse(200, "200 OK", "hello")
	r.Header.Set("Content-Type", "text/plain; charset=utf-8")
	w := &HTTPClientResponseWrapper{R: r}

	obj := BuildHTTPClientResponseObject(w)

	assert.Equal(t, cty.NumberIntVal(200), obj.GetAttr("status"))
	assert.Equal(t, cty.StringVal("200 OK"), obj.GetAttr("status_text"))
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("ok"))
	assert.Equal(t, cty.BoolVal(false), obj.GetAttr("redirected"))
	assert.Equal(t, cty.StringVal("HTTP/1.1"), obj.GetAttr("proto"))
	assert.Equal(t, cty.NumberIntVal(5), obj.GetAttr("content_length"))
	// mime params stripped
	assert.Equal(t, cty.StringVal("text/plain"), obj.GetAttr("content_type"))
	assert.True(t, obj.GetAttr("body").IsNull())
	assert.Equal(t, HTTPClientResponseCapsuleType, obj.GetAttr("_capsule").Type())
}

func TestBuildHTTPClientResponseObject_OkFalse(t *testing.T) {
	cases := []struct {
		status int
		ok     bool
	}{
		{100, false},
		{199, false},
		{200, true},
		{204, true},
		{299, true},
		{300, false},
		{404, false},
		{500, false},
	}
	for _, tc := range cases {
		r := newTestResponse(tc.status, http.StatusText(tc.status), "")
		obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
		assert.Equal(t, cty.BoolVal(tc.ok), obj.GetAttr("ok"), "status %d", tc.status)
	}
}

func TestBuildHTTPClientResponseObject_Redirected(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r, Redirected: true}
	obj := BuildHTTPClientResponseObject(w)
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("redirected"))
}

func TestBuildHTTPClientResponseObject_FinalURL(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
	finalURL := obj.GetAttr("final_url")
	assert.Equal(t, urlcty.URLObjectType, finalURL.Type())
	assert.Equal(t, cty.StringVal("https"), finalURL.GetAttr("scheme"))
	assert.Equal(t, cty.StringVal("api.example.com"), finalURL.GetAttr("hostname"))
	assert.Equal(t, cty.StringVal("/v1/things/42"), finalURL.GetAttr("path"))
}

func TestBuildHTTPClientResponseObject_FinalURL_NilRequest(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Request = nil
	obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
	assert.True(t, obj.GetAttr("final_url").IsNull())
}

func TestBuildHTTPClientResponseObject_Headers(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Add("X-Multi", "a")
	r.Header.Add("X-Multi", "b")

	obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
	headers := obj.GetAttr("headers")

	assert.True(t, headers.Type().IsMapType())
	assert.Equal(t,
		cty.ListVal([]cty.Value{cty.StringVal("application/json")}),
		headers.Index(cty.StringVal("Content-Type")),
	)
	assert.Equal(t,
		cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
		headers.Index(cty.StringVal("X-Multi")),
	)
}

func TestBuildHTTPClientResponseObject_EmptyHeaders(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
	headers := obj.GetAttr("headers")
	assert.True(t, headers.RawEquals(cty.MapValEmpty(cty.List(cty.String))))
}

func TestBuildHTTPClientResponseObject_ContentType_NoParams(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Header.Set("Content-Type", "application/json")
	obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
	assert.Equal(t, cty.StringVal("application/json"), obj.GetAttr("content_type"))
}

func TestBuildHTTPClientResponseObject_ContentType_Missing(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
	assert.Equal(t, cty.StringVal(""), obj.GetAttr("content_type"))
}

func TestBuildHTTPClientResponseObject_ContentLength_Unknown(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.ContentLength = -1
	obj := BuildHTTPClientResponseObject(&HTTPClientResponseWrapper{R: r})
	assert.Equal(t, cty.NumberIntVal(-1), obj.GetAttr("content_length"))
}

func TestBuildHTTPClientResponseObject_PreDecodedBody(t *testing.T) {
	r := newTestResponse(200, "200 OK", `{"ok":true}`)
	w := &HTTPClientResponseWrapper{
		R:              r,
		PreDecodedBody: cty.ObjectVal(map[string]cty.Value{"ok": cty.True}),
	}
	obj := BuildHTTPClientResponseObject(w)
	body := obj.GetAttr("body")
	assert.False(t, body.IsNull())
	assert.Equal(t, cty.True, body.GetAttr("ok"))
}

// --- GetHTTPClientResponseFromValue ---

func TestGetHTTPClientResponseFromValue_FromObject(t *testing.T) {
	r := newTestResponse(200, "200 OK", "hi")
	w := &HTTPClientResponseWrapper{R: r}
	obj := BuildHTTPClientResponseObject(w)

	got, ok := GetHTTPClientResponseFromValue(obj)
	require.True(t, ok)
	assert.Same(t, w, got)
}

func TestGetHTTPClientResponseFromValue_FromCapsule(t *testing.T) {
	r := newTestResponse(200, "200 OK", "hi")
	w := &HTTPClientResponseWrapper{R: r}
	cap := NewHTTPClientResponseCapsule(w)

	got, ok := GetHTTPClientResponseFromValue(cap)
	require.True(t, ok)
	assert.Same(t, w, got)
}

func TestGetHTTPClientResponseFromValue_WrongType(t *testing.T) {
	_, ok := GetHTTPClientResponseFromValue(cty.StringVal("not a response"))
	assert.False(t, ok)
}

// --- Get (richcty.Gettable) ---

func TestHTTPClientResponse_Get_Body(t *testing.T) {
	r := newTestResponse(200, "200 OK", "hello world")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("body")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("hello world"), result)
}

func TestHTTPClientResponse_Get_BodyBytes(t *testing.T) {
	r := newTestResponse(200, "200 OK", "binary")
	r.Header.Set("Content-Type", "application/octet-stream; boundary=abc")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("body_bytes")})
	require.NoError(t, err)
	assert.Equal(t, BytesObjectType, result.Type())
	// mime params stripped
	assert.Equal(t, cty.StringVal("application/octet-stream"), result.GetAttr("content_type"))
	// Data should round-trip through the capsule
	b, err := GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte("binary"), b.Data)
}

func TestHTTPClientResponse_Get_BodyJSON(t *testing.T) {
	r := newTestResponse(200, "200 OK", `{"name":"alice","age":30}`)
	r.Header.Set("Content-Type", "application/json")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("body_json")})
	require.NoError(t, err)
	assert.True(t, result.Type().IsObjectType())
	assert.Equal(t, cty.StringVal("alice"), result.GetAttr("name"))
}

func TestHTTPClientResponse_Get_BodyJSON_Invalid(t *testing.T) {
	r := newTestResponse(200, "200 OK", "not json")
	w := &HTTPClientResponseWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("body_json")})
	assert.Error(t, err)
}

func TestHTTPClientResponse_Get_Body_Repeatable(t *testing.T) {
	// After the first read the body is buffered; subsequent reads should
	// return the same content, not an empty stream.
	r := newTestResponse(200, "200 OK", "payload")
	w := &HTTPClientResponseWrapper{R: r}

	first, err := w.Get(bg, []cty.Value{cty.StringVal("body")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("payload"), first)

	second, err := w.Get(bg, []cty.Value{cty.StringVal("body")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("payload"), second)

	// And body_bytes should also re-read from buffer
	bb, err := w.Get(bg, []cty.Value{cty.StringVal("body_bytes")})
	require.NoError(t, err)
	b, err := GetBytesFromValue(bb)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), b.Data)
}

func TestHTTPClientResponse_Get_Header(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Header.Set("X-Request-Id", "abc123")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header"), cty.StringVal("X-Request-Id")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("abc123"), result)
}

func TestHTTPClientResponse_Get_Header_CaseInsensitive(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Header.Set("X-Request-Id", "abc123")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header"), cty.StringVal("x-request-id")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("abc123"), result)
}

func TestHTTPClientResponse_Get_Header_Absent(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header"), cty.StringVal("X-Missing")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal(""), result)
}

func TestHTTPClientResponse_Get_HeaderAll(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Header.Add("X-Multi", "a")
	r.Header.Add("X-Multi", "b")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header_all"), cty.StringVal("X-Multi")})
	require.NoError(t, err)
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}), result)
}

func TestHTTPClientResponse_Get_HeaderAll_Absent(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("header_all"), cty.StringVal("X-Missing")})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.ListValEmpty(cty.String)))
}

func TestHTTPClientResponse_Get_Cookie(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Header.Add("Set-Cookie", "session=abc123; Path=/")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("cookie"), cty.StringVal("session")})
	require.NoError(t, err)
	assert.Equal(t, CookieObjectType, result.Type())
	assert.Equal(t, cty.StringVal("session"), result.GetAttr("name"))
	assert.Equal(t, cty.StringVal("abc123"), result.GetAttr("value"))
}

func TestHTTPClientResponse_Get_Cookie_NotFound(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("cookie"), cty.StringVal("missing")})
	assert.Error(t, err)
}

func TestHTTPClientResponse_Get_Cookies(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.Header.Add("Set-Cookie", "a=1")
	r.Header.Add("Set-Cookie", "b=2")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("cookies")})
	require.NoError(t, err)
	require.True(t, result.Type().IsListType())
	cookies := result.AsValueSlice()
	require.Len(t, cookies, 2)
	assert.Equal(t, cty.StringVal("a"), cookies[0].GetAttr("name"))
	assert.Equal(t, cty.StringVal("1"), cookies[0].GetAttr("value"))
	assert.Equal(t, cty.StringVal("b"), cookies[1].GetAttr("name"))
	assert.Equal(t, cty.StringVal("2"), cookies[1].GetAttr("value"))
}

func TestHTTPClientResponse_Get_Cookies_Empty(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r}

	result, err := w.Get(bg, []cty.Value{cty.StringVal("cookies")})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.ListValEmpty(CookieObjectType)))
}

func TestHTTPClientResponse_Get_UnknownField(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("bogus")})
	assert.Error(t, err)
}

func TestHTTPClientResponse_Get_NoArgs(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r}

	_, err := w.Get(bg, nil)
	assert.Error(t, err)
}

func TestHTTPClientResponse_Get_MissingKey(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	w := &HTTPClientResponseWrapper{R: r}

	_, err := w.Get(bg, []cty.Value{cty.StringVal("header")})
	assert.Error(t, err)
}

// --- richcty.Stringable / richcty.Lengthable ---

func TestHTTPClientResponse_ToString(t *testing.T) {
	r := newTestResponse(200, "200 OK", "hello")
	w := &HTTPClientResponseWrapper{R: r}

	s, err := w.ToString(bg)
	require.NoError(t, err)
	assert.Equal(t, "hello", s)
}

func TestHTTPClientResponse_Length_FromContentLength(t *testing.T) {
	r := newTestResponse(200, "200 OK", "hello")
	r.ContentLength = 5
	w := &HTTPClientResponseWrapper{R: r}

	n, err := w.Length(bg)
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
}

func TestHTTPClientResponse_Length_FromBuffered(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.ContentLength = -1 // unknown
	w := &HTTPClientResponseWrapper{R: r, BufferedBody: []byte("buffered")}

	n, err := w.Length(bg)
	require.NoError(t, err)
	assert.Equal(t, int64(8), n)
}

func TestHTTPClientResponse_Length_Unknown(t *testing.T) {
	r := newTestResponse(200, "200 OK", "")
	r.ContentLength = -1
	w := &HTTPClientResponseWrapper{R: r}

	n, err := w.Length(bg)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), n)
}

// --- Interface satisfaction (compile-time) ---

var (
	_ richcty.Stringable = (*HTTPClientResponseWrapper)(nil)
	_ richcty.Lengthable = (*HTTPClientResponseWrapper)(nil)
	_ richcty.Gettable   = (*HTTPClientResponseWrapper)(nil)
)
