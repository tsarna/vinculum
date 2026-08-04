package schemadoc

import "strings"

// TermOptions controls terminal rendering.
type TermOptions struct {
	// Width is the column to wrap at. Zero means DefaultWidth.
	Width int
	// Color enables ANSI escapes. When false the output is plain text, which
	// is also what the plain sink wants.
	Color bool
}

// Terminal layout constants.
const (
	// DefaultWidth is used when no terminal width is available.
	DefaultWidth = 80
	// MinWidth and MaxWidth bound the terminal's own width. A very wide
	// terminal produces lines too long to track back to the left margin;
	// a very narrow one produces nothing but hyphenless rags.
	MinWidth = 40
	MaxWidth = 100

	// indentStep is how far each heading level indents its content.
	indentStep = 2
	// maxIndent caps nesting: mqtt reaches four levels, and past this depth
	// the indent costs more readable width than the structure conveys.
	maxIndent = 6
	// nameColumn is the width of the name column in attribute and field
	// listings. Longer names take their own line rather than pushing the
	// descriptions of every other row to the right.
	nameColumn = 22
)

// ClampWidth brings a measured terminal width into the range worth wrapping
// at, and supplies the default for an unknown one.
func ClampWidth(w int) int {
	switch {
	case w <= 0:
		return DefaultWidth
	case w < MinWidth:
		return MinWidth
	case w > MaxWidth:
		return MaxWidth
	}
	return w
}

// RenderTerm renders events as text for a terminal: wrapped to a width, styled
// with ANSI when asked, and indented to show the nesting that Markdown conveys
// with heading levels.
func RenderTerm(events []Event, opts TermOptions) string {
	t := &termSink{
		width: ClampWidth(opts.Width),
		st:    style{enabled: opts.Color},
	}
	for _, e := range events {
		t.render(e)
	}
	return strings.Join(t.lines, "\n") + "\n"
}

// RenderPlain renders events as unstyled text.
//
// It is the terminal sink with colour off rather than a third implementation:
// every layout decision — wrapping, indenting, the two-column attribute
// listing — is one a plain reader wants too, and the only difference is the
// escape sequences. A separate sink would be the same code with a chance of
// disagreeing with it.
//
// This is what help() returns into an expression, where the caller may be
// writing the string to a log or a file rather than a terminal.
func RenderPlain(events []Event, width int) string {
	return RenderTerm(events, TermOptions{Width: width, Color: false})
}

type termSink struct {
	lines []string
	width int
	st    style
	// indent is the current content indent, set by the last heading.
	indent int
}

func (t *termSink) line(s string) { t.lines = append(t.lines, strings.TrimRight(s, " ")) }
func (t *termSink) blank()        { t.line("") }
func (t *termSink) pad() string   { return strings.Repeat(" ", t.indent) }
func (t *termSink) avail() int    { return t.width - t.indent }
func (t *termSink) emit(ls ...string) {
	t.lines = append(t.lines, ls...)
}

// gap opens a blank line before a block, unless one is already there or this is
// the first thing on the page.
func (t *termSink) gap() {
	if len(t.lines) == 0 || t.lines[len(t.lines)-1] == "" {
		return
	}
	t.blank()
}

func (t *termSink) render(e Event) {
	switch v := e.(type) {
	case Heading:
		t.gap()
		headIndent := min((v.Level-1)*indentStep, maxIndent)
		t.line(strings.Repeat(" ", headIndent) + t.st.apply(spanStrong, plainText(v.Text)))
		t.indent = headIndent + indentStep
		t.blank()

	case Synopsis:
		t.gap()
		for _, l := range v.Lines {
			t.line(t.pad() + t.synopsisLine(l))
		}

	case Preformatted:
		t.gap()
		// Verbatim, indented to sit under its heading. Not wrapped and not
		// styled: the alignment is the content.
		for _, l := range strings.Split(strings.TrimRight(v.Text, "\n"), "\n") {
			t.line(t.pad() + l)
		}

	case Prose:
		t.gap()
		t.emit(renderProse(v.Markdown, t.width, t.indent, t.st)...)

	case Note:
		t.gap()
		for _, l := range wrapWords(splitWords(parseInline(v.Text)), t.avail(), style{}) {
			t.line(t.pad() + t.st.apply(spanEmphasis, l))
		}

	case AttrTable:
		t.attrTable(v)

	case AttrDetail:
		t.attrDetail(v)

	case BlockTable:
		t.blockTable(v)

	case ContextTable:
		t.contextTable(v)

	case Constraints:
		t.gap()
		for _, c := range v.Items {
			t.column("•", ConstraintText(c), 1)
		}

	case SeeAlso:
		t.seeAlso(v)

	case Menu:
		t.gap()
		t.emit(renderProse(v.Intro, t.width, t.indent, t.st)...)
		t.blank()
		for _, item := range v.Items {
			t.line(t.pad() + "    " + t.st.apply(spanCode, item))
		}

	case Example:
		t.gap()
		if v.Caption != "" {
			t.emit(renderProse(v.Caption, t.width, t.indent, t.st)...)
			t.blank()
		}
		for _, l := range strings.Split(strings.TrimRight(v.Source, "\n"), "\n") {
			t.line(t.pad() + "  " + t.st.apply(spanCode, l))
		}
	}
}

