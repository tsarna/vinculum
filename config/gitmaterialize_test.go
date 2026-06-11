package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// makeWorktree builds a fake checked-out tree (no real git needed) including a
// .git directory that materialization must skip.
func makeWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".git/HEAD":        "ref: refs/heads/main",
		"README.md":        "readme",
		"config/a.vcl":     "// a",
		"config/sub/b.vcl": "// b",
		"www/index.html":   "<html>",
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func materialize(t *testing.T, work string, f GitFetch) hcl.Diagnostics {
	t.Helper()
	def := &GitDefinition{Label: "x", Fetches: []GitFetch{f}}
	return materializeFetches(def, work, zap.NewNop())
}

func TestMaterialize_WholeTree(t *testing.T) {
	work := makeWorktree(t)
	into := filepath.Join(t.TempDir(), "dest")

	diags := materialize(t, work, GitFetch{Name: "all", Into: into})
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)

	assert.FileExists(t, filepath.Join(into, "README.md"))
	assert.FileExists(t, filepath.Join(into, "config", "a.vcl"))
	assert.FileExists(t, filepath.Join(into, "config", "sub", "b.vcl"))
	// .git must never be copied.
	_, err := os.Stat(filepath.Join(into, ".git"))
	assert.True(t, os.IsNotExist(err), "expected .git to be skipped")
}

func TestMaterialize_Subtree(t *testing.T) {
	work := makeWorktree(t)
	into := filepath.Join(t.TempDir(), "dest")

	diags := materialize(t, work, GitFetch{Name: "cfg", From: "config", Into: into})
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)

	assert.FileExists(t, filepath.Join(into, "a.vcl"))
	assert.FileExists(t, filepath.Join(into, "sub", "b.vcl"))
	// Files outside the subtree are not present.
	_, err := os.Stat(filepath.Join(into, "README.md"))
	assert.True(t, os.IsNotExist(err))
}

func TestMaterialize_SingleFile(t *testing.T) {
	work := makeWorktree(t)
	into := filepath.Join(t.TempDir(), "dest")

	diags := materialize(t, work, GitFetch{Name: "one", From: "config/a.vcl", Into: into})
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)
	assert.FileExists(t, filepath.Join(into, "a.vcl"))
}

func TestMaterialize_NonEmptyNoOverwriteFatal(t *testing.T) {
	work := makeWorktree(t)
	into := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(into, "existing.txt"), []byte("x"), 0o644))

	diags := materialize(t, work, GitFetch{Name: "all", Into: into})
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "not empty")
}

func TestMaterialize_NonEmptyOverwriteReplaces(t *testing.T) {
	work := makeWorktree(t)
	into := t.TempDir()
	stale := filepath.Join(into, "stale.txt")
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0o644))

	diags := materialize(t, work, GitFetch{Name: "all", Into: into, Overwrite: true})
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)

	assert.FileExists(t, filepath.Join(into, "README.md"))
	_, err := os.Stat(stale)
	assert.True(t, os.IsNotExist(err), "stale file should be removed")
}

func TestMaterialize_EmptyDirTreatedFresh(t *testing.T) {
	work := makeWorktree(t)
	into := t.TempDir() // exists, empty

	diags := materialize(t, work, GitFetch{Name: "all", Into: into})
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)
	assert.FileExists(t, filepath.Join(into, "README.md"))
}

func TestMaterialize_MissingFromFatal(t *testing.T) {
	work := makeWorktree(t)
	into := filepath.Join(t.TempDir(), "dest")

	diags := materialize(t, work, GitFetch{Name: "x", From: "does/not/exist", Into: into})
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "does not exist")
}

func TestMaterialize_MultipleFetches(t *testing.T) {
	work := makeWorktree(t)
	dest1 := filepath.Join(t.TempDir(), "d1")
	dest2 := filepath.Join(t.TempDir(), "d2")
	def := &GitDefinition{Label: "x", Fetches: []GitFetch{
		{Name: "cfg", From: "config", Into: dest1},
		{Name: "www", From: "www", Into: dest2},
	}}
	diags := materializeFetches(def, work, zap.NewNop())
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)
	assert.FileExists(t, filepath.Join(dest1, "a.vcl"))
	assert.FileExists(t, filepath.Join(dest2, "index.html"))
}
