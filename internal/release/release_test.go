package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleaseConsistency is the check itself, run against this tree.
//
// With VINCULUM_RELEASE_VERSION set — which the release workflows do, from the
// tag — it additionally requires the tree to describe that release. Locally it
// checks what must hold at every commit, so a doc page naming a version that
// does not exist fails while the change is being written rather than at tag
// time.
func TestReleaseConsistency(t *testing.T) {
	root := repoRoot(t)

	problems, err := Check(root, os.Getenv("VINCULUM_RELEASE_VERSION"))
	require.NoError(t, err)

	for _, p := range problems {
		t.Errorf("release inconsistency: %s", p)
	}
}

func TestChangelogLinkRefs(t *testing.T) {
	const good = `# Changelog

## [Unreleased]

- something

## [0.44.0] - 2026-07-22

- a thing

## [0.43.0] - 2026-07-18

- an older thing

[Unreleased]: https://github.com/tsarna/vinculum/compare/v0.44.0...HEAD
[0.44.0]: https://github.com/tsarna/vinculum/compare/v0.43.0...v0.44.0
[0.43.0]: https://github.com/tsarna/vinculum/compare/v0.42.0...v0.43.0
`

	t.Run("clean", func(t *testing.T) {
		cl, problems := parseChangelog(good)
		require.Empty(t, problems)
		assert.Equal(t, []string{"0.44.0", "0.43.0"}, cl.released)
		assert.True(t, cl.hasUnreleased)
		assert.Empty(t, checkLinkRefs(cl))
	})

	t.Run("unreleased compares from a stale version", func(t *testing.T) {
		cl, _ := parseChangelog(strings.Replace(good,
			"[Unreleased]: https://github.com/tsarna/vinculum/compare/v0.44.0...HEAD",
			"[Unreleased]: https://github.com/tsarna/vinculum/compare/v0.43.0...HEAD", 1))
		requireProblem(t, checkLinkRefs(cl), "should compare v0.44.0...HEAD")
	})

	t.Run("a section with no link definition", func(t *testing.T) {
		cl, _ := parseChangelog(strings.Replace(good,
			"[0.44.0]: https://github.com/tsarna/vinculum/compare/v0.43.0...v0.44.0\n", "", 1))
		requireProblem(t, checkLinkRefs(cl), "no link definition")
	})

	t.Run("sections out of order", func(t *testing.T) {
		_, problems := parseChangelog(strings.Replace(good, "## [0.43.0] - 2026-07-18", "## [0.45.0] - 2026-07-18", 1))
		requireProblem(t, problems, "newest first")
	})
}

func TestDocVersionNotes(t *testing.T) {
	cl := changelog{released: []string{"0.44.0", "0.43.0"}, dated: map[string]bool{"0.44.0": true}}

	t.Run("a released version is fine", func(t *testing.T) {
		root := docTree(t, map[string]string{"a.md": "> **Changed in 0.44.0.** yes"})
		pending, problems := checkDocVersionNotes(root, cl)
		assert.Empty(t, problems)
		assert.Empty(t, pending)
	})

	t.Run("one unreleased version is the pending release", func(t *testing.T) {
		root := docTree(t, map[string]string{
			"a.md": "> **Changed in 0.45.0.** yes",
			"b.md": "> **Removed in 0.45.0.** also yes",
		})
		pending, problems := checkDocVersionNotes(root, cl)
		assert.Empty(t, problems)
		assert.Equal(t, "0.45.0", pending)
	})

	t.Run("two unreleased versions disagree", func(t *testing.T) {
		root := docTree(t, map[string]string{
			"a.md": "> **Changed in 0.45.0.** yes",
			"b.md": "> **Changed in 0.46.0.** no",
		})
		_, problems := checkDocVersionNotes(root, cl)
		requireProblem(t, problems, "more than one unreleased version")
	})

	t.Run("a version older than the latest release never shipped", func(t *testing.T) {
		root := docTree(t, map[string]string{"a.md": "> **Changed in 0.43.5.** never existed"})
		_, problems := checkDocVersionNotes(root, cl)
		requireProblem(t, problems, "has no CHANGELOG section")
	})
}

func TestPinnedVersions(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "doc"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "testdata", "plugin-smoke"), 0o755))
	write(t, filepath.Join(root, "doc", "schema.md"), `  "vinculumVersion": "0.44.0",`)
	write(t, filepath.Join(root, "testdata", "plugin-smoke", "go.mod"), "require github.com/tsarna/vinculum v0.44.0")

	assert.Empty(t, checkPinnedVersions(root, "0.44.0"))

	problems := checkPinnedVersions(root, "0.45.0")
	assert.Len(t, problems, 2, "both pins are stale")
	requireProblem(t, problems, "pins 0.44.0, latest release is 0.45.0")
}

func TestReleaseVersionChecks(t *testing.T) {
	cl := changelog{
		released: []string{"0.45.0", "0.44.0"},
		dated:    map[string]bool{"0.45.0": true, "0.44.0": true},
	}

	t.Run("tree describes the release", func(t *testing.T) {
		assert.Empty(t, checkReleaseVersion("0.45.0", cl, "0.45.0"))
	})

	t.Run("tagging past the changelog", func(t *testing.T) {
		requireProblem(t, checkReleaseVersion("0.46.0", cl, ""), "rename the [Unreleased] section")
	})

	t.Run("docs promise a different version", func(t *testing.T) {
		requireProblem(t, checkReleaseVersion("0.45.0", cl, "0.46.0"), "pages promise changes in 0.46.0")
	})

	t.Run("a prerelease rehearses the version doc/ promises", func(t *testing.T) {
		unprepared := changelog{released: []string{"0.44.0"}, dated: map[string]bool{"0.44.0": true}}
		assert.Empty(t, checkReleaseVersion("0.45.0-rc1", unprepared, "0.45.0"),
			"an rc may be cut before the changelog section is renamed")
		assert.Empty(t, checkReleaseVersion("0.45.0-rc1", cl, ""),
			"or after")
		requireProblem(t, checkReleaseVersion("0.99.0-rc1", unprepared, "0.45.0"),
			"which neither the newest changelog section")
	})

	t.Run("undated section", func(t *testing.T) {
		undated := cl
		undated.dated = map[string]bool{"0.45.0": false}
		requireProblem(t, checkReleaseVersion("0.45.0", undated, ""), "carries no date")
	})
}

func TestCompareVersions(t *testing.T) {
	assert.Negative(t, compareVersions("0.44.0", "0.45.0"))
	assert.Positive(t, compareVersions("0.45.0", "0.44.9"))
	assert.Zero(t, compareVersions("1.2.3", "1.2.3"))
	assert.Negative(t, compareVersions("0.9.0", "0.10.0"), "compared as numbers, not text")
}

// ---------------------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.HasPrefix(string(mod), "module github.com/tsarna/vinculum\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "reached the filesystem root without finding the repo")
		dir = parent
	}
}

// docTree writes a doc/ directory holding the given files and returns the root.
func docTree(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "doc"), 0o755))
	for name, body := range files {
		write(t, filepath.Join(root, "doc", name), body)
	}
	return root
}

func write(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// requireProblem asserts that some problem mentions want.
func requireProblem(t *testing.T, problems []Problem, want string) {
	t.Helper()

	for _, p := range problems {
		if strings.Contains(p.String(), want) {
			return
		}
	}
	t.Fatalf("no problem mentioning %q; got %v", want, problems)
}
