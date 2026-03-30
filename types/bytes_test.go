package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

func TestBytesGet_DefaultIsUTF8(t *testing.T) {
	b := &Bytes{Data: []byte("hello"), ContentType: "text/plain"}
	result, err := b.Get(bg, nil)
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("hello")))
}

func TestBytesGet_UTF8Modes(t *testing.T) {
	b := &Bytes{Data: []byte("hello")}
	for _, mode := range []string{"utf8", "string", "text"} {
		result, err := b.Get(bg, []cty.Value{cty.StringVal(mode)})
		assert.NoError(t, err, "mode=%s", mode)
		assert.True(t, result.RawEquals(cty.StringVal("hello")), "mode=%s", mode)
	}
}

func TestBytesGet_Base64(t *testing.T) {
	b := &Bytes{Data: []byte("hello")}
	result, err := b.Get(bg, []cty.Value{cty.StringVal("base64")})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("aGVsbG8=")))
}

func TestBytesGet_Len(t *testing.T) {
	b := &Bytes{Data: []byte("hello")}
	for _, mode := range []string{"len", "length", "size"} {
		result, err := b.Get(bg, []cty.Value{cty.StringVal(mode)})
		assert.NoError(t, err, "mode=%s", mode)
		assert.True(t, result.RawEquals(cty.NumberIntVal(5)), "mode=%s", mode)
	}
}

func TestBytesGet_ContentType(t *testing.T) {
	b := &Bytes{Data: []byte("data"), ContentType: "image/png"}
	for _, mode := range []string{"content_type", "content-type", "mime", "mime_type"} {
		result, err := b.Get(bg, []cty.Value{cty.StringVal(mode)})
		assert.NoError(t, err, "mode=%s", mode)
		assert.True(t, result.RawEquals(cty.StringVal("image/png")), "mode=%s", mode)
	}
}

func TestBytesGet_ContentType_Empty(t *testing.T) {
	b := &Bytes{Data: []byte("data")}
	result, err := b.Get(bg, []cty.Value{cty.StringVal("content_type")})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("")))
}

func TestBytesGet_UnknownMode(t *testing.T) {
	b := &Bytes{Data: []byte("data")}
	_, err := b.Get(bg, []cty.Value{cty.StringVal("invalid")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestBytesGet_NonStringMode(t *testing.T) {
	b := &Bytes{Data: []byte("data")}
	_, err := b.Get(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)
}

func TestNewBytesCapsule_RoundTrip(t *testing.T) {
	val := NewBytesCapsule([]byte("test"), "text/plain")
	assert.Equal(t, BytesCapsuleType, val.Type())

	b, err := GetBytesFromCapsule(val)
	assert.NoError(t, err)
	assert.Equal(t, []byte("test"), b.Data)
	assert.Equal(t, "text/plain", b.ContentType)
}

func TestGetBytesFromCapsule_WrongType(t *testing.T) {
	_, err := GetBytesFromCapsule(cty.StringVal("not a capsule"))
	assert.Error(t, err)
}
