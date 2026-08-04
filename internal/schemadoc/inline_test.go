package schemadoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spanText renders parsed spans as "kind:text|kind:text", which is what the
// parser assertions compare.
func spanText(spans []span) string {
	var parts []string
	for _, s := range spans {
		kind := map[spanKind]string{
			spanPlain: "plain", spanCode: "code", spanStrong: "strong",
			spanEmphasis: "em", spanLink: "link",
		}[s.kind]
		parts = append(parts, kind+":"+s.text)
	}
	return strings.Join(parts, "|")
}

func TestParseInline(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello there", "plain:hello there"},
		{"code span", "set `topics` first", "plain:set |code:topics|plain: first"},
		{"strong", "**Deprecated.** use x", "strong:Deprecated.|plain: use x"},
		{"emphasis", "an *important* thing", "plain:an |em:important|plain: thing"},
		{"link", "see [the guide](x.md)", "plain:see |link:the guide"},

		// An underscore inside an identifier is not emphasis. This corpus is
		// full of queue_size and add_topic_prefix, and reading the first as an
		// opening marker would swallow the rest of the sentence.
		{"underscore in an identifier", "queue_size and drop_topic_prefix", "plain:queue_size and drop_topic_prefix"},
		{"underscore emphasis still works", "an _italic_ word", "plain:an |em:italic|plain: word"},

		// Unmatched markers are literal rather than swallowing the rest.
		{"unclosed code", "a ` backtick", "plain:a ` backtick"},
		{"unclosed strong", "a ** marker", "plain:a ** marker"},
		{"a bare asterisk", "2 * 3", "plain:2 * 3"},

		{"escaped marker", `a \* literal`, "plain:a * literal"},
		{"adjacent spans", "`a`/`b`", "code:a|plain:/|code:b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, spanText(parseInline(tc.in)))
		})
	}
}

// The wrapper counts visible columns, so a styled line has to be measured with
// its escapes removed.
func TestWrapCountsVisibleWidth(t *testing.T) {
	words := splitWords(parseInline("the `quick` brown fox jumps over the lazy dog"))

	for _, st := range []style{{enabled: false}, {enabled: true}} {
		lines := wrapWords(words, 20, st)
		for _, l := range lines {
			assert.LessOrEqual(t, len([]rune(stripANSI(l))), 20, "line too wide: %q", l)
		}
		assert.Equal(t, "the quick brown fox jumps over the lazy dog",
			strings.Join(mapStrings(lines, stripANSI), " "), "no words lost or gained")
	}
}

// Adjacent spans with no whitespace between them are one word. Rendering
// `subscriber`/`action` as three words would insert spaces the author did not
// write — and that string appears verbatim in the subscription block's docs.
func TestWrapDoesNotInsertSpacesBetweenAdjacentSpans(t *testing.T) {
	words := splitWords(parseInline("The `subscriber`/`action`/`transforms` set"))
	got := strings.Join(wrapWords(words, 100, style{}), " ")
	assert.Equal(t, "The subscriber/action/transforms set", got)

	// Punctuation attached to a code span stays attached.
	words = splitWords(parseInline("use `jq()`, then `stop()`."))
	assert.Equal(t, "use jq(), then stop().", strings.Join(wrapWords(words, 100, style{}), " "))
}

func TestRenderProseSeparatesBlocks(t *testing.T) {
	md := "First paragraph.\n\nSecond paragraph."
	got := renderProse(md, 60, 0, style{})
	assert.Equal(t, []string{"First paragraph.", "", "Second paragraph."}, got)
}

func TestRenderProseLists(t *testing.T) {
	md := "Before.\n\n- first item\n- a second item that is quite long and must wrap somewhere\n\nAfter."
	got := strings.Join(renderProse(md, 40, 0, style{}), "\n")

	assert.Contains(t, got, "• first item")
	// A wrapped item hangs under its own text, not under the bullet.
	assert.Regexp(t, `• a second item that is quite long and\n  must wrap somewhere`, got)
	// Items are one block: separated from prose, not from each other.
	assert.NotContains(t, got, "• first item\n\n•")
}

// Curated Doc strings write their worked examples as indented blocks inside a
// Go raw string. Reflowing one into a paragraph destroys the example.
func TestRenderProseKeepsIndentedCodeVerbatim(t *testing.T) {
	md := "Intro.\n\n\tfunction \"circle_area\" {\n\t    params = [radius]\n\t}\n\nAfter."
	got := renderProse(md, 60, 0, style{})

	require.Contains(t, got, `  function "circle_area" {`)
	require.Contains(t, got, "      params = [radius]")
	require.Contains(t, got, "  }")
	assert.Equal(t, "After.", got[len(got)-1], "the block ends where the indent does")
}

func TestRenderProseFencedCode(t *testing.T) {
	md := "Intro.\n\n```hcl\nbus \"main\" {}\n```\n\nAfter."
	got := renderProse(md, 60, 0, style{})

	assert.Contains(t, got, `  bus "main" {}`)
	assert.NotContains(t, got, "```")
	assert.Equal(t, "After.", got[len(got)-1])
}

func TestRenderProseIndent(t *testing.T) {
	got := renderProse("Some text here.", 60, 4, style{})
	assert.Equal(t, []string{"    Some text here."}, got)
}

func TestStripANSI(t *testing.T) {
	assert.Equal(t, "bold", stripANSI(ansiBold+"bold"+ansiReset))
	assert.Equal(t, "plain", stripANSI("plain"))
	assert.Equal(t, "ab", stripANSI(ansiCyan+"a"+ansiReset+ansiDim+"b"+ansiReset))
}

func mapStrings(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}
