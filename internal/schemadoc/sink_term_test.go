package schemadoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

func renderTermNode(n Node, opts TermOptions) string {
	return RenderTerm(Walk(n, WalkOptions{}), opts)
}

func TestTermRendersAVariant(t *testing.T) {
	doc := testDoc()
	out := renderTermNode(
		VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]),
		TermOptions{Width: 78})

	// No Markdown syntax survives into terminal output.
	assert.NotContains(t, out, "```")
	assert.NotContains(t, out, "| Attribute |")
	assert.NotContains(t, out, "**")

	assert.Contains(t, out, `client "mqtt"`)
	assert.Contains(t, out, `client "mqtt" "<name>" {`)
	assert.Contains(t, out, "An MQTT 5.0 publisher and subscriber.")
	// The attribute listing is an aligned two-column layout, not a table.
	assert.Regexp(t, `\n\s+broker\s+Broker URL\.`, out)
	assert.Contains(t, out, "(string, url, required)")
}

// Wrapping is the whole reason this sink exists, so the invariant is asserted
// over every topic at several widths, measured with escapes removed.
//
// Verbatim content is exempt and is filtered out here rather than asserted
// over: a synopsis and a worked example are code, and wrapping code to fit
// would corrupt what it says. They overflow a narrow terminal instead, which
// the terminal itself then wraps — ugly, but still true.
// The terminal sink spends no column on defaults — it trails them after the
// summary alongside the type, for the reason attrTable gives.
func TestTermTrailsTheDefaultAfterTheSummary(t *testing.T) {
	out := RenderTerm([]Event{AttrTable{Rows: []AttrRow{
		{Name: "keep_alive", Type: "expression", Hint: config.HintDuration,
			Default: "30s", Summary: "Ping interval."},
		{Name: "brokers", Type: "list", Required: true, Summary: "Broker addresses."},
	}}}, TermOptions{Width: 78})

	assert.NotContains(t, out, "| Default |")
	assert.Contains(t, out, "(expression, duration, default `30s`)")
	// Required and default never crowd each other: one excludes the other.
	assert.Contains(t, out, "(list, required)")
}

func TestTermWrapsEveryProseLineToTheWidth(t *testing.T) {
	doc := testDoc()

	for _, width := range []int{40, 60, 78, 100} {
		for _, n := range allNodes(doc) {
			events := withoutVerbatim(Walk(n, WalkOptions{}))
			out := RenderTerm(events, TermOptions{Width: width, Color: true})

			for _, line := range strings.Split(out, "\n") {
				plain := stripANSI(line)
				if len([]rune(plain)) <= width {
					continue
				}
				// The one legitimate overflow: a single token with nowhere to
				// break. Assert that is what happened rather than accepting
				// any overflow.
				longest := 0
				for _, f := range strings.Fields(plain) {
					if l := len([]rune(f)); l > longest {
						longest = l
					}
				}
				assert.Greater(t, longest+len([]rune(plain))-len([]rune(strings.TrimLeft(plain, " "))), width,
					"width %d, topic %v: line overflows but could have been wrapped: %q", width, n.Path, plain)
			}
		}
	}
}

// The terminal sink indents by heading depth, so a section emitted after a
// deeply nested sub-block must carry its own heading or it reads as part of
// that sub-block. The page's "See also" footer is the case that gets this
// wrong: it follows the last thing on the page but belongs to the page.
func TestTermPageFooterIsNotIndentedUnderTheLastSubBlock(t *testing.T) {
	doc := testDoc()
	out := renderTermNode(
		VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]),
		TermOptions{Width: 78})

	lines := strings.Split(out, "\n")
	indentOf := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

	seeAlso, attributes := -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case "See also":
			seeAlso = i
		case "Attributes":
			if attributes == -1 {
				attributes = i // the page's own, not a sub-block's
			}
		}
	}
	require.NotEqual(t, -1, seeAlso, "no See also section")
	require.NotEqual(t, -1, attributes, "no Attributes section")

	// It sits where the page's other sections sit.
	assert.Equal(t, indentOf(lines[attributes]), indentOf(lines[seeAlso]),
		"the footer belongs to the page, not to whatever preceded it")

	// And what preceded it really was deeper, so the assertion above is not
	// passing for want of any nesting to be wrong about.
	deepest := 0
	for _, l := range lines[attributes:seeAlso] {
		if strings.TrimSpace(l) != "" && indentOf(l) > deepest {
			deepest = indentOf(l)
		}
	}
	assert.Greater(t, deepest, indentOf(lines[seeAlso]))
}

// A synopsis is code and is emitted as written.
func TestTermDoesNotWrapASynopsis(t *testing.T) {
	long := "client \"mqtt\" \"<name>\" {"
	out := RenderTerm([]Event{Synopsis{Lines: []string{long}}}, TermOptions{Width: 20})
	assert.Contains(t, out, long)
}

func withoutVerbatim(events []Event) []Event {
	var out []Event
	for _, e := range events {
		switch e.(type) {
		case Synopsis, Example:
			continue
		}
		out = append(out, e)
	}
	return out
}

func TestTermColorIsOptional(t *testing.T) {
	doc := testDoc()
	n := BlockNode(doc, "subscription", doc.Blocks["subscription"])

	plain := renderTermNode(n, TermOptions{Width: 78})
	colored := renderTermNode(n, TermOptions{Width: 78, Color: true})

	assert.NotContains(t, plain, "\x1b[")
	assert.Contains(t, colored, "\x1b[")
	// Colour is presentation only: removing it leaves exactly the plain form.
	assert.Equal(t, plain, stripANSI(colored))
}

func TestClampWidth(t *testing.T) {
	assert.Equal(t, DefaultWidth, ClampWidth(0), "an unknown width gets the default")
	assert.Equal(t, DefaultWidth, ClampWidth(-1))
	assert.Equal(t, MinWidth, ClampWidth(10), "too narrow to wrap in")
	assert.Equal(t, MaxWidth, ClampWidth(300), "too wide to track back to the margin")
	assert.Equal(t, 72, ClampWidth(72))
}

func TestTermIndentsByNestingDepth(t *testing.T) {
	doc := testDoc()
	out := renderTermNode(
		VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]),
		TermOptions{Width: 78})

	lines := strings.Split(out, "\n")
	// The page's own heading sits at the left margin; a sub-block's heading is
	// indented, which is how nesting reads without heading marks.
	require.Equal(t, `client "mqtt"`, lines[0])
	assert.Contains(t, out, "\n  tls\n", "a sub-block heading is indented one step")
}

// allNodes returns every topic in a document, for assertions that should hold
// everywhere rather than on one hand-picked page.
func allNodes(doc *config.SchemaDocument) []Node {
	var out []Node
	var walk func(path []string)
	walk = func(path []string) {
		out = append(out, Resolve(doc, "", path)...)
		for _, m := range Members(doc, "", path) {
			walk(append(append([]string{}, path...), m))
		}
	}
	for _, name := range LeadingNames(doc, "") {
		walk([]string{name})
	}
	return out
}
