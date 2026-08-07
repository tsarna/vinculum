package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/schemadoc"
	"go.uber.org/zap"
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
	manType, manNoPager, manApropos, manConfigs, pluginPath = "", false, false, nil, ""

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
	assert.Contains(t, out, "## Evaluated by")
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
		{"--apropos with nothing to search for", []string{"--apropos"}},
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

// The function corpus, laid out like a block: the calling convention as a
// synopsis, the parameters as a table.
func TestManRendersAFunction(t *testing.T) {
	out, _, err := runManCmd(t, "send")
	require.NoError(t, err)

	assert.Contains(t, out, "# `send()`")
	assert.Contains(t, out, "## Parameters")
	assert.Contains(t, out, "| Attribute | Type | Required | Description |")
	assert.Contains(t, out, "`topic`")

	// The signature is the one help() prints, because both come from functy's
	// own renderer rather than two spellings of it.
	cfg := manTestConfig(t)
	doc, ok := cfg.FuncDoc("send")
	require.True(t, ok)
	require.Len(t, doc.Signatures, 1)
	assert.Contains(t, out, "```hcl\n"+doc.Signatures[0]+"\n```")

	help, ok := cfg.FuncHelp("send")
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(help, doc.Signatures[0]),
		"man and help must not spell the signature differently:\n man: %s\nhelp: %s", doc.Signatures[0], help)
}

// A declared function keeps the signature its extern states, which is the
// whole reason externs exist: cty cannot say "optional leading ctx".
func TestManRendersADeclaredFunction(t *testing.T) {
	out, _, err := runManCmd(t, "get")
	require.NoError(t, err)

	assert.Contains(t, out, "get(ctx?: ctx, thing, fallback?, *args) -> any")
	assert.Contains(t, out, "`ctx?`")
	assert.Contains(t, out, "`*args`")
}

// The headline case for --type: one word naming a block and a function both.
func TestManAmbiguityAcrossBlocksAndFunctions(t *testing.T) {
	// `assert` is a block type and a function in the real language.
	out, errOut, err := runManCmd(t, "assert")

	require.Error(t, err)
	assert.Equal(t, 1, ExitCode(err))
	assert.Contains(t, errOut, `"assert" is ambiguous, choose one of:`)
	assert.Contains(t, errOut, "vinculum man --type block assert")
	assert.Contains(t, errOut, "vinculum man --type function assert")
	assert.Empty(t, out)

	// Both offered commands resolve, to different things.
	block, _, err := runManCmd(t, "--type", "block", "assert")
	require.NoError(t, err)
	fn, _, err := runManCmd(t, "--type", "function", "assert")
	require.NoError(t, err)

	assert.Contains(t, block, "```hcl")
	assert.Contains(t, fn, "# `assert()`")
	assert.NotEqual(t, block, fn)
}

func TestManTypeFunctionExcludesBlocks(t *testing.T) {
	_, _, err := runManCmd(t, "--type", "function", "subscription")
	require.Error(t, err, "subscription is a block, not a function")

	_, _, err = runManCmd(t, "--type", "block", "send")
	require.Error(t, err, "send is a function, not a block")
}

func TestManSuggestsNearMissFunctions(t *testing.T) {
	_, errOut, err := runManCmd(t, "--type", "function", "sendd")

	require.Error(t, err)
	assert.Contains(t, errOut, `no topic named "sendd"`)
	assert.Contains(t, errOut, "vinculum man send")
}

// --apropos, against the real language.

func TestAproposFindsAnAttributeWithoutItsBlock(t *testing.T) {
	out, _, err := runManCmd(t, "-k", "keep_alive")
	require.NoError(t, err)

	assert.Contains(t, out, "vinculum man client mqtt keep_alive")
	// A bare attribute name is exactly what a path cannot resolve, which is why
	// searching for it has to be a different question.
	_, _, err = runManCmd(t, "keep_alive")
	require.Error(t, err)
}

// A reader searching for a word has no reason to know whether the config
// language or the function library owns the answer, so both are searched.
func TestAproposSearchesBothCorpora(t *testing.T) {
	out, _, err := runManCmd(t, "-k", "send")
	require.NoError(t, err)

	assert.Contains(t, out, "vinculum man client mqtt sender", "the block corpus")
	assert.Contains(t, out, "vinculum man send", "the function corpus")
}

// A ctx field is not addressable, so its row names the shape that carries it
// and says which field matched.
func TestAproposNamesTheShapeForAContextField(t *testing.T) {
	out, _, err := runManCmd(t, "-k", "topic_params")
	require.NoError(t, err)

	assert.Contains(t, out, "`ctx.topic_params` —")
	assert.Contains(t, out, "| `vinculum man fsm-hook` |")
}

// Every command an apropos row prints must read that row. A row resolving to
// the ambiguity menu, or to nothing, would be a row that lied.
func TestAproposRowsAreWorkingInvocations(t *testing.T) {
	for _, term := range []string{"topic", "baggage", "tls", "assert"} {
		out, _, err := runManCmd(t, "-k", term)
		require.NoError(t, err, "%q found nothing", term)

		var checked int
		for _, line := range strings.Split(out, "\n") {
			// The Markdown sink puts each command in the first cell.
			if !strings.HasPrefix(line, "| `vinculum man ") {
				continue
			}
			argv := strings.Fields(strings.TrimPrefix(
				strings.Split(line, "`")[1], "vinculum man "))

			_, _, err := runManCmd(t, argv...)
			require.NoError(t, err, "%q: `vinculum man %s` does not resolve",
				term, strings.Join(argv, " "))
			checked++
		}
		require.NotZero(t, checked, "%q printed no rows to check", term)
	}
}

func TestAproposAndsItsTerms(t *testing.T) {
	both, _, err := runManCmd(t, "-k", "baggage", "keys")
	require.NoError(t, err)
	one, _, err := runManCmd(t, "-k", "baggage")
	require.NoError(t, err)

	assert.Less(t, strings.Count(both, "| `vinculum man "),
		strings.Count(one, "| `vinculum man "),
		"a second term must narrow the search, not widen it")
}

func TestAproposRestrictsToAKind(t *testing.T) {
	out, _, err := runManCmd(t, "--type", "context", "-k", "topic")
	require.NoError(t, err)

	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "| `vinculum man ") {
			continue
		}
		// A ctx shape is a one-word path, so nothing from a block body can
		// have survived the filter.
		argv := strings.Fields(strings.Split(line, "`")[1])
		assert.Len(t, argv, 3, "not a context shape: %s", line)
	}
}

func TestAproposFindsNothing(t *testing.T) {
	out, errOut, err := runManCmd(t, "-k", "zzzznope")

	require.Error(t, err)
	assert.Equal(t, 1, ExitCode(err), "a search that found nothing failed, it was not misused")
	assert.True(t, Reported(err))
	assert.Contains(t, errOut, `nothing matches "zzzznope"`)
	assert.Empty(t, out)
}

// manTestConfig builds the same sourceless config funcCatalog does.
func manTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, diags := config.NewConfig().WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	return cfg
}
