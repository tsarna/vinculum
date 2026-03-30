package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

func TestBytesToString(t *testing.T) {
	b := &Bytes{Data: []byte("hello"), ContentType: "text/plain"}
	s, err := b.ToString(bg)
	assert.NoError(t, err)
	assert.Equal(t, "hello", s)
}

func TestBytesLength(t *testing.T) {
	b := &Bytes{Data: []byte("hello")}
	n, err := b.Length(bg)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), n)
}

func TestBytesGet_ContentType(t *testing.T) {
	b := &Bytes{Data: []byte("data"), ContentType: "image/png"}
	result, err := b.Get(bg, []cty.Value{cty.StringVal("content_type")})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("image/png")))
}

func TestBytesGet_ContentType_Empty(t *testing.T) {
	b := &Bytes{Data: []byte("data")}
	result, err := b.Get(bg, []cty.Value{cty.StringVal("content_type")})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("")))
}

func TestBytesGet_NoArgs_Error(t *testing.T) {
	b := &Bytes{Data: []byte("data")}
	_, err := b.Get(bg, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "field argument required")
}

func TestBytesGet_UnknownField(t *testing.T) {
	b := &Bytes{Data: []byte("data")}
	_, err := b.Get(bg, []cty.Value{cty.StringVal("invalid")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestBytesGet_NonStringField(t *testing.T) {
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
