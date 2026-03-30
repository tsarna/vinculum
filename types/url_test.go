package types

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

const testURL = "https://user:pass@example.com:8080/some/path?a=1&b=2&b=3#section"

func parsedTestURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(testURL)
	require.NoError(t, err)
	return u
}

func TestBuildURLObject_Fields(t *testing.T) {
	u := parsedTestURL(t)
	obj := BuildURLObject(u)

	assert.Equal(t, cty.StringVal("https"), obj.GetAttr("scheme"))
	assert.Equal(t, cty.StringVal("example.com:8080"), obj.GetAttr("host"))
	assert.Equal(t, cty.StringVal("example.com"), obj.GetAttr("hostname"))
	assert.Equal(t, cty.StringVal("8080"), obj.GetAttr("port"))
	assert.Equal(t, cty.StringVal("/some/path"), obj.GetAttr("path"))
	assert.Equal(t, cty.StringVal("a=1&b=2&b=3"), obj.GetAttr("raw_query"))
	assert.Equal(t, cty.StringVal("section"), obj.GetAttr("fragment"))
	assert.Equal(t, cty.StringVal("user"), obj.GetAttr("username"))
	assert.Equal(t, cty.StringVal("pass"), obj.GetAttr("password"))
	assert.Equal(t, cty.BoolVal(true), obj.GetAttr("password_set"))
	assert.Equal(t, cty.BoolVal(false), obj.GetAttr("force_query"))
	assert.Equal(t, cty.BoolVal(false), obj.GetAttr("omit_host"))
}

func TestBuildURLObject_HasCapsule(t *testing.T) {
	u := parsedTestURL(t)
	obj := BuildURLObject(u)
	cap := obj.GetAttr("_capsule")
	assert.Equal(t, URLCapsuleType, cap.Type())
}

func TestBuildURLObject_Query(t *testing.T) {
	u := parsedTestURL(t)
	obj := BuildURLObject(u)
	q := obj.GetAttr("query")
	assert.True(t, q.Type().IsMapType())

	// b has two values
	bList := q.Index(cty.StringVal("b"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("2"), cty.StringVal("3")}), bList)

	// a has one value
	aList := q.Index(cty.StringVal("a"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("1")}), aList)
}

func TestBuildURLObject_EmptyQuery(t *testing.T) {
	u, _ := url.Parse("https://example.com/path")
	obj := BuildURLObject(u)
	q := obj.GetAttr("query")
	assert.True(t, q.Type().IsMapType())
	assert.True(t, q.RawEquals(cty.MapValEmpty(cty.List(cty.String))))
}

func TestBuildURLObject_NoUserinfo(t *testing.T) {
	u, _ := url.Parse("https://example.com/path")
	obj := BuildURLObject(u)
	assert.Equal(t, cty.StringVal(""), obj.GetAttr("username"))
	assert.Equal(t, cty.StringVal(""), obj.GetAttr("password"))
	assert.Equal(t, cty.BoolVal(false), obj.GetAttr("password_set"))
}

func TestURLWrapper_ToString(t *testing.T) {
	u := parsedTestURL(t)
	w := &URLWrapper{U: u}
	s, err := w.ToString(bg)
	require.NoError(t, err)
	assert.Equal(t, u.String(), s)
}

func TestURLWrapper_Get_QueryParam(t *testing.T) {
	u := parsedTestURL(t)
	w := &URLWrapper{U: u}

	// multi-value param
	result, err := w.Get(bg, []cty.Value{cty.StringVal("query_param"), cty.StringVal("b")})
	require.NoError(t, err)
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("2"), cty.StringVal("3")}), result)

	// single-value param
	result, err = w.Get(bg, []cty.Value{cty.StringVal("query_param"), cty.StringVal("a")})
	require.NoError(t, err)
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("1")}), result)

	// absent param
	result, err = w.Get(bg, []cty.Value{cty.StringVal("query_param"), cty.StringVal("missing")})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.ListValEmpty(cty.String)))
}

func TestURLWrapper_Get_NoArgs_Error(t *testing.T) {
	w := &URLWrapper{U: parsedTestURL(t)}
	_, err := w.Get(bg, nil)
	assert.Error(t, err)
}

func TestURLWrapper_Get_UnknownField_Error(t *testing.T) {
	w := &URLWrapper{U: parsedTestURL(t)}
	_, err := w.Get(bg, []cty.Value{cty.StringVal("bogus")})
	assert.Error(t, err)
}

func TestGetURLFromCapsule_RoundTrip(t *testing.T) {
	u := parsedTestURL(t)
	cap := NewURLCapsule(u)
	extracted, err := GetURLFromCapsule(cap)
	require.NoError(t, err)
	assert.Equal(t, u.String(), extracted.String())
}

func TestGetURLFromCapsule_WrongType(t *testing.T) {
	_, err := GetURLFromCapsule(cty.StringVal("not a capsule"))
	assert.Error(t, err)
}

func TestGetURLFromValue_String(t *testing.T) {
	u, err := GetURLFromValue(cty.StringVal("https://example.com/path"))
	require.NoError(t, err)
	assert.Equal(t, "https", u.Scheme)
	assert.Equal(t, "example.com", u.Host)
}

func TestGetURLFromValue_Capsule(t *testing.T) {
	orig := parsedTestURL(t)
	cap := NewURLCapsule(orig)
	u, err := GetURLFromValue(cap)
	require.NoError(t, err)
	assert.Equal(t, orig.String(), u.String())
}

func TestGetURLFromValue_Object(t *testing.T) {
	orig := parsedTestURL(t)
	obj := BuildURLObject(orig)
	u, err := GetURLFromValue(obj)
	require.NoError(t, err)
	assert.Equal(t, orig.String(), u.String())
}

func TestGetURLFromValue_InvalidType_Error(t *testing.T) {
	_, err := GetURLFromValue(cty.NumberIntVal(42))
	assert.Error(t, err)
}