// synopsisLine dims comments so the shape of the block reads before the
// annotations do.
func (t *termSink) synopsisLine(l string) string {
	if strings.HasPrefix(strings.TrimSpace(l), "#") {
		return t.st.apply(spanEmphasis, l)
	}
	// A trailing comment only: the guard is what keeps a whole-line comment,
	// whose leading indent also matches "  # ", from being split in the middle.
	if i := strings.Index(l, "  # "); i > 0 && strings.TrimSpace(l[:i]) != "" {
		return l[:i] + t.st.apply(spanEmphasis, l[i:])
	}
	return l
}

// columnWidth sizes the name column for one listing: wide enough for its own
// names, never wider than nameColumn, and never taking more than a third of
// the available width.
//
// Sizing it per listing rather than fixing it means a two-attribute sub-block
// does not indent its descriptions past the widest attribute name in the
// language, and a narrow terminal does not end up with a nine-column gutter to
// wrap prose in.
func (t *termSink) columnWidth(names []string) int {
	w := 0
	for _, n := range names {
		if l := len([]rune(n)); l > w {
			w = l
		}
	}
	if w > nameColumn {
		w = nameColumn
	}
	if third := t.avail() / 3; w > third {
		w = third
	}
	if w < 4 {
		w = 4
	}
	return w
}

// gutter is the minimum space between the name column and the description, so
// that a name which exactly fills its column is still separated from the text.
const gutter = 2

// column lays out a name and a wrapped description side by side, the layout
// every listing here uses. width is the name column; a name that overflows it
// takes its own line rather than pushing every description right.
func (t *termSink) column(name, desc string, width int) {
	styled := t.st.apply(spanCode, name)
	indent := width + gutter
	body := wrapWords(splitWords(parseInline(desc)), t.avail()-indent, t.st)

	if len(body) == 0 {
		t.line(t.pad() + styled)
		return
	}
	if len([]rune(name)) > width {
		t.line(t.pad() + styled)
		for _, l := range body {
			t.line(t.pad() + strings.Repeat(" ", indent) + l)
		}
		return
	}

	gap := strings.Repeat(" ", indent-len([]rune(name)))
	t.line(t.pad() + styled + gap + body[0])
	for _, l := range body[1:] {
		t.line(t.pad() + strings.Repeat(" ", indent) + l)
	}
}

func (t *termSink) attrTable(v AttrTable) {
	if len(v.Rows) == 0 {
		return
	}
	t.gap()
	names := make([]string, len(v.Rows))
	for i, r := range v.Rows {
		names[i] = r.Name
	}
	width := t.columnWidth(names)

	for _, r := range v.Rows {
		// The type and whether it is required trail the summary rather than
		// taking a column of their own: they are shorter than the summary,
		// vary less, and a third column would leave too little room to read in.
		desc := oneLine(r.Summary)
		if r.Deprecated != "" {
			desc = "**Deprecated.** " + desc
		}
		if note := attrQualifier(r); note != "" {
			desc = strings.TrimSpace(desc + " *(" + note + ")*")
		}
		t.column(r.Name, desc, width)
	}
}

// attrQualifier renders the parenthetical after an attribute's summary.
//
// The hint is a sibling of the type here rather than nested inside it as the
// Markdown sink writes it: this whole string is already inside parentheses, and
// "(expression (action-expression), required)" is harder to read than the three
// facts it contains.
func attrQualifier(r AttrRow) string {
	parts := []string{r.Type}
	if h := string(r.Hint); h != "" && h != r.Type {
		parts = append(parts, h)
	}
	if r.Required {
		parts = append(parts, "required")
	}
	return strings.Join(parts, ", ")
}

