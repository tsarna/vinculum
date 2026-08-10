package schemadoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveNamespace covers the two things namespace resolution does that
// context resolution does not: it descends into members, and it stops where the
// names stop being the language's to know.
func TestResolveNamespace(t *testing.T) {
	doc := testDoc()

	t.Run("the root", func(t *testing.T) {
		got := Resolve(doc, "", []string{"sys"})
		require.Len(t, got, 1)
		assert.Equal(t, KindNamespace, got[0].Kind)
		assert.Equal(t, "`sys`", got[0].Title())
	})

	t.Run("a member", func(t *testing.T) {
		got := Resolve(doc, "", []string{"sys", "hostname"})
		require.Len(t, got, 1)
		// The whole dotted path: `hostname` alone would not say what it belongs to.
		assert.Equal(t, "`sys.hostname`", got[0].Title())
		assert.Equal(t, "Hostname of the machine.", got[0].Summary())
	})

	t.Run("a member of a member", func(t *testing.T) {
		got := Resolve(doc, "", []string{"sys", "functy", "version"})
		require.Len(t, got, 1)
		assert.Equal(t, "`sys.functy.version`", got[0].Title())
	})

	t.Run("a described member of a free one", func(t *testing.T) {
		// sys.signals carries whichever signals the host defines, but bynumber
		// is fixed and is therefore a page.
		got := Resolve(doc, "", []string{"sys", "signals", "bynumber"})
		require.Len(t, got, 1)
		assert.Equal(t, "`sys.signals.bynumber`", got[0].Title())
	})

	t.Run("a free member is not a topic", func(t *testing.T) {
		assert.Empty(t, Resolve(doc, "", []string{"sys", "signals", "SIGUSR1"}))
		assert.Empty(t, Resolve(doc, "", []string{"env", "HOME"}))
	})

	t.Run("a member that does not exist", func(t *testing.T) {
		assert.Empty(t, Resolve(doc, "", []string{"sys", "hostnam"}))
	})

	t.Run("--type narrows to it", func(t *testing.T) {
		assert.Len(t, Resolve(doc, KindNamespace, []string{"sys"}), 1)
		assert.Empty(t, Resolve(doc, KindBlock, []string{"sys"}))
	})
}

// TestBlockNamespacesAreNotTopics pins the one deliberate omission. Every block
// namespace shares its name with the block that fills it, so resolving them
// would make `vinculum man subscription` an ambiguity menu — for a page that
// could only repeat what the block's page says.
func TestBlockNamespacesAreNotTopics(t *testing.T) {
	doc := testDoc()
	require.Contains(t, doc.Namespaces, "subscription", "the fixture must carry one to prove this")

	got := Resolve(doc, "", []string{"subscription"})
	require.Len(t, got, 1)
	assert.Equal(t, KindBlock, got[0].Kind, "the block, with no ambiguity to resolve")

	assert.Empty(t, Resolve(doc, KindNamespace, []string{"subscription"}))
	assert.NotContains(t, LeadingNames(doc, KindNamespace), "subscription")

	var names []string
	for _, n := range Topics(doc, KindNamespace) {
		names = append(names, n.Path[0])
	}
	assert.Equal(t, []string{"env", "http_status", "sys"}, names)
}

// TestNamespaceCompletion covers Members, which is completion's half of
// resolution: what it offers must be exactly what Resolve would accept next.
func TestNamespaceCompletion(t *testing.T) {
	doc := testDoc()

	assert.Equal(t, []string{"functy", "hostname", "signals"}, Members(doc, "", []string{"sys"}))
	assert.Equal(t, []string{"version"}, Members(doc, "", []string{"sys", "functy"}))
	// A free namespace offers nothing, because nothing below it is nameable in
	// advance — not because it has no members.
	assert.Empty(t, Members(doc, "", []string{"env"}))
	// A partly-free member offers the part that is fixed.
	assert.Equal(t, []string{"bynumber"}, Members(doc, "", []string{"sys", "signals"}))

	assert.Subset(t, LeadingNames(doc, ""), []string{"env", "http_status", "sys"})
}

// TestNamespaceSuggests covers the "did you mean" path, which searches exactly
// the names Resolve accepts, so a suggestion always resolves.
func TestNamespaceSuggests(t *testing.T) {
	doc := testDoc()
	got := Suggest(doc, "", []string{"sy"})
	require.NotEmpty(t, got)
	assert.Equal(t, []string{"sys"}, got[0].Path)
}

