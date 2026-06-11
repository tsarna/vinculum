//go:build integration && (linux || darwin || freebsd)

package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// TestGitFetch_RealBinary builds a real (non-test) vinculum binary, stands up a
// local source git repository, and runs `vinculum check` against a config whose
// .vinit git block fetches a subtree of that repo into the config directory.
// The fetched .vcl must be discovered and parsed in pass 2 — proving the whole
// bootstrap-fetch pipeline end to end through the production binary.
//
// Run with: go test -tags integration ./config/...
func TestGitFetch_RealBinary(t *testing.T) {
	tmp := t.TempDir()

	binPath := filepath.Join(tmp, "vinculum")
	buildBin := exec.Command("go", "build", "-o", binPath, "github.com/tsarna/vinculum")
	buildBin.Stderr = os.Stderr
	require.NoError(t, buildBin.Run(), "go build of host binary failed")

	// A local source repo with a .vcl under vcl/ and an asset under www/.
	srcRepo := filepath.Join(tmp, "src")
	require.NoError(t, os.MkdirAll(srcRepo, 0o755))
	repo, err := git.PlainInit(srcRepo, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	writeRepoFile(t, srcRepo, "vcl/app.vcl", `assert "git_fetch_works" {
    condition = 1 + 1 == 2
}`)
	writeRepoFile(t, srcRepo, "www/index.html", "<html>fetched</html>")
	_, err = wt.Add(".")
	require.NoError(t, err)
	_, err = wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Config dir with a .vinit git block fetching vcl/ into conf/git/.
	configDir := filepath.Join(tmp, "conf")
	into := filepath.Join(configDir, "git")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	vinit := `
git "fixture" {
    repo = "` + srcRepo + `"
    fetch "vcl" {
        from = "vcl"
        into = "` + into + `"
    }
}`
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "boot.vinit"), []byte(vinit), 0o644))

	check := exec.Command(binPath, "check", configDir)
	out, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("vinculum check failed: %v\noutput:\n%s", err, out)
	}

	// The fetched .vcl must be on disk after the run.
	require.FileExists(t, filepath.Join(into, "app.vcl"))
}

func writeRepoFile(t *testing.T, repoDir, name, content string) {
	t.Helper()
	full := filepath.Join(repoDir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}
