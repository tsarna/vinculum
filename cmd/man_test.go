package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/schemadoc"
)

// runManCmd runs `vinculum man` with the given arguments and returns its
// stdout, its stderr, and its error.
//
// These tests run against the real schema, unlike the renderer's own, which
// use fixtures: this package blank-imports every subsystem (see plugins.go),
// so the registries are fully populated here, and the point of the command is
// that it answers questions about the actual language.
func runManCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	// Flags bind to package-level variables that cobra does not reset between
	// runs, so restore their declared defaults before each one.
	manType, manNoPager, manConfigs, pluginPath = "", false, nil, ""

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append([]string{"man"}, args...))
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

func TestManRendersATypeVariant(t *testing.T) {
	out, _, err := runManCmd(t, "client", "mqtt")
	require.NoError(t, err)

	assert.Contains(t, out, `# `+"`"+`client "mqtt"`+"`")
	assert.Contains(t, out, "```hcl\nclient \"mqtt\" \"<name>\" {")
	assert.Contains(t, out, "| `brokers` |")

	// Sub-blocks are expanded to full depth: mqtt reaches four levels.
	assert.Contains(t, out, "`receiver \"<name>\"`")
	assert.Contains(t, out, "`subscription \"<mqtt_topic>\"`")
}

func TestManShortFormReachesTheSamePage(t *testing.T) {
	long, _, err := runManCmd(t, "client", "mqtt")
	require.NoError(t, err)
	short, _, err := runManCmd(t, "mqtt")
	require.NoError(t, err)

	assert.Equal(t, long, short, "a bare type label is the same topic")
}

func TestManReachesDeepPaths(t *testing.T) {
	out, _, err := runManCmd(t, "server", "mcp", "tool", "param")
	require.NoError(t, err)

	assert.Contains(t, out, "In `server \"mcp\"` › `tool`.")
}

func TestManRendersAnAttributeWithItsContext(t *testing.T) {
	out, _, err := runManCmd(t, "subscription", "action")
	require.NoError(t, err)

	assert.Contains(t, out, "# `action`")
	assert.Contains(t, out, "In `subscription`.")
	// An attribute's ctx is most of what a reader came for.
	assert.Contains(t, out, "`ctx.topic`")
	assert.Contains(t, out, "`ctx.msg`")
}

func TestManRendersAContextShape(t *testing.T) {
	out, _, err := runManCmd(t, "message")
	require.NoError(t, err)

	assert.Contains(t, out, "`ctx` — message")
	assert.Contains(t, out, "`ctx.trace_id`")
	// The shape's page says which attributes see it.
	assert.Contains(t, out, "**See also**")
	assert.Contains(t, out, "`subscription` › `action`")
}

func TestManIndexListsBlocksAndShapes(t *testing.T) {
	out, _, err := runManCmd(t)
	require.NoError(t, err)

	assert.Contains(t, out, "# Vinculum configuration language")
	assert.Contains(t, out, "## Blocks")
	assert.Contains(t, out, "- `subscription`")
	assert.Contains(t, out, "## Context shapes")
	assert.Contains(t, out, "- `message`")
	assert.Contains(t, out, "vinculum man client mqtt")

	// Type variants belong under their block, not in the index.
	assert.NotContains(t, out, "- `mqtt`")
}

func TestManAmbiguousTopicOffersTheCommandsThatResolveIt(t *testing.T) {
	// The collision that exists in the real schema.
	out, errOut, err := runManCmd(t, "http")

	require.Error(t, err)
	assert.Equal(t, 1, ExitCode(err))
	assert.True(t, Reported(err), "the menu is the explanation; main must not restate it")

	assert.Contains(t, errOut, `"http" is ambiguous, choose one of:`)
	assert.Contains(t, errOut, "vinculum man client http")
	assert.Contains(t, errOut, "vinculum man server http")
	// Redirecting a lookup that turned out ambiguous must not write a menu
	// into the file.
	assert.Empty(t, out)

	// And each of the offered commands resolves.
	for _, argv := range [][]string{{"client", "http"}, {"server", "http"}} {
		page, _, err := runManCmd(t, argv...)
		require.NoError(t, err)
		assert.NotEmpty(t, page)
	}
}

func TestManUnknownTopicSuggestsNearMisses(t *testing.T) {
	out, errOut, err := runManCmd(t, "subscriptions")

	require.Error(t, err)
	assert.Equal(t, 1, ExitCode(err))
	assert.True(t, Reported(err))
	assert.Contains(t, errOut, `no topic named "subscriptions"`)
	assert.Contains(t, errOut, "did you mean:")
	assert.Contains(t, errOut, "vinculum man subscription")
	assert.Empty(t, out)
}

func TestManUnknownTopicWithNoNearMissPointsAtTheIndex(t *testing.T) {
	_, errOut, err := runManCmd(t, "zzzzzzzz")

	require.Error(t, err)
	assert.Equal(t, 1, ExitCode(err))
	assert.Contains(t, errOut, "Run `vinculum man` for a list of topics.")
	assert.NotContains(t, errOut, "did you mean")
}

func TestManTypeRestriction(t *testing.T) {
	_, _, err := runManCmd(t, "--type", "context", "client")
	require.Error(t, err, "a block is not a context shape")

	out, _, err := runManCmd(t, "--type", "block", "client")
	require.NoError(t, err)
	assert.Contains(t, out, "# `client`")
}

func TestManUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"an unknown kind", []string{"--type", "nope", "client"}},
		{"--plugin-path with no config to search", []string{"--plugin-path", "/plugins", "client"}},
		{"--config with no --plugin-path", []string{"--config", "x.vcl", "client"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runManCmd(t, tc.args...)
			require.Error(t, err)
			assert.Equal(t, 2, ExitCode(err), "usage errors exit 2, not 1")
			assert.False(t, Reported(err), "main prints a usage error itself")
		})
	}
}

// Every topic the index offers must render, and every name completion offers
// must resolve. Together these walk the whole document, so a node the walker
// mishandles fails here rather than the first time someone looks it up.
func TestManEveryTopicRenders(t *testing.T) {
	doc, _ := config.GenerateSchema(config.SchemaGenOptions{})

	var walked int
	var walk func(path []string)
	walk = func(path []string) {
		candidates := schemadoc.Resolve(doc, "", path)
		require.NotEmpty(t, candidates, "%v should resolve", path)
		for _, n := range candidates {
			assert.NotPanics(t, func() {
				text := schemadoc.RenderMarkdown(schemadoc.Walk(n, schemadoc.WalkOptions{}), schemadoc.MarkdownOptions{})
				assert.NotEmpty(t, strings.TrimSpace(text), "%v rendered nothing", path)
			}, "%v", path)
			walked++
		}
		for _, m := range schemadoc.Members(doc, "", path) {
			walk(append(append([]string{}, path...), m))
		}
	}

	for _, name := range schemadoc.LeadingNames(doc, "") {
		walk([]string{name})
	}
	// The real schema is 15 blocks, 43 variants, 74 nested blocks and 807
	// attributes; a walk that visited a handful of them would pass every
	// assertion above and prove nothing.
	assert.Greater(t, walked, 800, "the walk should reach the whole document")
	t.Logf("rendered %d topics", walked)
}