func (t *termSink) attrDetail(d AttrDetail) {
	t.gap()
	t.line(t.pad() + t.st.apply(spanCode, d.Name))

	// The pieces are assembled into one Markdown string and rendered once,
	// rather than rendered one at a time: each is its own paragraph, and only
	// a single pass can put the blank lines between them.
	var parts []string
	if d.Doc != "" {
		parts = append(parts, d.Doc)
	}
	if len(d.Enum) > 0 {
		parts = append(parts, "One of: "+strings.Join(backtickAll(d.Enum), ", ")+".")
	}
	if d.Deprecated != "" {
		parts = append(parts, "**Deprecated.** "+d.Deprecated)
	}
	if d.Context != "" {
		parts = append(parts, "Evaluated against the `"+d.Context+"` context.")
	}

	t.emit(renderProse(strings.Join(parts, "\n\n"), t.width, t.indent+indentStep, t.st)...)
}

func (t *termSink) blockTable(v BlockTable) {
	if len(v.Rows) == 0 {
		return
	}
	t.gap()
	names := make([]string, len(v.Rows))
	for i, r := range v.Rows {
		names[i] = blockHeader(r.Name, r.Labels)
	}
	width := t.columnWidth(names)

	for i, r := range v.Rows {
		desc := oneLine(r.Summary)
		if r.Cardinality != "" {
			desc = strings.TrimSpace(desc + " *(" + r.Cardinality + ")*")
		}
		t.column(names[i], desc, width)
	}
}

func (t *termSink) contextTable(v ContextTable) {
	t.gap()
	intro := "Fields readable as `ctx.<name>`"
	if v.Shape != "" {
		intro += " (shape `" + v.Shape + "`)"
	}
	t.emit(renderProse(intro+":", t.width, t.indent, t.st)...)
	t.blank()

	names := make([]string, len(v.Rows))
	for i, r := range v.Rows {
		names[i] = "ctx." + r.Name
	}
	width := t.columnWidth(names)

	for i, r := range v.Rows {
		desc := oneLine(r.Summary)
		var notes []string
		if r.Type != "" {
			notes = append(notes, r.Type)
		}
		switch {
		case r.Added:
			notes = append(notes, "added here")
		case r.Universal:
			notes = append(notes, "universal")
		}
		if r.Optional {
			notes = append(notes, "not always present")
		}
		t.column(names[i], strings.TrimSpace(desc+" *("+strings.Join(notes, ", ")+")*"), width)
	}
	if v.OpenFields {
		t.blank()
		t.emit(renderProse("*This shape is open: a particular site may carry fields beyond these.*",
			t.width, t.indent, t.st)...)
	}
}

func (t *termSink) seeAlso(s SeeAlso) {
	if len(s.Items) == 0 {
		return
	}
	t.gap()
	for _, l := range s.Items {
		text := l.Text
		// A terminal cannot follow a link, so the target is shown — unless it
		// is already what the text says, which is the usual case for a page
		// referred to by its filename.
		if l.Target != "" && l.Target != l.Text {
			text += " (" + l.Target + ")"
		}
		t.column("•", text, 1)
	}
}

// ---------------------------------------------------------------------------
// ANSI styling
// ---------------------------------------------------------------------------

// style applies the terminal's text attributes, or none at all.
//
// The vocabulary is deliberately four attributes wide and uses one colour.
// Bold, dim, and underline render on any terminal and against any background;
// a palette would not, and documentation that is unreadable on half of the
// terminals it is displayed on has failed at the only thing it does.
type style struct{ enabled bool }

const (
	ansiReset     = "\x1b[0m"
	ansiBold      = "\x1b[1m"
	ansiDim       = "\x1b[2m"
	ansiUnderline = "\x1b[4m"
	ansiCyan      = "\x1b[36m"
)

func (s style) apply(kind spanKind, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	switch kind {
	case spanCode:
		return ansiCyan + text + ansiReset
	case spanStrong:
		return ansiBold + text + ansiReset
	case spanEmphasis:
		return ansiDim + text + ansiReset
	case spanLink:
		return ansiUnderline + text + ansiReset
	}
	return text
}

// plainText strips the inline markup from a string that is going somewhere
// markup cannot be interpreted — a heading, which is already styled as a whole.
func plainText(s string) string {
	var b strings.Builder
	for _, sp := range parseInline(s) {
		b.WriteString(sp.text)
	}
	return b.String()
}

// stripANSI removes escape sequences, for the one place that restyles text
// that has already been through a sink.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
