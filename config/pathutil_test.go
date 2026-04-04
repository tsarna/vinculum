package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeResolvePath(t *testing.T) {
	base := t.TempDir()

	t.Run("relative path within base", func(t *testing.T) {
		got, err := SafeResolvePath(base, "foo.txt")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "foo.txt"), got)
	})

	t.Run("absolute path within base", func(t *testing.T) {
		got, err := SafeResolvePath(base, filepath.Join(base, "bar.txt"))
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "bar.txt"), got)
	})

	t.Run("directory traversal rejected", func(t *testing.T) {
		_, err := SafeResolvePath(base, "../escape.txt")
		assert.Error(t, err)
	})

	t.Run("absolute path outside base rejected", func(t *testing.T) {
		_, err := SafeResolvePath(base, "/etc/passwd")
		assert.Error(t, err)
	})

	t.Run("tilde rejected", func(t *testing.T) {
		_, err := SafeResolvePath(base, "~/sensitive")
		assert.Error(t, err)
	})
}
