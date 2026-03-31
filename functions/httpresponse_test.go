package functions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

// --- http_response ---

func TestHTTPResponseFunc_StatusOnly(t *testing.T) {
	result, err := HTTPResponseFunc.Call([]cty.Value{cty.NumberIntVal(204)})
	require.NoError(t, err)
	assert.Equal(t, types.HTTPResponseObjectType, result.Type())
	assert.Equal(t, cty.NumberIntVal(204), result.GetAttr("status"))

	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Nil(t, r.Body)
}

func TestHTTPResponseFunc_WithStringBody(t *testing.T) {
	result, err := HTTPResponseFunc.Call([]cty.Value{
		cty.NumberIntVal(200),
		cty.StringVal("hello"),
	})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), r.Body)
	assert.Equal(t, "text/plain; charset=utf-8", r.ContentType)
}

func TestHTTPResponseFunc_WithObjectBody(t *testing.T) {
	body := cty.ObjectVal(map[string]cty.Value{"ok": cty.BoolVal(true)})
	result, err := HTTPResponseFunc.Call([]cty.Value{cty.NumberIntVal(200), body})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, "application/json", r.ContentType)
	assert.Contains(t, string(r.Body), "true")
}

func TestHTTPResponseFunc_WithHeaders_MapString(t *testing.T) {
	headers := cty.MapVal(map[string]cty.Value{
		"X-Custom": cty.StringVal("value"),
	})
	result, err := HTTPResponseFunc.Call([]cty.Value{
		cty.NumberIntVal(200),
		cty.StringVal("body"),
		headers,
	})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, "value", r.Headers.Get("X-Custom"))
}

func TestHTTPResponseFunc_WithHeaders_ObjectMultivalue(t *testing.T) {
	headers := cty.ObjectVal(map[string]cty.Value{
		"X-Multi": cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
	})
	result, err := HTTPResponseFunc.Call([]cty.Value{
		cty.NumberIntVal(200),
		cty.StringVal("body"),
		headers,
	})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, r.Headers.Values("X-Multi"))
}

// --- http_redirect ---

func TestHTTPRedirectFunc_URLOnly(t *testing.T) {
	result, err := HTTPRedirectFunc.Call([]cty.Value{cty.StringVal("/new-path")})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, 302, r.Status)
	assert.Equal(t, "/new-path", r.Headers.Get("Location"))
}

func TestHTTPRedirectFunc_StatusAndURL(t *testing.T) {
	result, err := HTTPRedirectFunc.Call([]cty.Value{
		cty.NumberIntVal(301),
		cty.StringVal("/permanent"),
	})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, 301, r.Status)
	assert.Equal(t, "/permanent", r.Headers.Get("Location"))
}

func TestHTTPRedirectFunc_URLOnly_TooManyArgs(t *testing.T) {
	_, err := HTTPRedirectFunc.Call([]cty.Value{
		cty.StringVal("/path"),
		cty.StringVal("/extra"),
	})
	assert.Error(t, err)
}

func TestHTTPRedirectFunc_StatusOnly_MissingURL(t *testing.T) {
	_, err := HTTPRedirectFunc.Call([]cty.Value{cty.NumberIntVal(302)})
	assert.Error(t, err)
}

func TestHTTPRedirectFunc_InvalidFirstArg(t *testing.T) {
	_, err := HTTPRedirectFunc.Call([]cty.Value{cty.BoolVal(true)})
	assert.Error(t, err)
}

// --- http_error ---

func TestHTTPErrorFunc_Basic(t *testing.T) {
	result, err := HTTPErrorFunc.Call([]cty.Value{
		cty.NumberIntVal(404),
		cty.StringVal("not found"),
	})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, 404, r.Status)
	assert.Equal(t, []byte("not found"), r.Body)
	assert.Equal(t, "text/plain; charset=utf-8", r.ContentType)
	assert.True(t, r.IsError)
}

// --- addheader ---

