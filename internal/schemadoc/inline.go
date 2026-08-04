package schemadoc

import (
	"strings"
	"unicode"
)

// Rendering curated Markdown as terminal text.
//
// Only the `Prose` event carries Markdown — everything else the walker emits is
// structured — so what has to be understood here is the subset that appears in
// hand-authored Summary and Doc strings, and nothing more. That is why there is
// no Markdown library in this repository: a full CommonMark parser would be a
// large dependency for a grammar we also author.
//
// The subset is: paragraphs, fenced code blocks, bullet and numbered lists,
// blockquotes, and the inline spans below. GFM tables are deliberately absent —
// a table inside a curated Doc is a sign the data wants to be structured (an
// Enum, a ContextField list) rather than prose.

// spanKind is how one run of text is marked up.
type spanKind int

const (
	spanPlain spanKind = iota
	spanCode
	spanStrong
	spanEmphasis
	spanLink
)

// span is a run of text with one style. Inline markup is parsed into spans
// before wrapping, because wrapping has to count *visible* width: escape codes
// occupy no columns, so styling has to be applied after the line breaks are
// chosen rather than before.
type span struct {
	text string
	kind spanKind
	href string
}

// parseInline splits Markdown into styled spans.
//
// Nesting is not supported and does not need to be: `**bold `code`**` does not
// appear in curated text, and treating the inner marker as literal is a better
// failure than a parser that can get lost.
func parseInline(s string) []span {
	var spans []span
	var plain strings.Builder

	flush := func() {
		if plain.Len() > 0 {
			spans = append(spans, span{text: plain.String()})
			plain.Reset()
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); {
		switch {
		case runes[i] == '\\' && i+1 < len(runes) && isPunct(runes[i+1]):
			// An escaped marker is literal, and loses its backslash.
			plain.WriteRune(runes[i+1])
			i += 2

		case runes[i] == '`':
			if end := indexRune(runes, '`', i+1); end > 0 {
				flush()
				spans = append(spans, span{text: string(runes[i+1 : end]), kind: spanCode})
				i = end + 1
				continue
			}
			plain.WriteRune(runes[i])
			i++

		case hasPrefixAt(runes, i, "**"), hasPrefixAt(runes, i, "__"):
			marker := string(runes[i : i+2])
			if end := indexString(runes, marker, i+2); end > 0 {
				flush()
				spans = append(spans, span{text: string(runes[i+2 : end]), kind: spanStrong})
				i = end + 2
				continue
			}
			plain.WriteRune(runes[i])
			i++

		case runes[i] == '*' || runes[i] == '_':
			// A marker only opens emphasis when it is followed by text: an
			// underscore inside an identifier (queue_size, add_topic_prefix) is
			// far more common in this corpus than emphasis is.
			if end := emphasisEnd(runes, i); end > 0 {
				flush()
				spans = append(spans, span{text: string(runes[i+1 : end]), kind: spanEmphasis})
				i = end + 1
				continue
			}
			plain.WriteRune(runes[i])
			i++

		case runes[i] == '[':
			if text, href, next, ok := parseLink(runes, i); ok {
				flush()
				spans = append(spans, span{text: text, kind: spanLink, href: href})
				i = next
				continue
			}
			plain.WriteRune(runes[i])
			i++

		default:
			plain.WriteRune(runes[i])
			i++
		}
	}
	flush()
	return spans
}

// emphasisEnd finds the closing marker of a single-character emphasis span, or
// returns -1 when the marker at i does not open one.
func emphasisEnd(runes []rune, i int) int {
	marker := runes[i]
	// Not emphasis when the marker is inside a word: queue_size, or a bare *.
	if i > 0 && isWordRune(runes[i-1]) {
		return -1
	}
	if i+1 >= len(runes) || unicode.IsSpace(runes[i+1]) {
		return -1
	}
	for j := i + 1; j < len(runes); j++ {
		if runes[j] != marker {
			continue
		}
		// The closing marker must end the span, not start a new word.
		if j+1 < len(runes) && isWordRune(runes[j+1]) {
			continue
		}
		if unicode.IsSpace(runes[j-1]) {
			continue
		}
		return j
	}
	return -1
}

// parseLink reads [text](href) at i.
func parseLink(runes []rune, i int) (text, href string, next int, ok bool) {
	close := indexRune(runes, ']', i+1)
	if close < 0 || close+1 >= len(runes) || runes[close+1] != '(' {
		return "", "", 0, false
	}
	end := indexRune(runes, ')', close+2)
	if end < 0 {
		return "", "", 0, false
	}
	return string(runes[i+1 : close]), string(runes[close+2 : end]), end + 1, true
}

func indexRune(runes []rune, r rune, from int) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == r {
			return i
		}
	}
	return -1
}

