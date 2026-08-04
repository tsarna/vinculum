package schemadoc

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCatalog is a handful of functions, so the function corpus can be tested
// without booting a runtime to assemble a real eval context.
type fakeCatalog map[string]string

func (f fakeCatalog) FuncNames() []string {
	out := make([]string, 0, len(f))
	for k := range f {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f fakeCatalog) FuncHelp(name string) (string, bool) {
	text, ok := f[name]
	return text, ok
}

func testCatalog() fakeCatalog {
	return fakeCatalog{
		"send":   "send(ctx, subscriber, topic: string) -> bool\n\n  Sends a message.",
		"assert": "assert(condition) -> bool",
		"count":  "count(thing) -> number",
	}
}

func TestResolveFuncs(t *testing.T) {
	cat := testCatalog()

	got := ResolveFuncs(cat, "", []string{"send"})
	require.Len(t, got, 1)
	assert.Equal(t, KindFunction, got[0].Kind)
	assert.Equal(t, []string{"send"}, got[0].Path)

	assert.Empty(t, ResolveFuncs(cat, "", []string{"nope"}))
	// A function name is a whole path: functions have no members.
	assert.Empty(t, ResolveFuncs(cat, "", []string{"send", "ctx"}))
	// A restricted kind excludes the other corpus.
	assert.Empty(t, ResolveFuncs(cat, KindBlock, []string{"send"}))
	assert.Len(t, ResolveFuncs(cat, KindFunction, []string{"send"}), 1)
	// No catalog is not an error — it is a front door with no Config built.
	assert.Empty(t, ResolveFuncs(nil, "", []string{"send"}))
}

// The catalog's text is reproduced exactly. Its alignment carries the calling
// convention, so re-wrapping it would destroy the content.
func TestWalkFunctionEmitsTheHelpVerbatim(t *testing.T) {
	cat := testCatalog()
	events := Walk(FuncNode(cat, "send"), WalkOptions{})

	var pre string
	for _, e := range events {
		if p, ok := e.(Preformatted); ok {
			pre = p.Text
		}
	}
	assert.Equal(t, cat["send"], pre)

	md := RenderMarkdown(events, MarkdownOptions{})
	assert.Contains(t, md, "# `send()`")
	assert.Contains(t, md, "```\nsend(ctx, subscriber, topic: string) -> bool")

	// Through the terminal sink the lines keep their relative indentation.
	term := RenderTerm(events, TermOptions{Width: 40})
	assert.Contains(t, term, "  Sends a message.")
}

func TestFuncLeadingNames(t *testing.T) {
	cat := testCatalog()

	assert.Equal(t, []string{"assert", "count", "send"}, FuncLeadingNames(cat, ""))
	assert.Equal(t, []string{"assert", "count", "send"}, FuncLeadingNames(cat, KindFunction))
	assert.Empty(t, FuncLeadingNames(cat, KindBlock))
	assert.Empty(t, FuncLeadingNames(nil, ""))
}

func TestSuggestFuncs(t *testing.T) {
	cat := testCatalog()

	got := SuggestFuncs(cat, "", []string{"sned"})
	require.NotEmpty(t, got)
	assert.Equal(t, "send", got[0].Path[0])

	// Case differences are a near miss, not a distance.
	got = SuggestFuncs(cat, "", []string{"SEND"})
	require.NotEmpty(t, got)
	assert.Equal(t, "send", got[0].Path[0])

	assert.Empty(t, SuggestFuncs(cat, "", []string{"completely_different"}))
	assert.Empty(t, SuggestFuncs(nil, "", []string{"sned"}))
}

// A menu spanning both corpora names the kind, because the paths are identical
// and only the kind tells them apart. This is the case the whole --type flag
// exists for.
func TestMenuAcrossCorporaNamesTheKind(t *testing.T) {
	doc := testDoc()
	candidates := append(
		Resolve(doc, "", []string{"subscription"}),
		ResolveFuncs(testCatalog(), "", []string{"subscription"})...,
	)
	require.Len(t, candidates, 1, "the fixture has no function named subscription")

	// Build the collision by hand: a block and a function of one name.
	both := append(Resolve(doc, "", []string{"subscription"}), FuncNode(testCatalog(), "subscription"))
	menu := MenuFor([]string{"subscription"}, both, CommandSpeller)

	require.Len(t, menu.Items, 2)
	for _, item := range menu.Items {
		assert.Contains(t, item, "--type", "identical paths need the kind to tell them apart")
	}
	assert.Contains(t, strings.Join(menu.Items, "\n"), "--type block subscription")
	assert.Contains(t, strings.Join(menu.Items, "\n"), "--type function subscription")
}
