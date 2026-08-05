package schemadoc

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

// fakeCatalog is a handful of functions, so the function corpus can be tested
// without booting a runtime to assemble a real eval context.
type fakeCatalog struct {
	docs      map[string]config.FuncDoc
	ambiguous map[string][]string
}

func (f fakeCatalog) FuncNames() []string {
	out := make([]string, 0, len(f.docs))
	for k := range f.docs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f fakeCatalog) FuncDoc(name string) (config.FuncDoc, bool) {
	d, ok := f.docs[name]
	return d, ok
}

func (f fakeCatalog) FuncNameCandidates(name string) []string { return f.ambiguous[name] }

func testCatalog() fakeCatalog {
	return fakeCatalog{
		docs: map[string]config.FuncDoc{
			"send": {
				Name:       "send",
				Signatures: []string{"send(ctx, subscriber, topic: string) -> bool"},
				Doc:        "Sends a message.",
				Params: []config.FuncParam{
					{Name: "ctx", Doc: "the context", Required: true},
					{Name: "topic", Type: "string", Doc: "where to send it", Required: true},
					{Name: "*fields", Doc: "extra fields"},
				},
			},
			// An overload set: two calling conventions, not one with optional
			// parameters.
			"parsetime": {
				Name: "parsetime",
				Signatures: []string{
					"parsetime(s: string) -> time",
					"parsetime(format: string, s: string) -> time",
				},
				Doc:    "Reads a timestamp.",
				Params: []config.FuncParam{{Name: "s", Type: "string", Required: true}},
			},
			"assert": {Name: "assert", Signatures: []string{"assert(condition) -> bool"}},
			"count":  {Name: "count", Signatures: []string{"count(thing) -> number"}},
		},
		ambiguous: map[string][]string{"dup": {"a::dup", "b::dup"}},
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

// A function is laid out like a block: the calling convention as a synopsis,
// the description as prose, the parameters as a table.
func TestWalkFunctionIsStructured(t *testing.T) {
	events := Walk(FuncNode(testCatalog(), "send"), WalkOptions{})

	md := RenderMarkdown(events, MarkdownOptions{})
	assert.Contains(t, md, "# `send()`")
	assert.Contains(t, md, "```hcl\nsend(ctx, subscriber, topic: string) -> bool\n```")
	assert.Contains(t, md, "Sends a message.")
	assert.Contains(t, md, "## Parameters")
	// A real table, not a fenced block — which is the point of the change.
	assert.Contains(t, md, "| Attribute | Type | Required | Description |")
	assert.Contains(t, md, "| `topic` | string | yes | where to send it |")
	assert.Contains(t, md, "| `*fields` |  |  | extra fields |")
	assert.NotContains(t, md, "```\nsend", "the signature is hcl-fenced, not preformatted")
}

// Every form of an overload set gets a line: they are distinct calling
// conventions, and rendering only the first would hide one.
func TestWalkFunctionRendersEveryOverloadForm(t *testing.T) {
	md := RenderMarkdown(Walk(FuncNode(testCatalog(), "parsetime"), WalkOptions{}), MarkdownOptions{})

	assert.Contains(t, md, "parsetime(s: string) -> time\nparsetime(format: string, s: string) -> time")
}

// The parameter descriptions re-wrap, which the fixed-width block help()
// returns cannot do — the reason for rendering structurally at all.
func TestWalkFunctionParametersWrapToTheWidth(t *testing.T) {
	cat := testCatalog()
	cat.docs["wide"] = config.FuncDoc{
		Name:       "wide",
		Signatures: []string{"wide(a) -> any"},
		Params: []config.FuncParam{{
			Name: "a",
			Doc:  "A description long enough that it cannot possibly fit on one line of a narrow terminal without being wrapped.",
		}},
	}

	term := RenderTerm(Walk(FuncNode(cat, "wide"), WalkOptions{}), TermOptions{Width: 50})
	for _, line := range strings.Split(term, "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 50, "line overflows the width: %q", line)
	}
}

// A function parameter may carry no type annotation, where a block attribute
// always has one — so the terminal sink's trailing parenthetical must not lead
// with an empty element.
func TestWalkFunctionUntypedParameterHasNoEmptyQualifier(t *testing.T) {
	cat := testCatalog()
	cat.docs["untyped"] = config.FuncDoc{
		Name:       "untyped",
		Signatures: []string{"untyped(x) -> any"},
		Params:     []config.FuncParam{{Name: "x", Doc: "anything", Required: true}},
	}

	term := RenderTerm(Walk(FuncNode(cat, "untyped"), WalkOptions{}), TermOptions{Width: 80})
	assert.Contains(t, term, "(required)")
	assert.NotContains(t, term, "(, required)")
}

// A default has no column of its own, so it is folded into the description
// rather than dropped.
func TestWalkFunctionShowsParameterDefaults(t *testing.T) {
	cat := testCatalog()
	cat.docs["withdefault"] = config.FuncDoc{
		Name:       "withdefault",
		Signatures: []string{"withdefault(a = 1) -> any"},
		Params: []config.FuncParam{
			{Name: "a", Default: "1", Doc: "How many"},
			{Name: "b", Default: "null"},
		},
	}

	md := RenderMarkdown(Walk(FuncNode(cat, "withdefault"), WalkOptions{}), MarkdownOptions{})
	assert.Contains(t, md, "How many. Defaults to `1`.")
	assert.Contains(t, md, "Defaults to `null`.")
}

func TestFuncLeadingNames(t *testing.T) {
	cat := testCatalog()

	assert.Equal(t, []string{"assert", "count", "parsetime", "send"}, FuncLeadingNames(cat, ""))
	assert.Equal(t, []string{"assert", "count", "parsetime", "send"}, FuncLeadingNames(cat, KindFunction))
	assert.Empty(t, FuncLeadingNames(cat, KindBlock))
	assert.Empty(t, FuncLeadingNames(nil, ""))
}

// A bare name declared in two namespaces resolves to nothing, exactly as a
// misspelling does. Only the candidates tell them apart — which is the whole
// reason the catalog reports them.
func TestAmbiguousFuncName(t *testing.T) {
	cat := testCatalog()

	got := AmbiguousFuncName(cat, "", []string{"dup"})
	assert.Equal(t, []string{"a::dup", "b::dup"}, got)

	// A name that resolves is not ambiguous, and neither is one that is absent.
	assert.Empty(t, AmbiguousFuncName(cat, "", []string{"send"}))
	assert.Empty(t, AmbiguousFuncName(cat, "", []string{"nope"}))
	// Not a function question at all.
	assert.Empty(t, AmbiguousFuncName(cat, KindBlock, []string{"dup"}))
	assert.Empty(t, AmbiguousFuncName(cat, "", []string{"a", "b"}))
	assert.Empty(t, AmbiguousFuncName(nil, "", []string{"dup"}))

	menu := AmbiguousFuncMenu([]string{"dup"}, got, CommandSpeller)
	assert.Equal(t, `"dup" is a function in more than one namespace, choose one of:`, menu.Intro)
	assert.Equal(t, []string{"vinculum man a::dup", "vinculum man b::dup"}, menu.Items)
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
