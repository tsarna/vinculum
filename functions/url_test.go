package functions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

func TestURLParse_Fields(t *testing.T) {
	fn := makeURLParseFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("https://user:pass@example.com:8080/path?k=v#frag")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("https"), result.GetAttr("scheme"))
	assert.Equal(t, cty.StringVal("example.com:8080"), result.GetAttr("host"))
	assert.Equal(t, cty.StringVal("example.com"), result.GetAttr("hostname"))
	assert.Equal(t, cty.StringVal("8080"), result.GetAttr("port"))
	assert.Equal(t, cty.StringVal("/path"), result.GetAttr("path"))
	assert.Equal(t, cty.StringVal("k=v"), result.GetAttr("raw_query"))
	assert.Equal(t, cty.StringVal("frag"), result.GetAttr("fragment"))
	assert.Equal(t, cty.StringVal("user"), result.GetAttr("username"))
	assert.Equal(t, cty.StringVal("pass"), result.GetAttr("password"))
}

func TestURLParse_HasCapsule(t *testing.T) {
	fn := makeURLParseFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("https://example.com")})
	require.NoError(t, err)
	cap := result.GetAttr("_capsule")
	assert.Equal(t, types.URLCapsuleType, cap.Type())
}

func TestURLJoin_RelativeRef(t *testing.T) {
	fn := makeURLJoinFunc()
	// RFC 3986: ../c against /a/b (no trailing slash) resolves to /c
	// (parent of last segment "b" is /a/, parent of /a/ is /)
	result, err := fn.Call([]cty.Value{
		cty.StringVal("https://example.com/a/b"),
		cty.StringVal("../c"),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("/c"), result.GetAttr("path"))
	assert.Equal(t, cty.StringVal("https"), result.GetAttr("scheme"))
	assert.Equal(t, cty.StringVal("example.com"), result.GetAttr("hostname"))
}

func TestURLJoin_AbsoluteRef(t *testing.T) {
	fn := makeURLJoinFunc()
	result, err := fn.Call([]cty.Value{
		cty.StringVal("https://example.com/base/"),
		cty.StringVal("/absolute"),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("/absolute"), result.GetAttr("path"))
}

func TestURLJoin_AcceptsURLObject(t *testing.T) {
	parseFn := makeURLParseFunc()
	base, err := parseFn.Call([]cty.Value{cty.StringVal("https://example.com/a/b")})
	require.NoError(t, err)

	joinFn := makeURLJoinFunc()
	// same RFC 3986 resolution: ../c against /a/b gives /c
	result, err := joinFn.Call([]cty.Value{base, cty.StringVal("../c")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("/c"), result.GetAttr("path"))
	assert.Equal(t, cty.StringVal("example.com"), result.GetAttr("hostname"))
}

func TestURLJoinPath(t *testing.T) {
	fn := makeURLJoinPathFunc()
	result, err := fn.Call([]cty.Value{
		cty.StringVal("https://api.example.com/v1"),
		cty.StringVal("users"),
		cty.StringVal("42"),
		cty.StringVal("profile"),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("/v1/users/42/profile"), result.GetAttr("path"))
	assert.Equal(t, cty.StringVal("api.example.com"), result.GetAttr("hostname"))
}

func TestURLJoinPath_EscapesSegments(t *testing.T) {
	fn := makeURLJoinPathFunc()
	// Use base with trailing slash so JoinPath produces an absolute path
	result, err := fn.Call([]cty.Value{
		cty.StringVal("https://example.com/"),
		cty.StringVal("hello world"),
	})
	require.NoError(t, err)
	// path holds the decoded form (space as space)
	assert.Equal(t, cty.StringVal("/hello world"), result.GetAttr("path"))
	// Verify encoding via tostring — space should appear as %20 in the URL string
	tsFn := richcty.GetGenericFunctions()["tostring"]
	str, err := tsFn.Call([]cty.Value{result})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("https://example.com/hello%20world"), str)
}

func TestURLQueryEncode_StringMap(t *testing.T) {
	fn := makeURLQueryEncodeFunc()
	result, err := fn.Call([]cty.Value{
		cty.MapVal(map[string]cty.Value{
			"q":    cty.StringVal("hello world"),
			"page": cty.StringVal("2"),
		}),
	})
	require.NoError(t, err)
	// keys are sorted: page, q
	assert.Equal(t, cty.StringVal("page=2&q=hello+world"), result)
}

func TestURLQueryEncode_ListMap(t *testing.T) {
	fn := makeURLQueryEncodeFunc()
	result, err := fn.Call([]cty.Value{
		cty.MapVal(map[string]cty.Value{
			"tag": cty.ListVal([]cty.Value{
				cty.StringVal("go"),
				cty.StringVal("cty"),
			}),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("tag=go&tag=cty"), result)
}

func TestURLQueryEncode_NonMap_Error(t *testing.T) {
	fn := makeURLQueryEncodeFunc()
	_, err := fn.Call([]cty.Value{cty.StringVal("not a map")})
	assert.Error(t, err)
}

func TestURLQueryDecode(t *testing.T) {
	fn := makeURLQueryDecodeFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("a=1&b=2&b=3")})
	require.NoError(t, err)

	aList := result.Index(cty.StringVal("a"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("1")}), aList)

	bList := result.Index(cty.StringVal("b"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("2"), cty.StringVal("3")}), bList)
}

func TestURLQueryDecode_StripLeadingQuestionMark(t *testing.T) {
	fn := makeURLQueryDecodeFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("?k=v")})
	require.NoError(t, err)
	assert.True(t, result.Type().IsMapType())
	kList := result.Index(cty.StringVal("k"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("v")}), kList)
}

func TestURLQueryDecode_Empty(t *testing.T) {
	fn := makeURLQueryDecodeFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("")})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.MapValEmpty(cty.List(cty.String))))
}

func TestURLDecode(t *testing.T) {
	fn := makeURLDecodeFunc()

	result, err := fn.Call([]cty.Value{cty.StringVal("hello+world")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("hello world"), result)

	result, err = fn.Call([]cty.Value{cty.StringVal("caf%C3%A9")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("café"), result)
}

func TestURLDecode_InvalidEncoding_Error(t *testing.T) {
	fn := makeURLDecodeFunc()
	_, err := fn.Call([]cty.Value{cty.StringVal("%zz")})
	assert.Error(t, err)
}