func indexString(runes []rune, s string, from int) int {
	for i := from; i+len([]rune(s)) <= len(runes); i++ {
		if hasPrefixAt(runes, i, s) {
			return i
		}
	}
	return -1
}

func hasPrefixAt(runes []rune, i int, s string) bool {
	sr := []rune(s)
	if i+len(sr) > len(runes) {
		return false
	}
	for j, r := range sr {
		if runes[i+j] != r {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func isPunct(r rune) bool {
	return strings.ContainsRune("\\`*_[]()#+-.!", r)
}

// fragment is a run of text with one style, within a word.
type fragment struct {
	text string
	kind spanKind
}

// word is one wrappable unit. It holds fragments rather than a single string
// because a word can span a style boundary: `subscriber`/`action` is written
// with no whitespace in it, so it is one word made of four differently styled
// pieces. Splitting on style instead would insert a space that the author did
// not write.
type word struct {
	parts []fragment
	width int
}

// splitWords breaks spans into wrappable words, preserving both the styling
// within a word and the absence of whitespace between styles.
//
// A code span containing spaces is split like any other text, so `client
// "mqtt"` may break across a line. Styling each half separately is the price of
// never overflowing the terminal width, and refusing to break it would produce
// the ragged output that wrapping exists to avoid.
func splitWords(spans []span) []word {
	var words []word
	// open is the word being accumulated; a span that starts without leading
	// whitespace continues it rather than starting a new one.
	var open *word

	for _, s := range spans {
		text := s.text
		if s.kind == spanLink && s.href != "" {
			// The URL is shown rather than footnoted: a terminal reader cannot
			// click a footnote any more than a link, and hiding the target
			// helps nobody.
			text = text + " (" + s.href + ")"
		}
		if text == "" {
			continue
		}

		leading := startsWithSpace(text)
		trailing := endsWithSpace(text)

		for i, f := range strings.Fields(text) {
			if i == 0 && !leading && open != nil {
				open.parts = append(open.parts, fragment{f, s.kind})
				open.width += len([]rune(f))
				continue
			}
			words = append(words, word{parts: []fragment{{f, s.kind}}, width: len([]rune(f))})
			open = &words[len(words)-1]
		}
		if trailing {
			open = nil
		}
	}
	return words
}

func startsWithSpace(s string) bool {
	return s != "" && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n')
}

func endsWithSpace(s string) bool {
	n := len(s)
	return n > 0 && (s[n-1] == ' ' || s[n-1] == '\t' || s[n-1] == '\n')
}

// wrapWords lays words out at the given width, styling each fragment as it is
// emitted. The returned lines carry no indent; the caller adds it.
func wrapWords(words []word, width int, st style) []string {
	if width < 8 {
		width = 8
	}

	var lines []string
	var line strings.Builder
	visible := 0

	for _, w := range words {
		if visible > 0 && visible+1+w.width > width {
			lines = append(lines, line.String())
			line.Reset()
			visible = 0
		}
		if visible > 0 {
			line.WriteByte(' ')
			visible++
		}
		for _, f := range w.parts {
			line.WriteString(st.apply(f.kind, f.text))
		}
		visible += w.width
	}
	if visible > 0 {
		lines = append(lines, line.String())
	}
	return lines
}

// renderProse lays out one curated Markdown string as terminal lines, already
// indented.
//
// Block elements are separated by a blank line, as they are in the source: two
// paragraphs of a Doc run together read as one, and the author wrote two.
func renderProse(md string, width, indent int, st style) []string {
	var out []string
	pad := strings.Repeat(" ", indent)
	avail := width - indent

	// separate opens a blank line before a block unless it would be the first
	// line or would double an existing one.
	separate := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}

	var para []string
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, " ")
		para = nil
		separate()
		for _, l := range wrapWords(splitWords(parseInline(text)), avail, st) {
			out = append(out, pad+l)
		}
	}

	// A run of list items is one block: they are separated from surrounding
	// prose but not from each other.
	prevWasListItem := false

	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		wasListItem := prevWasListItem
		prevWasListItem = trimmed != "" && isListItem(trimmed)

		switch {
		case strings.HasPrefix(trimmed, "```"):
			flushPara()
			separate()
			// Verbatim to the closing fence, or to the end if there is none.
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				out = append(out, pad+"  "+st.apply(spanCode, lines[i]))
			}

		// An indented code block. Curated Doc strings write their worked
		// examples this way — a tab-indented run inside a Go raw string — so
		// treating it as prose would reflow a config example into a paragraph.
		case isIndentedCode(line) && len(para) == 0:
			flushPara()
			separate()
			block, next := indentedCodeBlock(lines, i)
			for _, l := range block {
				out = append(out, pad+"  "+st.apply(spanCode, l))
			}
			i = next - 1

		case trimmed == "":
			flushPara()

		case isListItem(trimmed):
			flushPara()
			if !wasListItem {
				separate()
			}
			marker, rest := splitListItem(trimmed)
			// Gather the item's continuation lines before wrapping, so a
			// hanging indent lines up under the first word rather than under
			// the bullet.
			for i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if next == "" || isListItem(next) {
					break
				}
				rest += " " + next
				i++
			}
			// Runes, not bytes: the bullet is a multi-byte character, and a
			// byte count would indent continuation lines past where the item's
			// own text starts.
			markerWidth := len([]rune(marker))
			hang := strings.Repeat(" ", markerWidth)
			wrapped := wrapWords(splitWords(parseInline(rest)), avail-markerWidth, st)
			for j, l := range wrapped {
				if j == 0 {
					out = append(out, pad+marker+l)
				} else {
					out = append(out, pad+hang+l)
				}
			}

		case strings.HasPrefix(trimmed, ">"):
			flushPara()
			separate()
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			for _, l := range wrapWords(splitWords(parseInline(quote)), avail-2, st) {
				out = append(out, pad+"  "+st.apply(spanEmphasis, stripANSI(l)))
			}

		case strings.HasPrefix(trimmed, "#"):
			// A heading inside curated prose is rare and structurally
			// meaningless here — the walker owns the heading levels — so it
			// renders as an emphasized line rather than restarting the
			// hierarchy.
			flushPara()
			separate()
			out = append(out, pad+st.apply(spanStrong, strings.TrimLeft(trimmed, "# ")))

		default:
			para = append(para, trimmed)
		}
	}
	flushPara()
	return out
}

