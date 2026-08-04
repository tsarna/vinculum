package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTemp writes a document to a temporary file and returns its path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

const regionDoc = `# A page

Hand-written prose above.

### ` + "`client`" + `

<!-- vinculum:begin block-index client level=3 -->
stale
<!-- vinculum:end block-index client -->

Hand-written prose below.
`

func TestSchemaMarkdownWholeLanguage(t *testing.T) {
	out, err := runSchemaCommand(t, "--format", "markdown")
	require.NoError(t, err)

	assert.Contains(t, out, "# Vinculum configuration language")
	assert.Contains(t, out, "`client \"mqtt\"`")
	assert.Contains(t, out, "`ctx` — message")
	assert.NotContains(t, out, `"schemaVersion"`, "markdown, not JSON")
}

func TestSchemaMarkdownUpdateRewritesOnlyTheRegions(t *testing.T) {
	path := writeTemp(t, "page.md", regionDoc)

	_, err := runSchemaCommand(t, "--format", "markdown", "--update", path)
	require.NoError(t, err)

	got := read(t, path)
	assert.Contains(t, got, "Hand-written prose above.")
	assert.Contains(t, got, "Hand-written prose below.")
	assert.Contains(t, got, "### `client`")
	assert.NotContains(t, got, "stale")
	assert.Contains(t, got, "[`client \"mqtt\"`](client-mqtt.md)")
	assert.Contains(t, got, "<!-- vinculum:end block-index client -->")
}

func TestSchemaMarkdownCheckReportsAndDoesNotWrite(t *testing.T) {
	path := writeTemp(t, "page.md", regionDoc)

	_, err := runSchemaCommand(t, "--format", "markdown", "--check", path)
	require.Error(t, err)
	assert.Equal(t, 1, ExitCode(err), "a stale region is a failure, not a usage error")
	assert.True(t, Reported(err), "the listing is the explanation")

	assert.Equal(t, regionDoc, read(t, path), "--check must not write")
}

// The loop CI depends on: update, then check passes; and running update again
// changes nothing. Without idempotence, --check could not tell a stale
// document from a non-deterministic generator.
func TestSchemaMarkdownUpdateThenCheckIsClean(t *testing.T) {
	path := writeTemp(t, "page.md", regionDoc)

	_, err := runSchemaCommand(t, "--format", "markdown", "--update", path)
	require.NoError(t, err)
	afterFirst := read(t, path)

	_, err = runSchemaCommand(t, "--format", "markdown", "--check", path)
	assert.NoError(t, err, "a freshly updated document is current")

	_, err = runSchemaCommand(t, "--format", "markdown", "--update", path)
	require.NoError(t, err)
	assert.Equal(t, afterFirst, read(t, path), "a second update changes nothing")
}

func TestSchemaMarkdownLeavesAPageWithNoRegionsAlone(t *testing.T) {
	const plain = "# No markers here\n\nJust prose.\n"
	path := writeTemp(t, "plain.md", plain)

	_, err := runSchemaCommand(t, "--format", "markdown", "--update", path)
	require.NoError(t, err)
	assert.Equal(t, plain, read(t, path))
}

func TestSchemaMarkdownWalksDirectories(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte(regionDoc), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "b.md"), []byte(regionDoc), 0o644))
	// Not Markdown, so not touched.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.txt"), []byte(regionDoc), 0o644))

	_, err := runSchemaCommand(t, "--format", "markdown", "--update", dir)
	require.NoError(t, err)

	assert.Contains(t, read(t, filepath.Join(dir, "a.md")), "client-mqtt.md")
	assert.Contains(t, read(t, filepath.Join(nested, "b.md")), "client-mqtt.md")
	assert.Equal(t, regionDoc, read(t, filepath.Join(dir, "c.txt")))
}

// A malformed marker stops the run rather than being worked around: the
// alternative is silently swallowing or duplicating hand-written prose.
func TestSchemaMarkdownMalformedMarkersAreUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"never ended", "<!-- vinculum:begin block-index client -->\n", "is never ended"},
		{"never began", "<!-- vinculum:end block-index client -->\n", "never began"},
		{"unknown kind", "<!-- vinculum:begin nope x -->\n<!-- vinculum:end nope x -->\n", "unknown region kind"},
		{
			"an ambiguous topic",
			"<!-- vinculum:begin block-body http -->\n<!-- vinculum:end block-body http -->\n",
			"resolves to 2 topics",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, "bad.md", tc.doc)
			_, err := runSchemaCommand(t, "--format", "markdown", "--update", path)
			require.Error(t, err)
			assert.Equal(t, 2, ExitCode(err))
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "bad.md", "the report names the file")
			assert.Equal(t, tc.doc, read(t, path), "a document that cannot be generated is not half-written")
		})
	}
}

func TestSchemaMarkdownUsageErrors(t *testing.T) {
	path := writeTemp(t, "page.md", regionDoc)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"--update without markdown", []string{"--update", path}},
		{"--check without markdown", []string{"--check", path}},
		{"both at once", []string{"--format", "markdown", "--update", path, "--check", path}},
		{"an unknown format", []string{"--format", "roff"}},
		{"a path that does not exist", []string{"--format", "markdown", "--update", filepath.Join(t.TempDir(), "nope.md")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSchemaCommand(t, tc.args...)
			require.Error(t, err)
			assert.Equal(t, 2, ExitCode(err))
		})
	}
}

// doc/ must stay current, so that the reference and the schema cannot drift.
// This is the same invariant the CI step asserts, run here so a local `go
// test` catches it before a push does.
func TestDocRegionsAreCurrent(t *testing.T) {
	_, err := runSchemaCommand(t, "--format", "markdown", "--check", docDir)
	if err != nil {
		t.Fatalf("%v\n\nRun: go run . schema --format markdown --update doc/", err)
	}
}

func TestMarkdownFilesExpansion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.md"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.md"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skip.txt"), nil, 0o644))

	got, err := markdownFiles([]string{dir, filepath.Join(dir, "a.md")})
	require.NoError(t, err)

	// Sorted, and named twice only once.
	require.Len(t, got, 2)
	assert.True(t, strings.HasSuffix(got[0], "a.md"))
	assert.True(t, strings.HasSuffix(got[1], "b.md"))
}
