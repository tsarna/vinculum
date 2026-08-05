package repl

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/schemadoc"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// runMan drives :man and returns what it wrote.
//
// A bytes.Buffer is not a terminal, so no pager is invoked and no ANSI is
// emitted — the same path a redirected `vinculum man` takes.
func runMan(t *testing.T, h *host, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	h.manTo(&out, args)
	return out.String()
}

func manTestHost(t *testing.T) *host {
	t.Helper()
	return newTestHost(t, `bus "events" {}`)
}

// :man must appear in :help's listing, which the engine builds from Summary.
// Without one it is undiscoverable.
func TestManIsAMetaCommandWithASummary(t *testing.T) {
	h := manTestHost(t)

	for _, m := range h.metaCommands() {
		if m.Names[0] == ":man" {
			assert.NotEmpty(t, m.Summary, ":help lists commands by their Summary")
			assert.NotNil(t, m.Run)
			return
		}
	}
	t.Fatal(":man is not registered")
}

func TestManRendersABlock(t *testing.T) {
	got := runMan(t, manTestHost(t), "subscription")

	assert.Contains(t, got, "subscription")
	assert.Contains(t, got, "Attributes")
	assert.NotContains(t, got, "\x1b[", "not a terminal, so no ANSI")
	assert.NotContains(t, got, "|---|", "the terminal sink, not Markdown")
}

// The REPL's corpus is the live session's own eval context, so a config that
// declared things is documented as it actually is.
func TestManRendersAFunction(t *testing.T) {
	h := manTestHost(t)
	got := runMan(t, h, "send")

	doc, ok := h.cfg.FuncDoc("send")
	require.True(t, ok)
	require.Len(t, doc.Signatures, 1)

	assert.Contains(t, got, doc.Signatures[0])
	assert.Contains(t, got, "Parameters")
	assert.Contains(t, got, "topic")
}

func TestManWithNoArgumentsListsTopics(t *testing.T) {
	got := runMan(t, manTestHost(t))

	assert.Contains(t, got, "Blocks")
	assert.Contains(t, got, "subscription")
	// The footer is in this front door's idiom, not the shell command's.
	assert.Contains(t, got, ":man client mqtt")
	assert.NotContains(t, got, "vinculum man")
}

// The menu has to be typeable where it is printed.
func TestManAmbiguityOffersMetaCommands(t *testing.T) {
	got := runMan(t, manTestHost(t), "http")

	assert.Contains(t, got, `"http" is ambiguous`)
	assert.Contains(t, got, ":man client http")
	assert.Contains(t, got, ":man server http")
	assert.NotContains(t, got, "vinculum man")
}

func TestManAmbiguityAcrossKindsUsesThePrefix(t *testing.T) {
	// `assert` is a block type and a function both.
	got := runMan(t, manTestHost(t), "assert")

	assert.Contains(t, got, `"assert" is ambiguous`)
	assert.Contains(t, got, ":man block:assert")
	assert.Contains(t, got, ":man function:assert")

	// And each resolves to something different.
	h := manTestHost(t)
	block := runMan(t, h, "block:assert")
	fn := runMan(t, h, "function:assert")
	assert.Contains(t, block, "condition")
	assert.Contains(t, fn, "assert(")
	assert.NotEqual(t, block, fn)
}

// The REPL is where this actually bites: its corpus is the live session's eval
// context, so a project whose own .cty files declare one name in two namespaces
// gets the candidates rather than "no topic named dup".
func TestManReportsAFunctionAmbiguousAcrossNamespaces(t *testing.T) {
	cfg, diags := config.NewConfig().
		WithSources("../config/testdata/funcambig").
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	h := newHost(cfg, NewInteractiveLogging(zapcore.InfoLevel))

	got := runMan(t, h, "dup")
	assert.Contains(t, got, `"dup" is a function in more than one namespace`)
	assert.Contains(t, got, ":man alpha::dup")
	assert.Contains(t, got, ":man beta::dup")
	assert.NotContains(t, got, "no topic named", "ambiguous is not absent")

	// Each qualified name resolves, and a bare name in one namespace still does.
	assert.Contains(t, runMan(t, h, "alpha::dup"), "Alpha's own dup.")
	assert.Contains(t, runMan(t, h, "solo"), "Only in alpha.")
}

func TestManNotFound(t *testing.T) {
	h := manTestHost(t)

	got := runMan(t, h, "subscriptions")
	assert.Contains(t, got, `no topic named "subscriptions"`)
	assert.Contains(t, got, "did you mean:")
	assert.Contains(t, got, ":man subscription")

	got = runMan(t, h, "zzzzzzzz")
	assert.Contains(t, got, "Type :man for a list of topics.")
	assert.NotContains(t, got, "did you mean")
}

func TestManReportsABadKind(t *testing.T) {
	got := runMan(t, manTestHost(t), "widget:http")

	assert.Contains(t, got, `unknown kind "widget"`)
	assert.Contains(t, got, "block, context, function")
}

func TestParseManArgs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		kind    schemadoc.Kind
		path    []string
		wantErr string
	}{
		{name: "nothing"},
		{name: "a bare topic", args: []string{"subscription"}, path: []string{"subscription"}},
		{name: "a path", args: []string{"client", "mqtt"}, path: []string{"client", "mqtt"}},
		{
			name: "a kind prefix", args: []string{"block:assert"},
			kind: schemadoc.KindBlock, path: []string{"assert"},
		},
		{
			name: "a kind prefix on a path", args: []string{"block:client", "mqtt"},
			kind: schemadoc.KindBlock, path: []string{"client", "mqtt"},
		},
		// A functy qualified name is one word and must survive whole.
		{name: "a qualified name", args: []string{"time::now"}, path: []string{"time::now"}},
		{name: "an unknown kind", args: []string{"widget:x"}, wantErr: `unknown kind "widget"`},
		{name: "a kind with no topic", args: []string{"block:"}, wantErr: "names a kind but no topic"},
		// The engine splits on spaces; empties should not become path elements.
		{name: "stray blanks", args: []string{"", "subscription", " "}, path: []string{"subscription"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, path, err := parseManArgs(tc.args)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.kind, kind)
			assert.Equal(t, tc.path, path)
		})
	}
}

func TestManSpeller(t *testing.T) {
	assert.Equal(t, ":man subscription", ManSpeller(schemadoc.KindBlock, []string{"subscription"}, false))
	assert.Equal(t, ":man client mqtt", ManSpeller(schemadoc.KindBlock, []string{"client", "mqtt"}, false))
	assert.Equal(t, ":man block:assert", ManSpeller(schemadoc.KindBlock, []string{"assert"}, true))
	assert.Equal(t, ":man function:assert", ManSpeller(schemadoc.KindFunction, []string{"assert"}, true))
}

// Everything :man prints goes to stdout, including diagnostics: doc/repl.md
// suggests running the REPL with 2>vinculum.log, which would otherwise swallow
// the answer to a mistyped topic.
func TestManWritesDiagnosticsToTheSameStreamAsPages(t *testing.T) {
	h := manTestHost(t)

	var errOut bytes.Buffer
	// cmdMan is handed the engine's stderr and must ignore it.
	exit := h.cmdMan([]string{"zzzzzzzz"}, &errOut)

	assert.False(t, exit, ":man never ends the session")
	assert.Empty(t, errOut.String(), "nothing may go to the engine's stderr")
}
