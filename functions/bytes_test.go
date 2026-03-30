package functions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

// --- bytes() ---

func TestBytesFunc_FromString(t *testing.T) {
	fn := makeBytesFunc()

	result, err := fn.Call([]cty.Value{cty.StringVal("hello")})
	require.NoError(t, err)
	assert.Equal(t, types.BytesObjectType, result.Type())
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), b.Data)
	assert.Equal(t, "", b.ContentType)
}

func TestBytesFunc_FromStringWithContentType(t *testing.T) {
	fn := makeBytesFunc()

	result, err := fn.Call([]cty.Value{cty.StringVal("hello"), cty.StringVal("text/plain")})
	require.NoError(t, err)
	assert.Equal(t, types.BytesObjectType, result.Type())
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), b.Data)
	assert.Equal(t, "text/plain", b.ContentType)
}

func TestBytesFunc_FromBytesObject_PreservesContentType(t *testing.T) {
	fn := makeBytesFunc()
	src := types.BuildBytesObject([]byte{1, 2, 3}, "image/png")

	result, err := fn.Call([]cty.Value{src})
	require.NoError(t, err)
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, b.Data)
	assert.Equal(t, "image/png", b.ContentType)
}

func TestBytesFunc_FromBytesObject_OverridesContentType(t *testing.T) {
	fn := makeBytesFunc()
	src := types.BuildBytesObject([]byte{1, 2, 3}, "image/png")

	result, err := fn.Call([]cty.Value{src, cty.StringVal("image/jpeg")})
	require.NoError(t, err)
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, b.Data)
	assert.Equal(t, "image/jpeg", b.ContentType)
}

func TestBytesFunc_InvalidType(t *testing.T) {
	fn := makeBytesFunc()
	_, err := fn.Call([]cty.Value{cty.NumberIntVal(42)})
	assert.Error(t, err)
}

// --- base64encode() ---

func TestBase64EncodeFunc_FromString(t *testing.T) {
	fn := makeBase64EncodeFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("hello")})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("aGVsbG8=")))
}

func TestBase64EncodeFunc_FromBytesObject(t *testing.T) {
	fn := makeBase64EncodeFunc()
	b := types.BuildBytesObject([]byte("hello"), "")
	result, err := fn.Call([]cty.Value{b})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("aGVsbG8=")))
}

func TestBase64EncodeFunc_InvalidType(t *testing.T) {
	fn := makeBase64EncodeFunc()
	_, err := fn.Call([]cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)
}

// --- base64decode() ---

func TestBase64DecodeFunc_OneArg_ReturnsString(t *testing.T) {
	fn := makeBase64DecodeFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("aGVsbG8=")})
	require.NoError(t, err)
	assert.Equal(t, cty.String, result.Type())
	assert.True(t, result.RawEquals(cty.StringVal("hello")))
}

func TestBase64DecodeFunc_TwoArgs_ReturnsBytesWithContentType(t *testing.T) {
	fn := makeBase64DecodeFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("aGVsbG8="), cty.StringVal("text/plain")})
	require.NoError(t, err)
	assert.Equal(t, types.BytesObjectType, result.Type())
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), b.Data)
	assert.Equal(t, "text/plain", b.ContentType)
}

func TestBase64DecodeFunc_TwoArgs_EmptyContentType(t *testing.T) {
	fn := makeBase64DecodeFunc()
	result, err := fn.Call([]cty.Value{cty.StringVal("aGVsbG8="), cty.StringVal("")})
	require.NoError(t, err)
	assert.Equal(t, types.BytesObjectType, result.Type())
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), b.Data)
	assert.Equal(t, "", b.ContentType)
}

func TestBase64DecodeFunc_InvalidBase64(t *testing.T) {
	fn := makeBase64DecodeFunc()
	_, err := fn.Call([]cty.Value{cty.StringVal("not-valid-base64!!!")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "base64decode")
}

// --- MakeFileBytesFunc() ---

func TestMakeFileBytesFunc_ReadFile(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "data.bin"), []byte{1, 2, 3, 4}, 0644))
	fn := MakeFileBytesFunc(base)

	result, err := fn.Call([]cty.Value{cty.StringVal("data.bin")})
	require.NoError(t, err)
	assert.Equal(t, types.BytesObjectType, result.Type())
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3, 4}, b.Data)
	assert.Equal(t, "", b.ContentType)
}

func TestMakeFileBytesFunc_ReadFileWithContentType(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "img.png"), []byte{0x89, 0x50}, 0644))
	fn := MakeFileBytesFunc(base)

	result, err := fn.Call([]cty.Value{cty.StringVal("img.png"), cty.StringVal("image/png")})
	require.NoError(t, err)
	assert.Equal(t, types.BytesObjectType, result.Type())
	b, err := types.GetBytesFromValue(result)
	require.NoError(t, err)
	assert.Equal(t, []byte{0x89, 0x50}, b.Data)
	assert.Equal(t, "image/png", b.ContentType)
}

func TestMakeFileBytesFunc_TraversalRejected(t *testing.T) {
	base := t.TempDir()
	fn := MakeFileBytesFunc(base)

	_, err := fn.Call([]cty.Value{cty.StringVal("../escape.bin")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outside")
}

func TestMakeFileBytesFunc_AbsolutePathOutsideBaseRejected(t *testing.T) {
	base := t.TempDir()
	fn := MakeFileBytesFunc(base)

	_, err := fn.Call([]cty.Value{cty.StringVal("/etc/passwd")})
	assert.Error(t, err)
}

func TestMakeFileBytesFunc_MissingFile(t *testing.T) {
	base := t.TempDir()
	fn := MakeFileBytesFunc(base)

	_, err := fn.Call([]cty.Value{cty.StringVal("nonexistent.bin")})
	assert.Error(t, err)
}
