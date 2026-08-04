package schemadoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveNode is the single-candidate resolution the section tests need.
func resolveNode(t *testing.T, path ...string) Node {
	t.Helper()
	got := Resolve(testDoc(), KindBlock, path)
	require.Len(t, got, 1, "%v should resolve to exactly one topic", path)
	return got[0]
}

func renderSection(t *testing.T, sec Section, path ...string) string {
	t.Helper()
	events, err := WalkSection(resolveNode(t, path...), sec, WalkOptions{BaseLevel: 4})
	require.NoError(t, err)
	return RenderMarkdown(events, MarkdownOptions{})
}

// A section carries no heading of its own: the hand-written heading above the
// region is the section. Emitting one would double every heading in the page.
func TestSectionsEmitNoHeadingOfTheirOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		sec  Section
		path []string
	}{
		{"synopsis", SectionSynopsis, []string{"client", "mqtt"}},
		{"attrs", SectionAttrs, []string{"subscription"}},
		{"ctx", SectionCtx, []string{"subscription", "action"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderSection(t, tc.sec, tc.path...)
			require.NotEmpty(t, got)
			for _, line := range strings.Split(got, "\n") {
				assert.False(t, strings.HasPrefix(line, "#"), "unexpected heading: %q", line)
			}
		})
	}
}

func TestSectionSynopsisIsTheSkeletonAlone(t *testing.T) {
	got := renderSection(t, SectionSynopsis, "client", "mqtt")

	assert.True(t, strings.HasPrefix(got, "```hcl"))
	assert.Contains(t, got, `client "mqtt" "<name>" {`)
	assert.Contains(t, got, "broker")
	// The description and the attribute table belong to other sections.
	assert.NotContains(t, got, "An MQTT 5.0 publisher")
	assert.NotContains(t, got, "| Attribute |")
}

func TestSectionAttrsIsTheTableAndDetail(t *testing.T) {
	got := renderSection(t, SectionAttrs, "client", "mqtt")

	assert.Contains(t, got, "| Attribute | Type | Required | Description |")
	assert.Contains(t, got, "`broker`")
	assert.Contains(t, got, "**Deprecated.** Use `id` instead.")
	assert.Contains(t, got, "One of: `0`, `1`, `2`.")
	// Not the synopsis, and not the ctx tables — those are separate regions.
	assert.NotContains(t, got, "```hcl")
	assert.NotContains(t, got, "Fields readable as")
}

// The rules governing how attributes combine belong beside the table that
// lists them, not trailing a page of per-attribute prose where they read as
// documentation of whichever attribute happened to come last.
func TestSectionAttrsPutsConstraintsUnderTheTable(t *testing.T) {
	got := renderSection(t, SectionAttrs, "client", "mqtt")

	constraint := strings.Index(got, "Specify at most one of")
	require.NotEqual(t, -1, constraint)
	// "**`" opens the first per-attribute detail block, whichever attribute
	// sorts there — the ordering under test is section-level, not per-name.
	firstDetail := strings.Index(got, "**`")
	require.NotEqual(t, -1, firstDetail, "the fixture has per-attribute detail to come after")

	assert.Less(t, constraint, firstDetail, "constraints come before the per-attribute detail")
	assert.Less(t, strings.Index(got, "| Attribute |"), constraint, "and after the table")
}

// Silently omitting a sub-block would let a page document `client "mqtt"`
// without ever mentioning that it accepts a `tls` block — the same class of
// omission this whole feature exists to prevent.
func TestSectionAttrsListsSubBlocksRatherThanDroppingThem(t *testing.T) {
	got := renderSection(t, SectionAttrs, "client", "mqtt")

	assert.Contains(t, got, "#### Blocks", "the listing gets the region's own level")
	assert.Contains(t, got, "`tls`")
	assert.Contains(t, got, "TLS settings for the connection.")
	// Listed, not expanded: its attributes are a region of their own.
	assert.NotContains(t, got, "cert_file")
}

func TestSectionCtxIncludesSiteAddedFields(t *testing.T) {
	got := renderSection(t, SectionCtx, "client", "mqtt", "on_decode_error")

	assert.Contains(t, got, "Fields readable as `ctx.<name>` (shape `decode-error`):")
	assert.Contains(t, got, "`ctx.mqtt_topic`")
	assert.Contains(t, got, "*(added here)*")
	// The shape's own fields come too, and it says it is open.
	assert.Contains(t, got, "`ctx.error`")
	assert.Contains(t, got, "This shape is open")
}

// A region naming a section the node does not have is a mistake in the page.
// Rendering nothing would leave a hand-written heading standing over a blank
// space, which is exactly the failure that is hard to notice in review.
func TestWalkSectionRejectsWhatItCannotRender(t *testing.T) {
	doc := testDoc()

	for _, tc := range []struct {
		name string
		sec  Section
		path []string
		want string
	}{
		{"a synopsis for an attribute", SectionSynopsis, []string{"subscription", "action"}, "has no synopsis"},
		{"attributes of an attribute", SectionAttrs, []string{"subscription", "action"}, "has no attributes"},
		{"a ctx for a whole block", SectionCtx, []string{"subscription"}, "is not an attribute"},
		{"a ctx for an attribute that has none", SectionCtx, []string{"client", "mqtt", "broker"}, "not evaluated against a ctx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := Resolve(doc, KindBlock, tc.path)
			require.Len(t, n, 1)
			_, err := WalkSection(n[0], tc.sec, WalkOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("an unknown section", func(t *testing.T) {
		_, err := WalkSection(resolveNode(t, "subscription"), Section("nope"), WalkOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown section "nope"`)
	})
}

// A typed block has no body of its own, so it has no attributes — but it does
// have a synopsis, which is what says the type label decides the rest.
func TestSectionsOnATypedBlock(t *testing.T) {
	got := renderSection(t, SectionSynopsis, "client")
	assert.Contains(t, got, "attributes depend on the type label")

	_, err := WalkSection(resolveNode(t, "client"), SectionAttrs, WalkOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no body of its own")
}