// isIndentedCode reports whether a line opens or continues an indented code
// block: a leading tab, or four leading spaces.
func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
}

// indentedCodeBlock collects the block starting at lines[start], dedented by
// one level, and returns the index of the first line after it.
//
// A blank line belongs to the block only when more code follows it, so the
// blank line that ends the block does not become a trailing empty code line.
func indentedCodeBlock(lines []string, start int) (block []string, next int) {
	i := start
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" {
			j := i
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j >= len(lines) || !isIndentedCode(lines[j]) {
				break
			}
			block = append(block, lines[i:j]...)
			i = j
			continue
		}
		if !isIndentedCode(lines[i]) {
			break
		}
		block = append(block, dedent(lines[i]))
		i++
	}
	return block, i
}

// dedent removes one level of leading indentation: a tab, or up to four spaces.
func dedent(line string) string {
	if strings.HasPrefix(line, "\t") {
		return line[1:]
	}
	return strings.TrimPrefix(line, "    ")
}

func isListItem(s string) bool {
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") || strings.HasPrefix(s, "+ ") {
		return true
	}
	for i, r := range s {
		if unicode.IsDigit(r) {
			continue
		}
		return i > 0 && r == '.' && i+1 < len(s) && s[i+1] == ' '
	}
	return false
}

// splitListItem returns the item's marker (padded to its rendered width) and
// its text.
func splitListItem(s string) (marker, rest string) {
	if len(s) > 1 && (s[0] == '-' || s[0] == '*' || s[0] == '+') {
		return "• ", strings.TrimSpace(s[1:])
	}
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+2], strings.TrimSpace(s[i+2:])
	}
	return "• ", s
}
