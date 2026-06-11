package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// srcRepo builds a throwaway git repository on disk for clone tests. It commits
// the given files (path -> contents) on the default branch and returns the repo
// directory and the HEAD commit hash.
func srcRepo(t *testing.T, files map[string]string) (string, plumbing.Hash) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	for name, content := range files {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
		_, err := wt.Add(name)
		require.NoError(t, err)
	}
	h, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@example.com", When: time.Now()},
	})
	require.NoError(t, err)
	return dir, h
}

func cloneInto(t *testing.T, def *GitDefinition) (string, hcl.Diagnostics) {
	t.Helper()
	work := t.TempDir()
	diags := cloneRepo(def, work, zap.NewNop())
	return work, diags
}

func TestGitClone_DefaultBranch(t *testing.T) {
	src, _ := srcRepo(t, map[string]string{"config/a.vcl": "// a", "README.md": "hi"})
	def := &GitDefinition{Label: "x", Repo: src}
	work, diags := cloneInto(t, def)
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)
	assert.FileExists(t, filepath.Join(work, "config", "a.vcl"))
	assert.FileExists(t, filepath.Join(work, "README.md"))
}

func TestGitClone_Tag(t *testing.T) {
	src, head := srcRepo(t, map[string]string{"f.txt": "v"})
	repo, err := git.PlainOpen(src)
	require.NoError(t, err)
	_, err = repo.CreateTag("v1", head, nil)
	require.NoError(t, err)

	def := &GitDefinition{Label: "x", Repo: src, Tag: "v1"}
	work, diags := cloneInto(t, def)
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)
	assert.FileExists(t, filepath.Join(work, "f.txt"))
}

func TestGitClone_CommitFullDepth(t *testing.T) {
	src, head := srcRepo(t, map[string]string{"f.txt": "v"})
	full := 0
	def := &GitDefinition{Label: "x", Repo: src, Commit: head.String(), Depth: &full}
	work, diags := cloneInto(t, def)
	require.False(t, diags.HasErrors(), "unexpected: %v", diags)
	assert.FileExists(t, filepath.Join(work, "f.txt"))
}

func TestGitClone_CommitUnreachable(t *testing.T) {
	src, _ := srcRepo(t, map[string]string{"f.txt": "v"})
	// A syntactically valid but absent SHA; shallow clone cannot reach it.
	def := &GitDefinition{
		Label:  "x",
		Repo:   src,
		Commit: "0123456789012345678901234567890123456789",
	}
	_, diags := cloneInto(t, def)
	require.True(t, diags.HasErrors(), "expected checkout failure")
	assert.Contains(t, diags.Error(), "depth")
}

func TestGitClone_BadRepoFatal(t *testing.T) {
	def := &GitDefinition{Label: "x", Repo: filepath.Join(t.TempDir(), "nope")}
	_, diags := cloneInto(t, def)
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "git clone failed")
}

func TestGitAuth_HTTPToken(t *testing.T) {
	def := &GitDefinition{
		Label: "x",
		Repo:  "https://example.com/r.git",
		Auth:  &GitAuth{Token: "secret"},
	}
	am, diags := buildAuthMethod(def, zap.NewNop())
	require.False(t, diags.HasErrors())
	ba, ok := am.(*githttp.BasicAuth)
	require.True(t, ok, "expected BasicAuth, got %T", am)
	assert.Equal(t, "secret", ba.Password)
	assert.NotEmpty(t, ba.Username)
}

func TestGitAuth_HTTPUserPass(t *testing.T) {
	def := &GitDefinition{
		Label: "x",
		Repo:  "https://example.com/r.git",
		Auth:  &GitAuth{Username: "u", Password: "p"},
	}
	am, diags := buildAuthMethod(def, zap.NewNop())
	require.False(t, diags.HasErrors())
	ba, ok := am.(*githttp.BasicAuth)
	require.True(t, ok)
	assert.Equal(t, "u", ba.Username)
	assert.Equal(t, "p", ba.Password)
}

func TestGitAuth_AnonymousNil(t *testing.T) {
	def := &GitDefinition{Label: "x", Repo: "https://example.com/r.git"}
	am, diags := buildAuthMethod(def, zap.NewNop())
	require.False(t, diags.HasErrors())
	assert.Nil(t, am)
}

func TestGitAuth_SSHNoKeyFatal(t *testing.T) {
	def := &GitDefinition{
		Label: "x",
		Repo:  "git@github.com:org/r.git",
		Auth:  &GitAuth{KnownHosts: "/dev/null"},
	}
	_, diags := buildAuthMethod(def, zap.NewNop())
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "requires a key")
}

func TestGitHostKey_Insecure(t *testing.T) {
	def := &GitDefinition{Label: "x", Repo: "ssh://git@h/r.git", Auth: &GitAuth{InsecureIgnoreHostKey: true}}
	cb, diags := sshHostKeyCallback(def, zap.NewNop())
	require.False(t, diags.HasErrors())
	assert.NotNil(t, cb)
}

func TestGitHostKey_KnownHostsFile(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(kh, []byte(""), 0o644))
	def := &GitDefinition{Label: "x", Repo: "ssh://git@h/r.git", Auth: &GitAuth{KnownHosts: kh}}
	cb, diags := sshHostKeyCallback(def, zap.NewNop())
	require.False(t, diags.HasErrors())
	assert.NotNil(t, cb)
}

func TestGitHostKey_NoneFatal(t *testing.T) {
	// Point HOME at an empty dir so no default known_hosts exists.
	t.Setenv("HOME", t.TempDir())
	def := &GitDefinition{Label: "x", Repo: "ssh://git@h/r.git", Auth: &GitAuth{}}
	_, diags := sshHostKeyCallback(def, zap.NewNop())
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "known_hosts")
}

func TestGitSSHUser(t *testing.T) {
	assert.Equal(t, "git", sshUser("git@github.com:org/r.git"))
	assert.Equal(t, "deploy", sshUser("ssh://deploy@host/r.git"))
	assert.Equal(t, "git", sshUser("ssh://host/r.git"))
}

// TestGitEndToEnd exercises the whole pipeline in-process: a .vinit git block
// clones a local repo in pass 1 and materializes a .vcl into the config
// directory, which pass 2 then parses — the fetched bus must appear in the
// resulting Config.
func TestGitEndToEnd(t *testing.T) {
	src, _ := srcRepo(t, map[string]string{
		"vcl/app.vcl": `bus "fetched" {}`,
		"www/i.html":  "<html>",
	})

	confDir := t.TempDir()
	into := filepath.Join(confDir, "git")
	vinit := `
git "e2e" {
    repo = "` + src + `"
    fetch "vcl" {
        from = "vcl"
        into = "` + into + `"
    }
}`
	require.NoError(t, os.WriteFile(filepath.Join(confDir, "boot.vinit"), []byte(vinit), 0o644))

	cfg, diags := NewConfig().
		WithSources(confDir).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// The fetched file is on disk...
	assert.FileExists(t, filepath.Join(into, "app.vcl"))
	// ...and pass 2 parsed it into the live config.
	require.NotNil(t, cfg)
	_, ok := cfg.Buses["fetched"]
	assert.True(t, ok, "expected fetched bus to be loaded in pass 2")
}