func TestAddHeaderFunc_AppendsHeader(t *testing.T) {
	base, err := HTTPResponseFunc.Call([]cty.Value{cty.NumberIntVal(200)})
	require.NoError(t, err)

	result, err := AddHeaderFunc.Call([]cty.Value{
		base,
		cty.StringVal("X-Foo"),
		cty.StringVal("bar"),
	})
	require.NoError(t, err)
	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Equal(t, "bar", r.Headers.Get("X-Foo"))
}

func TestAddHeaderFunc_MultiValue(t *testing.T) {
	base, err := HTTPResponseFunc.Call([]cty.Value{cty.NumberIntVal(200)})
	require.NoError(t, err)

	r1, err := AddHeaderFunc.Call([]cty.Value{base, cty.StringVal("X-Tag"), cty.StringVal("a")})
	require.NoError(t, err)
	r2, err := AddHeaderFunc.Call([]cty.Value{r1, cty.StringVal("X-Tag"), cty.StringVal("b")})
	require.NoError(t, err)

	r, ok := types.GetHTTPResponseFromValue(r2)
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, r.Headers.Values("X-Tag"))
}

func TestAddHeaderFunc_DoesNotMutateOriginal(t *testing.T) {
	base, err := HTTPResponseFunc.Call([]cty.Value{cty.NumberIntVal(200)})
	require.NoError(t, err)

	_, err = AddHeaderFunc.Call([]cty.Value{base, cty.StringVal("X-Foo"), cty.StringVal("bar")})
	require.NoError(t, err)

	orig, ok := types.GetHTTPResponseFromValue(base)
	require.True(t, ok)
	assert.Empty(t, orig.Headers.Get("X-Foo"))
}

func TestAddHeaderFunc_InvalidResponse(t *testing.T) {
	_, err := AddHeaderFunc.Call([]cty.Value{
		cty.StringVal("not a response"),
		cty.StringVal("X-Foo"),
		cty.StringVal("bar"),
	})
	assert.Error(t, err)
}

// --- removeheader ---

func TestRemoveHeaderFunc_RemovesHeader(t *testing.T) {
	headers := cty.MapVal(map[string]cty.Value{"X-Remove": cty.StringVal("yes")})
	base, err := HTTPResponseFunc.Call([]cty.Value{
		cty.NumberIntVal(200),
		cty.StringVal("body"),
		headers,
	})
	require.NoError(t, err)

	result, err := RemoveHeaderFunc.Call([]cty.Value{base, cty.StringVal("X-Remove")})
	require.NoError(t, err)

	r, ok := types.GetHTTPResponseFromValue(result)
	require.True(t, ok)
	assert.Empty(t, r.Headers.Get("X-Remove"))
}

func TestRemoveHeaderFunc_DoesNotMutateOriginal(t *testing.T) {
	headers := cty.MapVal(map[string]cty.Value{"X-Keep": cty.StringVal("yes")})
	base, err := HTTPResponseFunc.Call([]cty.Value{
		cty.NumberIntVal(200),
		cty.StringVal("body"),
		headers,
	})
	require.NoError(t, err)

	_, err = RemoveHeaderFunc.Call([]cty.Value{base, cty.StringVal("X-Keep")})
	require.NoError(t, err)

	orig, ok := types.GetHTTPResponseFromValue(base)
	require.True(t, ok)
	assert.Equal(t, "yes", orig.Headers.Get("X-Keep"))
}

func TestRemoveHeaderFunc_InvalidResponse(t *testing.T) {
	_, err := RemoveHeaderFunc.Call([]cty.Value{
		cty.StringVal("not a response"),
		cty.StringVal("X-Foo"),
	})
	assert.Error(t, err)
}

// --- setcookie ---

func TestSetCookieFunc_Basic(t *testing.T) {
	cookie := cty.ObjectVal(map[string]cty.Value{
		"name":  cty.StringVal("session"),
		"value": cty.StringVal("abc123"),
	})
	result, err := SetCookieFunc.Call([]cty.Value{cookie})
	require.NoError(t, err)
	s := result.AsString()
	assert.Contains(t, s, "session=abc123")
}