// TestWalkNamespace covers what a namespace page says.
func TestWalkNamespace(t *testing.T) {
	doc := testDoc()
	n := Resolve(doc, "", []string{"sys"})[0]
	out := renderNode(n, WalkOptions{})

	assert.Contains(t, out, "# `sys`")
	assert.Contains(t, out, "Process and host identity.")
	assert.Contains(t, out, "Captured once at startup.")
	assert.Contains(t, out, "Readable as `sys.<name>`:")
	assert.Contains(t, out, "| `sys.hostname` | string | Hostname of the machine. |")
	// A member with members of its own says so, so a reader knows the path
	// continues.
	assert.Contains(t, out, "*(has members of its own)*")
	// The hand-written page, as every type gets.
	assert.Contains(t, out, "[config.md#variables](config.md#variables)")
	// No value column: sys describes the machine, not the language.
	assert.NotContains(t, out, "| Value |")
}

// TestWalkFreeNamespace covers the namespace whose members are not the
// language's to know: it must say so rather than render as empty, which would
// read as "there is nothing here".
func TestWalkFreeNamespace(t *testing.T) {
	out := renderNode(Resolve(testDoc(), "", []string{"env"})[0], WalkOptions{})

	assert.Contains(t, out, "Environment variables of the running process.")
	assert.Contains(t, out, "Any name may be read here")
	assert.NotContains(t, out, "| Name | Type |")
}

// TestWalkConstantNamespace covers the value column, which is the point of a
// namespace whose values are part of the language.
func TestWalkConstantNamespace(t *testing.T) {
	out := renderNode(Resolve(testDoc(), "", []string{"http_status"})[0], WalkOptions{})

	assert.Contains(t, out, "| Name | Type | Value | Description |")
	assert.Contains(t, out, "| `http_status.NotFound` | number | `404` |")
}

// TestWalkMember covers a member page, including that its documentation is
// rendered once rather than twice — the header row carries no detail of its
// own, because the prose below it is the same text.
func TestWalkMember(t *testing.T) {
	doc := testDoc()

	out := renderNode(Resolve(doc, "", []string{"sys", "functy", "version"})[0], WalkOptions{})
	assert.Contains(t, out, "# `sys.functy.version`")
	assert.Equal(t, 1, strings.Count(out, "Read from build info."), "rendered twice")

	// An object member lists what may follow its own dot.
	out = renderNode(Resolve(doc, "", []string{"sys", "signals"})[0], WalkOptions{})
	assert.Contains(t, out, "## Members")
	assert.Contains(t, out, "Readable as `sys.signals.<name>`:")
	assert.Contains(t, out, "`sys.signals.bynumber`")
	// Free below this point, and the page has to say so — but only for the
	// members it lists, not for the header row above them.
	assert.Equal(t, 1, strings.Count(out, "Any name may be read here"))
}

// TestNamespaceTerminalSink covers the other sink, which nothing enforces
// exhaustiveness over: an unhandled event is silently dropped.
func TestNamespaceTerminalSink(t *testing.T) {
	doc := testDoc()
	out := RenderTerm(Walk(Resolve(doc, "", []string{"sys"})[0], WalkOptions{}), TermOptions{Width: 80})

	assert.Contains(t, out, "sys.hostname")
	assert.Contains(t, out, "Hostname of the machine.")
	assert.Contains(t, out, "Readable as sys.<name>:")

	out = RenderTerm(Walk(Resolve(doc, "", []string{"http_status"})[0], WalkOptions{}), TermOptions{Width: 80})
	assert.Contains(t, out, "404", "a constant namespace shows its values")
}

// TestNamespaceIndex covers the front page, which is where a reader who does
// not know `sys` exists would find out.
func TestNamespaceIndex(t *testing.T) {
	out := RenderMarkdown(Index(testDoc(), WalkOptions{}), MarkdownOptions{})

	assert.Contains(t, out, "## Namespaces")
	assert.Contains(t, out, "- `sys` — Process and host identity.")
	assert.Contains(t, out, "- `env` — Environment variables of the running process.")
	assert.NotContains(t, out, "- `subscription` — Each subscription, by name.")
}

// TestAproposFindsMembers covers the search corpus. A member is addressable, so
// unlike a `ctx` field its hit is its own path rather than its container's.
func TestAproposFindsMembers(t *testing.T) {
	doc := testDoc()

	hits := Apropos(doc, nil, "", []string{"hostname"})
	require.Len(t, hits, 1)
	assert.Equal(t, KindNamespace, hits[0].Kind)
	assert.Equal(t, []string{"sys", "hostname"}, hits[0].Path)
	assert.Empty(t, hits[0].Detail, "a member is addressable in its own right")

	// Nested members are reached too.
	hits = Apropos(doc, nil, "", []string{"bynumber"})
	require.Len(t, hits, 1)
	assert.Equal(t, []string{"sys", "signals", "bynumber"}, hits[0].Path)

	// --type narrows the corpus.
	assert.NotEmpty(t, Apropos(doc, nil, KindNamespace, []string{"identity"}))
	assert.Empty(t, Apropos(doc, nil, KindBlock, []string{"identity"}))
}
