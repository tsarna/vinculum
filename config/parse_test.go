package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseConfigFilesDotDirectory guards against the dot-directory walk bug: a
// filepath.Walk of "." visits the root first with Name() == ".", which the
// skip-hidden-dirs check must not treat as a dot-directory — otherwise SkipDir on
// the root silently loads nothing (so `check .` / `serve .` from inside a config
// dir would report an empty, "valid" config).
func TestParseConfigFilesDotDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.vcl"), []byte(`bus "main" {}`), 0o644))
	// A hidden subdirectory must still be skipped.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden", "b.vcl"), []byte(`bus "other" {}`), 0o644))

	t.Chdir(dir)

	for _, arg := range []string{".", "./"} {
		bodies, diags := ParseConfigFiles(arg)
		require.False(t, diags.HasErrors(), "%q: %v", arg, diags)
		require.Len(t, bodies, 1, "%q should load the top-level .vcl (and skip the hidden subdir)", arg)
	}
}
