package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/internal/schemadoc"
)

// The command corpus, against the real tree.

func TestManRendersACommand(t *testing.T) {
	out, _, err := runManCmd(t, "serve")
	require.NoError(t, err)

	assert.Contains(t, out, "# `vinculum serve`")
	assert.Contains(t, out, "```sh\nvinculum serve [config-files-or-directories...] [flags]\n```")
	assert.Contains(t, out, "`-f, --file-path`")
	assert.Contains(t, out, "[env: VINCULUM_FILE_PATH]", "the page carries what --help does")
	assert.Contains(t, out, "## Global flags")

	// The half of the link a reader of the command needs: what the flag turns on.
	assert.Contains(t, out, "*Functions available only with `--file-path`: `file()`,")
	assert.Contains(t, out, "*Functions available only with `--allow-kill`: `kill()`.*")

	typed, _, err := runManCmd(t, "vinculum", "serve")
	require.NoError(t, err)
	assert.Equal(t, out, typed, "the command as typed names the same page")
}

// `check` is a block type, a ctx shape, and a command, and gets the menu that
// tells them apart — the `assert` case, with one more kind.
func TestManCommandCollidesWithABlock(t *testing.T) {
	_, errOut, err := runManCmd(t, "check")
	require.Error(t, err)
	assert.Contains(t, errOut, "vinculum man --type block check")
	assert.Contains(t, errOut, "vinculum man --type command check")

	out, _, err := runManCmd(t, "--type", "command", "check")
	require.NoError(t, err)
	assert.Contains(t, out, "# `vinculum check`")
}

// The other half: a function a flag switches on says which flag, and on which
// commands — so a config calling file() is not written and then fails to boot.
func TestManFunctionNamesTheFlagItNeeds(t *testing.T) {
	out, _, err := runManCmd(t, "--type", "function", "file")
	require.NoError(t, err)
	assert.Contains(t, out,
		"*Available only when run with `--file-path` (`vinculum check`, `vinculum serve`, `vinculum test`).*")

	out, _, err = runManCmd(t, "kill")
	require.NoError(t, err)
	assert.Contains(t, out, "*Available only when run with `--allow-kill` (`vinculum serve`, `vinculum test`).*",
		"check has no --allow-kill, and must not be offered")

	out, _, err = runManCmd(t, "send")
	require.NoError(t, err)
	assert.NotContains(t, out, "Available only")
}

// Every feature a function needs is enabled by a flag on serve. Without the
// annotation a function's page falls back to naming the feature, which is true
// and tells a reader nothing about what to type.
func TestEveryFunctionFeatureHasAFlag(t *testing.T) {
	cat := schemadoc.BuiltinFuncs()
	require.NotNil(t, cat)

	annotated := map[string]bool{}
	serverCmd.Flags().VisitAll(func(f *pflag.Flag) {
		for _, feature := range f.Annotations[schemadoc.FlagFeatureAnnotation] {
			annotated[feature] = true
		}
	})

	gated := map[string][]string{}
	for _, name := range cat.FuncNames() {
		doc, ok := cat.FuncDoc(name)
		require.True(t, ok, name)
		for _, feature := range doc.Features {
			gated[feature] = append(gated[feature], name)
			assert.True(t, annotated[feature],
				"%s needs feature %q, which no flag on serve is annotated as enabling", name, feature)
		}
	}
	// A probe that found nothing would pass the loop above vacuously.
	assert.Contains(t, gated["readfiles"], "file")
	assert.Contains(t, gated["writefiles"], "filewrite")
	assert.Contains(t, gated["allowkill"], "kill")
}

func TestCommandDocPagesExist(t *testing.T) {
	var found int
	var visit func(c *cobra.Command)
	visit = func(c *cobra.Command) {
		if page := c.Annotations[schemadoc.CommandDocPageAnnotation]; page != "" {
			found++
			_, err := os.Stat(filepath.Join(docDir, page))
			assert.NoError(t, err, "%s: doc page %q does not exist", c.CommandPath(), page)
		}
		for _, sub := range c.Commands() {
			visit(sub)
		}
	}
	visit(rootCmd)
	assert.NotZero(t, found, "no command names a doc page; the check would pass vacuously")
}

func TestManAproposFindsAFlag(t *testing.T) {
	out, _, err := runManCmd(t, "-k", "allow-kill")
	require.NoError(t, err)
	assert.Contains(t, out, "vinculum man serve")
	assert.Contains(t, out, "`--allow-kill`")
	assert.NotContains(t, out, " check", "check has no --allow-kill")

	// check is ambiguous, so its row carries the kind even with no block among
	// the hits: the printed command has to read the row.
	out, _, err = runManCmd(t, "-k", "write-path")
	require.NoError(t, err)
	assert.Contains(t, out, "vinculum man --type command check")

	// Restricting the search to blocks leaves the function out of the hits,
	// not out of the question of whether `assert` alone would read a menu.
	out, _, err = runManCmd(t, "--type", "block", "-k", "assert")
	require.NoError(t, err)
	assert.Contains(t, out, "vinculum man --type block assert")
	assert.NotContains(t, out, "vinculum man assert\n")
}
