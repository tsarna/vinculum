package types

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// --- BuildHTTPResponseObject tests ---

func TestBuildHTTPResponseObject_BasicFields(t *testing.T) {
	r := &HTTPResponseWrapper{
		Status:  200,
		Headers: make(http.Header),
	}
	obj := BuildHTTPResponseObject(r)

	assert.Equal(t, HTTPResponseObjectType, obj.Type())
	assert.Equal(t, cty.NumberIntVal(200), obj.GetAttr("status"))
	assert.True(t, obj.GetAttr("headers").RawEquals(cty.MapValEmpty(cty.List(cty.String))))
	assert.Equal(t, HTTPResponseCapsuleType, obj.GetAttr("_capsule").Type())
}

func TestBuildHTTPResponseObject_WithHeaders(t *testing.T) {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Add("X-Custom", "a")
	h.Add("X-Custom", "b")
	r := &HTTPResponseWrapper{Status: 201, Headers: h}

	obj := BuildHTTPResponseObject(r)
	headers := obj.GetAttr("headers")
	assert.True(t, headers.Type().IsMapType())

	ct := headers.Index(cty.StringVal("Content-Type"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("application/json")}), ct)

	custom := headers.Index(cty.StringVal("X-Custom"))
	assert.Equal(t, cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}), custom)
}

// --- GetHTTPResponseFromValue tests ---

func TestGetHTTPResponseFromValue_FromObject(t *testing.T) {
	r := &HTTPResponseWrapper{Status: 404, Headers: make(http.Header)}
	obj := BuildHTTPResponseObject(r)

	got, ok := GetHTTPResponseFromValue(obj)
	require.True(t, ok)
	assert.Equal(t, 404, got.Status)
}

func TestGetHTTPResponseFromValue_FromCapsule(t *testing.T) {
	r := &HTTPResponseWrapper{Status: 500, Headers: make(http.Header)}
	cap := NewHTTPResponseCapsule(r)

	got, ok := GetHTTPResponseFromValue(cap)
	require.True(t, ok)
	assert.Equal(t, 500, got.Status)
}

func TestGetHTTPResponseFromValue_WrongType(t *testing.T) {
	_, ok := GetHTTPResponseFromValue(cty.StringVal("not a response"))
	assert.False(t, ok)
}

// --- CoerceBodyToBytes tests ---

func TestCoerceBodyToBytes_Null(t *testing.T) {
	body, ct, err := CoerceBodyToBytes(cty.NullVal(cty.String))
	require.NoError(t, err)
	assert.Nil(t, body)
	assert.Equal(t, "", ct)
}

func TestCoerceBodyToBytes_String(t *testing.T) {
	body, ct, err := CoerceBodyToBytes(cty.StringVal("hello"))
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), body)
	assert.Equal(t, "text/plain; charset=utf-8", ct)
}

func TestCoerceBodyToBytes_BytesObject_WithContentType(t *testing.T) {
	obj := BuildBytesObject([]byte{1, 2, 3}, "image/png")
	body, ct, err := CoerceBodyToBytes(obj)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, body)
	assert.Equal(t, "image/png", ct)
}

func TestCoerceBodyToBytes_BytesObject_EmptyContentType(t *testing.T) {
	obj := BuildBytesObject([]byte{1, 2, 3}, "")
	body, ct, err := CoerceBodyToBytes(obj)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, body)
	assert.Equal(t, "application/octet-stream", ct)
}

func TestCoerceBodyToBytes_Number(t *testing.T) {
	body, ct, err := CoerceBodyToBytes(cty.NumberIntVal(42))
	require.NoError(t, err)
	assert.Equal(t, "application/json", ct)
	assert.Contains(t, string(body), "42")
}

func TestCoerceBodyToBytes_Bool(t *testing.T) {
	body, ct, err := CoerceBodyToBytes(cty.BoolVal(true))
	require.NoError(t, err)
	assert.Equal(t, "application/json", ct)
	assert.Contains(t, string(body), "true")
}

func TestCoerceBodyToBytes_Object(t *testing.T) {
	obj := cty.ObjectVal(map[string]cty.Value{
		"status": cty.StringVal("ok"),
	})
	body, ct, err := CoerceBodyToBytes(obj)
	require.NoError(t, err)
	assert.Equal(t, "application/json", ct)
	assert.Contains(t, string(body), "ok")
}