func TestSetCookieFunc_AllFields(t *testing.T) {
	expires := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	cookie := cty.ObjectVal(map[string]cty.Value{
		"name":        cty.StringVal("sid"),
		"value":       cty.StringVal("xyz"),
		"path":        cty.StringVal("/"),
		"domain":      cty.StringVal("example.com"),
		"expires":     timecty.NewTimeCapsule(expires),
		"max_age":     cty.NumberIntVal(3600),
		"secure":      cty.BoolVal(true),
		"http_only":   cty.BoolVal(true),
		"same_site":   cty.StringVal("Lax"),
		"partitioned": cty.BoolVal(false),
	})
	result, err := SetCookieFunc.Call([]cty.Value{cookie})
	require.NoError(t, err)
	s := result.AsString()
	assert.Contains(t, s, "sid=xyz")
	assert.Contains(t, s, "Path=/")
	assert.Contains(t, s, "Domain=example.com")
	assert.Contains(t, s, "Max-Age=3600")
	assert.Contains(t, s, "Secure")
	assert.Contains(t, s, "HttpOnly")
	assert.Contains(t, s, "SameSite=Lax")
}

func TestSetCookieFunc_ExpiresRFC3339String(t *testing.T) {
	cookie := cty.ObjectVal(map[string]cty.Value{
		"name":    cty.StringVal("tok"),
		"value":   cty.StringVal("v"),
		"expires": cty.StringVal("2030-06-01T00:00:00Z"),
	})
	result, err := SetCookieFunc.Call([]cty.Value{cookie})
	require.NoError(t, err)
	assert.Contains(t, result.AsString(), "tok=v")
}

func TestSetCookieFunc_ExpiresDuration(t *testing.T) {
	cookie := cty.ObjectVal(map[string]cty.Value{
		"name":    cty.StringVal("tok"),
		"value":   cty.StringVal("v"),
		"expires": timecty.NewDurationCapsule(time.Hour),
	})
	result, err := SetCookieFunc.Call([]cty.Value{cookie})
	require.NoError(t, err)
	assert.Contains(t, result.AsString(), "tok=v")
	assert.Contains(t, result.AsString(), "Expires=")
}

func TestSetCookieFunc_MissingName(t *testing.T) {
	cookie := cty.ObjectVal(map[string]cty.Value{
		"value": cty.StringVal("abc"),
	})
	_, err := SetCookieFunc.Call([]cty.Value{cookie})
	assert.Error(t, err)
}

func TestSetCookieFunc_MissingValue(t *testing.T) {
	cookie := cty.ObjectVal(map[string]cty.Value{
		"name": cty.StringVal("sid"),
	})
	_, err := SetCookieFunc.Call([]cty.Value{cookie})
	assert.Error(t, err)
}

func TestSetCookieFunc_NonObject(t *testing.T) {
	_, err := SetCookieFunc.Call([]cty.Value{cty.StringVal("not an object")})
	assert.Error(t, err)
}

func TestSetCookieFunc_InvalidExpiresString(t *testing.T) {
	cookie := cty.ObjectVal(map[string]cty.Value{
		"name":    cty.StringVal("tok"),
		"value":   cty.StringVal("v"),
		"expires": cty.StringVal("not-a-date"),
	})
	_, err := SetCookieFunc.Call([]cty.Value{cookie})
	assert.Error(t, err)
}

func TestSetCookieFunc_SameSiteVariants(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		expected string
	}{
		{"Strict", "SameSite=Strict"},
		{"None", "SameSite=None"},
		{"Lax", "SameSite=Lax"},
	} {
		cookie := cty.ObjectVal(map[string]cty.Value{
			"name":      cty.StringVal("c"),
			"value":     cty.StringVal("v"),
			"same_site": cty.StringVal(tc.mode),
		})
		result, err := SetCookieFunc.Call([]cty.Value{cookie})
		require.NoError(t, err, "mode %s", tc.mode)
		assert.Contains(t, result.AsString(), tc.expected, "mode %s", tc.mode)
	}
}
