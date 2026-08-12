package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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

// The top-level schema used to be consumed with PartialContent and the
// remainder thrown away, so a mistyped block header or a stray attribute was a
// config that did nothing, forever, with no signal.
func TestTopLevelSchemaIsClosed(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	tests := []struct {
		name    string
		src     string
		expects []string
	}{
		{
			name:    "misspelled block type",
			src:     "serverr \"http\" \"web\" {\n  listen = \":8080\"\n}\n",
			expects: []string{"Unsupported block type", "serverr", "Did you mean \"server\"?"},
		},
		{
			name:    "stray top-level attribute",
			src:     "whatever = 42\n",
			expects: []string{"Unsupported argument", "whatever"},
		},
		{
			name:    "misspelled function-definition block",
			src:     "functon \"f\" {\n  params = [a]\n  result = a\n}\n",
			expects: []string{"Unsupported block type", "functon", "Did you mean \"function\"?"},
		},
		{
			name:    "block type written as an attribute",
			src:     "bus = \"main\"\n",
			expects: []string{"Unsupported argument", "Did you mean to define a block of type \"bus\"?"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, diags := NewConfig().
				WithSources([]byte(tt.src)).
				WithLogger(logger).
				Build()

			require.True(t, diags.HasErrors(), "unknown top-level content must be reported")
			text := diagsText(diags)
			for _, want := range tt.expects {
				assert.Contains(t, text, want)
			}
		})
	}
}

// The blocks that are extracted before the general block pass (function, jq,
// editor, procedure) are hidden from the closed schema by their own extraction,
// so they must not be reported as unknown. (No editor block here: the editor
// types register from the editors package, which config does not import.)
func TestTopLevelSchemaAcceptsExtractedBlocks(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	src := `
function "double" {
  params = [n]
  result = n * 2
}

jq "field" {
  query = ".field"
}

procedure "noop" {
  return = 1
}

bus "main" {}
`

	_, diags := NewConfig().WithSources([]byte(src)).WithLogger(logger).Build()
	assert.False(t, diags.HasErrors(), "%v", diags)
}

func diagsText(diags hcl.Diagnostics) string {
	var sb strings.Builder
	for _, d := range diags {
		sb.WriteString(d.Summary)
		sb.WriteString(": ")
		sb.WriteString(d.Detail)
		sb.WriteString("\n")
	}
	return sb.String()
}
