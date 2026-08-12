package schemadoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRegions(t *testing.T) {
	src := strings.Join([]string{
		"# Title",
		"",
		"<!-- vinculum:begin block-index client level=3 -->",
		"generated",
		"<!-- vinculum:end block-index client -->",
		"",
		"<!--vinculum:begin context message-->",
		"<!--vinculum:end context message-->",
	}, "\n")

	got, err := ParseRegions(src)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, RegionBlockIndex, got[0].Kind)
	assert.Equal(t, []string{"client"}, got[0].Args)
	assert.Equal(t, 3, got[0].Level)

	// Whitespace inside the comment is optional, and level defaults.
	assert.Equal(t, RegionContext, got[1].Kind)
	assert.Equal(t, 2, got[1].Level)
}

// Every malformed pair is an error rather than a best-effort recovery: each
// one would otherwise silently swallow or duplicate part of a hand-written
// page, which is a far worse outcome than refusing to run.
func TestParseRegionsRejectsMalformedMarkers(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{
			"a region that never ends",
			"<!-- vinculum:begin block-index client -->\ntext\n",
			"is never ended",
		},
		{
			"an end with no begin",
			"text\n<!-- vinculum:end block-index client -->\n",
			"ends a region that never began",
		},
		{
			"an end naming a different region",
			"<!-- vinculum:begin block-index client -->\n<!-- vinculum:end block-index server -->\n",
			`"block-index server" ends "block-index client"`,
		},
		{
			"a nested begin",
			"<!-- vinculum:begin block-index client -->\n<!-- vinculum:begin context message -->\n",
			"begins inside",
		},
		{"an unknown kind", "<!-- vinculum:begin nope x -->\n", `unknown region kind "nope"`},
		{"a section with no path", "<!-- vinculum:begin block-attrs -->\n", "takes a topic path"},
		{"a missing argument", "<!-- vinculum:begin block-index -->\n", "takes one argument"},
		{"too many arguments", "<!-- vinculum:begin context a b -->\n", "takes one argument"},
		// The namespaces region deliberately takes no argument: its value is
		// being the complete list, and one-at-a-time markers could go stale by
		// omitting the next root added.
		{"an argument to namespaces", "<!-- vinculum:begin namespaces sys -->\n", "takes no arguments"},
		{"no argument to namespace-members", "<!-- vinculum:begin namespace-members -->\n", "takes one argument"},
		{"a bad level", "<!-- vinculum:begin context message level=9 -->\n", "bad level"},
		{"a non-numeric level", "<!-- vinculum:begin context message level=x -->\n", "bad level"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRegions(tc.src)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestParseRegionsIgnoresOrdinaryComments(t *testing.T) {
	got, err := ParseRegions("<!-- a note -->\n<!-- TODO: vinculum:begin looks like one -->\n")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A marker inside a fenced code block is an example of a marker. Without this,
// the page that documents the feature gets rewritten by it: doc/schema.md
// shows what a region looks like, and the generator replaced that example with
// a real generated index the first time it ran.
func TestParseRegionsIgnoresMarkersInCodeFences(t *testing.T) {
	src := strings.Join([]string{
		"Here is how a region looks:",
		"",
		"```md",
		"<!-- vinculum:begin block-index client level=3 -->",
		"- a generated entry",
		"<!-- vinculum:end block-index client -->",
		"```",
		"",
		"And here is a real one:",
		"",
		"<!-- vinculum:begin context message -->",
		"<!-- vinculum:end context message -->",
	}, "\n")

	got, err := ParseRegions(src)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the region outside the fence is real")
	assert.Equal(t, RegionContext, got[0].Kind)

	// And the example survives a rewrite untouched.
	updated, _, err := UpdateRegions(testDoc(), src)
	require.NoError(t, err)
	assert.Contains(t, updated, "- a generated entry")
}

func TestUpdateRegionsLeavesEverythingElseAlone(t *testing.T) {
	doc := testDoc()
	src := strings.Join([]string{
		"# Hand-written",
		"",
		"Prose above.",
		"",
		"<!-- vinculum:begin block-index client level=3 -->",
		"stale",
		"<!-- vinculum:end block-index client -->",
		"",
		"Prose below.",
		"",
	}, "\n")

	got, changed, err := UpdateRegions(doc, src)
	require.NoError(t, err)
	require.Len(t, changed, 1)

	assert.Contains(t, got, "# Hand-written")
	assert.Contains(t, got, "Prose above.")
	assert.Contains(t, got, "Prose below.")
	assert.NotContains(t, got, "stale")
	assert.Contains(t, got, `- [`+"`"+`client "mqtt"`+"`"+`](client-mqtt.md) — An MQTT 5.0 publisher and subscriber.`)
	// The markers survive, or the region could only ever be written once.
	assert.Contains(t, got, "<!-- vinculum:begin block-index client level=3 -->")
	assert.Contains(t, got, "<!-- vinculum:end block-index client -->")
}

// Regenerating an up-to-date document must be a no-op, or --check could never
// distinguish "stale" from "the generator is not deterministic".
func TestUpdateRegionsIsIdempotent(t *testing.T) {
	doc := testDoc()
	src := "<!-- vinculum:begin block-index client -->\n<!-- vinculum:end block-index client -->\n"

	once, changed, err := UpdateRegions(doc, src)
	require.NoError(t, err)
	assert.Len(t, changed, 1, "the first pass fills an empty region")

	twice, changed, err := UpdateRegions(doc, once)
	require.NoError(t, err)
	assert.Empty(t, changed, "the second pass has nothing to do")
	assert.Equal(t, once, twice)
}

func TestUpdateRegionsWithNoRegionsIsUntouched(t *testing.T) {
	doc := testDoc()
	src := "# Just a page\n\nWith no markers at all.\n"

	got, changed, err := UpdateRegions(doc, src)
	require.NoError(t, err)
	assert.Empty(t, changed)
	assert.Equal(t, src, got, "a file with no regions is not rewritten")
}

func TestRenderRegionRejectsWhatItCannotRender(t *testing.T) {
	doc := testDoc()

	for _, tc := range []struct {
		name string
		r    Region
		want string
	}{
		{"an unknown block", Region{Kind: RegionBlockIndex, Args: []string{"nope"}}, "no such block type"},
		{
			"a plain block has no type index",
			Region{Kind: RegionBlockIndex, Args: []string{"subscription"}},
			"not a typed block",
		},
		{"an unknown shape", Region{Kind: RegionContext, Args: []string{"nope"}}, "no such context shape"},
		{"an unknown namespace", Region{Kind: RegionNamespaceMembers, Args: []string{"nope"}}, "no such namespace"},
		// A block namespace's names come from the configuration, so an empty
		// region here would claim the root carries nothing.
		{
			"a block namespace has no members",
			Region{Kind: RegionNamespaceMembers, Args: []string{"subscription"}},
			"has no members to list",
		},
		{"an unknown topic", Region{Kind: RegionBlockBody, Args: []string{"nope"}}, "resolves to 0 topics"},
		// An ambiguous path is refused rather than resolved by picking one:
		// silently documenting the wrong client would leave no trace.
		{"an ambiguous topic", Region{Kind: RegionBlockBody, Args: []string{"http"}}, "resolves to 2 topics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderRegion(doc, tc.r)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The three section kinds are what let a page keep its hand-tuned synopsis and
// its worked examples while generating the attribute table between them.
func TestRenderRegionSections(t *testing.T) {
	doc := testDoc()

	for _, tc := range []struct {
		name           string
		r              Region
		want, dontWant string
	}{
		{
			"a synopsis alone",
			Region{Kind: RegionBlockSynopsis, Args: []string{"client", "mqtt"}},
			"```hcl", "| Attribute |",
		},
		{
			"attributes alone",
			Region{Kind: RegionBlockAttrs, Args: []string{"client", "mqtt"}},
			"| Attribute |", "```hcl",
		},
		{
			"one attribute's ctx alone",
			Region{Kind: RegionBlockCtx, Args: []string{"subscription", "action"}},
			"| Field | Type | Description |", "| Attribute |",
		},
		// The namespaces region says what each root is; members are a separate
		// region because a page wants them selectively — `sys`'s are facts to
		// look up, `http_status`'s sixty are one fact said sixty times.
		{
			"every namespace, without members",
			Region{Kind: RegionNamespaces, Level: 4},
			"#### `sys`", "| `sys.hostname` |",
		},
		{
			"one namespace's members alone",
			Region{Kind: RegionNamespaceMembers, Args: []string{"sys"}, Level: 4},
			"| `sys.hostname` |", "#### `sys`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderRegion(doc, tc.r)
			require.NoError(t, err)
			assert.Contains(t, got, tc.want)
			assert.NotContains(t, got, tc.dontWant)
		})
	}
}

// A section that does not apply is reported rather than rendered empty: an
// empty region under a hand-written "#### Attributes" is easy to miss in review
// and says the block has none, which is a different claim from "not applicable".
func TestRenderRegionReportsAnInapplicableSection(t *testing.T) {
	doc := testDoc()

	for _, tc := range []struct {
		name string
		r    Region
		want string
	}{
		{
			"attributes of a typed block",
			Region{Kind: RegionBlockAttrs, Args: []string{"client"}},
			"no body of its own",
		},
		{
			"a ctx for an attribute that has none",
			Region{Kind: RegionBlockCtx, Args: []string{"client", "mqtt", "broker"}},
			"not evaluated against a ctx",
		},
		{
			"an ambiguous path",
			Region{Kind: RegionBlockAttrs, Args: []string{"http"}},
			"resolves to 2 topics",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderRegion(doc, tc.r)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestRenderRegionBlockBodyHonoursTheLevel(t *testing.T) {
	doc := testDoc()

	got, err := RenderRegion(doc, Region{Kind: RegionBlockBody, Args: []string{"client", "mqtt"}, Level: 3})
	require.NoError(t, err)

	// No heading of its own: the hand-written heading above the region is the
	// section, which is the whole point of embedding rather than generating.
	assert.NotContains(t, got, `### `+"`"+`client "mqtt"`+"`")
	assert.Contains(t, got, "#### Attributes")
	assert.Contains(t, got, "#### `tls`")
}
