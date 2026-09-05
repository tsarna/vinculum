package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every intra-repo Markdown link resolves: the file it names exists, and where
// it carries a #fragment, that fragment is an anchor in the file.
//
// Twelve were broken when this was written, and had been for some time — the
// overview's Redis rows dropped the `-name` that `client "redis_kv" "<name>"`
// slugs to, while doc/config.md's rows for the same sections had it right. That
// is the shape of the whole category: nothing links these pages together but
// hand-written paths, a heading gets reworded, and the reference that pointed at
// it degrades silently. `schema --format markdown --check` covers the generated
// regions and nothing covered the prose around them.
//
// This is the sibling of TestDocPagesExist, which checks the same property for
// the one link the schema generates. Both share anchorsIn.

// linkRe matches a Markdown inline link's target. Reference-style links and
// bare autolinks are not used in doc/, so this does not chase them.
var linkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

// skipDirs are not part of the repository. specs/ and tasks/ are gitignored
// working directories that are present in a developer's checkout and absent in
// CI, and they legitimately reference files that have moved or never existed —
// checking them would fail in one place and not the other.
var skipDirs = map[string]bool{
	".git": true, "specs": true, "tasks": true, "node_modules": true,
}

func TestMarkdownLinksResolve(t *testing.T) {
	root := ".."

	files := markdownFilesUnder(t, root)
	require.NotEmpty(t, files, "no Markdown found; the walk root is probably wrong")

	// One parse per file rather than one per link: CHANGELOG.md alone carries
	// hundreds of links into doc/, and re-reading the target for each is the
	// difference between a fast test and a slow one.
	anchors := map[string]map[string]bool{}
	anchorsFor := func(path string) map[string]bool {
		if a, ok := anchors[path]; ok {
			return a
		}
		data, err := os.ReadFile(path)
		if err != nil {
			anchors[path] = nil
			return nil
		}
		anchors[path] = anchorsIn(string(data))
		return anchors[path]
	}

	for _, file := range files {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		body := string(data)

		for _, m := range linkRe.FindAllStringSubmatchIndex(body, -1) {
			target := body[m[2]:m[3]]
			if isExternal(target) {
				continue
			}

			where := fmt.Sprintf("%s:%d", file, 1+strings.Count(body[:m[0]], "\n"))
			path, fragment, _ := strings.Cut(target, "#")

			dest := file
			if path != "" {
				// A directory is a legitimate target — examples/README.md links
				// to each example by its folder — so this asks whether the path
				// exists rather than whether it is a file.
				dest = filepath.Join(filepath.Dir(file), path)
				if _, err := os.Stat(dest); err != nil {
					assert.Failf(t, "broken link", "%s: link to a path that does not exist: %s", where, target)
					continue
				}
			}
			if fragment == "" || !strings.HasSuffix(dest, ".md") {
				continue
			}
			assert.True(t, anchorsFor(dest)[fragment],
				"%s: link to an anchor that is not in %s: %s", where, filepath.Base(dest), target)
		}
	}
}

// isExternal reports whether a link target leaves the repository, and is
// therefore not this test's to verify — reaching the network would make the
// suite depend on someone else's uptime.
func isExternal(target string) bool {
	return strings.HasPrefix(target, "http://") ||
		strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "mailto:")
}

func markdownFilesUnder(t *testing.T, root string) []string {
	t.Helper()

	var files []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	}))
	return files
}
