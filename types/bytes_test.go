package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestBuildBytesObject_Fields(t *testing.T) {
	obj := BuildBytesObject([]byte("hello"), "text/plain")
	assert.Equal(t, BytesObjectType, obj.Type())
	assert.Equal(t, cty.StringVal("text/plain"), obj.GetAttr("content_type"))
	assert.Equal(t, BytesCapsuleType, obj.GetAttr("_capsule").Type())
}

func TestBuildBytesObject_EmptyContentType(t *testing.T) {
	obj := BuildBytesObject([]byte("data"), "")
	assert.Equal(t, cty.StringVal(""), obj.GetAttr("content_type"))
}

func TestGetBytesFromValue_Capsule(t *testing.T) {
	cap := NewBytesCapsule([]byte("hello"), "text/plain")
	b, err := GetBytesFromValue(cap)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), b.Data)
	assert.Equal(t, "text/plain", b.ContentType)
}

func TestGetBytesFromValue_Object(t *testing.T) {
	obj := BuildBytesObject([]byte("world"), "image/png")
	b, err := GetBytesFromValue(obj)
	require.NoError(t, err)
	assert.Equal(t, []byte("world"), b.Data)
	assert.Equal(t, "image/png", b.ContentType)
}

func TestGetBytesFromValue_InvalidType_Error(t *testing.T) {
	_, err := GetBytesFromValue(cty.StringVal("not bytes"))
	assert.Error(t, err)
}
