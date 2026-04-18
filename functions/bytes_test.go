package functions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bytescty "github.com/tsarna/bytes-cty-type"
	"github.com/zclconf/go-cty/cty"
)

// --- MakeFileBytesFunc() ---

func TestMakeFileBytesFunc_ReadFile(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "data.bin"), []byte{1, 2, 3, 4}, 0644))
	fn := MakeFileBytesFunc(base)

	result, err := fn.Call([]cty.Value{cty.StringVal("data.bin")})
	require.NoError(t, err)
	assert.Equal(t, bytescty.BytesObjectType, result.Type())
	b, err := bytescty.GetBytesFromValue(result)
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
	assert.Equal(t, bytescty.BytesObjectType, result.Type())
	b, err := bytescty.GetBytesFromValue(result)
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
