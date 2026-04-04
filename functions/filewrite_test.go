package functions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestMakeFileWriteFunc(t *testing.T) {
	base := t.TempDir()
	fn := MakeFileWriteFunc(base)

	t.Run("create new file", func(t *testing.T) {
		result, err := fn.Call([]cty.Value{
			cty.StringVal("hello.txt"),
			cty.StringVal("hello world"),
		})
		require.NoError(t, err)
		assert.Equal(t, cty.True, result)
		got, err := os.ReadFile(filepath.Join(base, "hello.txt"))
		require.NoError(t, err)
		assert.Equal(t, "hello world", string(got))
	})

	t.Run("overwrite existing file", func(t *testing.T) {
		path := filepath.Join(base, "overwrite.txt")
		require.NoError(t, os.WriteFile(path, []byte("old"), 0644))
		_, err := fn.Call([]cty.Value{
			cty.StringVal("overwrite.txt"),
			cty.StringVal("new content"),
		})
		require.NoError(t, err)
		got, _ := os.ReadFile(path)
		assert.Equal(t, "new content", string(got))
	})

	t.Run("traversal rejected", func(t *testing.T) {
		_, err := fn.Call([]cty.Value{
			cty.StringVal("../escape.txt"),
			cty.StringVal("bad"),
		})
		assert.Error(t, err)
	})
}

func TestMakeFileAppendFunc(t *testing.T) {
	base := t.TempDir()
	fn := MakeFileAppendFunc(base)

	t.Run("create and append to new file", func(t *testing.T) {
		result, err := fn.Call([]cty.Value{
			cty.StringVal("append.txt"),
			cty.StringVal("line1\n"),
		})
		require.NoError(t, err)
		assert.Equal(t, cty.True, result)
		got, _ := os.ReadFile(filepath.Join(base, "append.txt"))
		assert.Equal(t, "line1\n", string(got))
	})

	t.Run("append to existing file", func(t *testing.T) {
		_, err := fn.Call([]cty.Value{
			cty.StringVal("append.txt"),
			cty.StringVal("line2\n"),
		})
		require.NoError(t, err)
		got, _ := os.ReadFile(filepath.Join(base, "append.txt"))
		assert.Equal(t, "line1\nline2\n", string(got))
	})

	t.Run("traversal rejected", func(t *testing.T) {
		_, err := fn.Call([]cty.Value{
			cty.StringVal("../escape.txt"),
			cty.StringVal("bad"),
		})
		assert.Error(t, err)
	})
}
