package schemadoc

import (
	"fmt"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// MarkdownOptions controls Markdown rendering.
type MarkdownOptions struct {
	// LinkArgv renders a cross-reference that names a topic by argv. Nil means
	// such references are rendered as plain text, which is what a page outside
	// doc/ wants — there is nothing to link to.
	LinkArgv func(argv []string) string
}

// RenderMarkdown renders events as GitHub-flavored Markdown.
//
// This is the faithful form: it carries every event's content without the
// wrapping, truncation, or styling the terminal sink applies, which is why the
// doc/ generator and the golden tests both use it.
func RenderMarkdown(events []Event, opts MarkdownOptions) string {
	m := &markdownSink{opts: opts}
	for _, e := range events {
		m.render(e)
	}
	return strings.TrimLeft(m.b.String(), "\n")
}

type markdownSink struct {
	b    strings.Builder
	opts MarkdownOptions
}

// para starts a new block-level element, guaranteeing exactly one blank line
// before it. Every emitter goes through this rather than tracking separators
// itself.
func (m *markdownSink) para() {
	s := m.b.String()
	switch {
	case s == "":
	case strings.HasSuffix(s, "\n\n"):
	case strings.HasSuffix(s, "\n"):
		m.b.WriteString("\n")
	default:
		m.b.WriteString("\n\n")
	}
}

func (m *markdownSink) line(s string) { m.b.WriteString(s); m.b.WriteString("\n") }

func (m *markdownSink) render(e Event) {
	switch v := e.(type) {
	case Heading:
		m.para()
		m.line(strings.Repeat("#", clampLevel(v.Level)) + " " + v.Text)

	case Synopsis:
		m.para()
		m.line("```hcl")
		for _, l := range v.Lines {
			m.line(l)
		}
		m.line("```")

	case Prose:
		m.para()
		m.line(strings.TrimRight(v.Markdown, "\n"))

	case Note:
		m.para()
		m.line("*" + v.Text + "*")

	case AttrTable:
		m.attrTable(v)

	case AttrDetail:
		m.attrDetail(v)

	case BlockTable:
		m.blockTable(v)

	case ContextTable:
		m.contextTable(v)

	case MemberTable:
		m.memberTable(v)

	case Constraints:
		m.para()
		for _, c := range v.Items {
			m.line("- " + ConstraintText(c))
		}

	case SeeAlso:
		m.seeAlso(v)

	case Menu:
		m.para()
		m.line(v.Intro)
		m.para()
		for _, item := range v.Items {
			m.line("    " + item)
		}

	case Results:
		m.results(v)

	case Example:
		m.para()
		if v.Caption != "" {
			m.line(v.Caption)
			m.para()
		}
		m.line("```hcl")
		m.line(strings.TrimRight(v.Source, "\n"))
		m.line("```")
	}
}

func (m *markdownSink) attrTable(t AttrTable) {
	if len(t.Rows) == 0 {
		return
	}
	// The Default column appears only when something in this table has one,
	// so a body with no defaults is not given an empty column to explain.
	defaults := t.HasDefaults()

	m.para()
	if defaults {
		m.line("| Attribute | Type | Required | Default | Description |")
		m.line("|---|---|---|---|:---|")
	} else {
		m.line("| Attribute | Type | Required | Description |")
		m.line("|---|---|---|:---|")
	}
	for _, r := range t.Rows {
		req := ""
		if r.Required {
			req = "yes"
		}
		desc := oneLine(r.Summary)
		if r.Deprecated != "" {
			desc = "**Deprecated.** " + desc
		}
		if !defaults {
			m.line(fmt.Sprintf("| `%s` | %s | %s | %s |", r.Name, typeLabel(r.Type, r.Hint), req, desc))
			continue
		}
		def := ""
		if r.Default != "" {
			def = "`" + r.Default + "`"
		}
		m.line(fmt.Sprintf("| `%s` | %s | %s | %s | %s |",
			r.Name, typeLabel(r.Type, r.Hint), req, def, desc))
	}
}

func (m *markdownSink) attrDetail(d AttrDetail) {
	m.para()
	m.line("**`" + d.Name + "`**")
	if d.Doc != "" {
		m.para()
		m.line(strings.TrimRight(d.Doc, "\n"))
	}
	if len(d.Enum) > 0 {
		m.para()
		m.line("One of: " + strings.Join(backtickAll(d.Enum), ", ") + ".")
	}
	if d.Deprecated != "" {
		m.para()
		m.line("**Deprecated.** " + d.Deprecated)
	}
	if d.Context != "" {
		m.para()
		m.line("Evaluated against the `" + d.Context + "` context.")
	}
}

// results renders search hits as a table, so that a search redirected to a file
// or piped somewhere arrives as something a reader can scan two columns of.
func (m *markdownSink) results(v Results) {
	if len(v.Rows) == 0 {
		return
	}
	m.para()
	if v.Intro != "" {
		m.line(v.Intro)
		m.para()
	}
	m.line("| Topic | Description |")
	m.line("|---|:---|")
	for _, r := range v.Rows {
		m.line(fmt.Sprintf("| `%s` | %s |", r.Command, resultDesc(r)))
	}
}

func (m *markdownSink) blockTable(t BlockTable) {
	if len(t.Rows) == 0 {
		return
	}
	m.para()
	for _, r := range t.Rows {
		name := "`" + blockHeader(r.Name, r.Labels) + "`"
		if r.DocPage != "" {
			name = "[" + name + "](" + r.DocPage + ")"
		} else if m.opts.LinkArgv != nil && len(r.Path) > 0 {
			if target := m.opts.LinkArgv(r.Path); target != "" {
				name = "[" + name + "](" + target + ")"
			}
		}
		parts := []string{"- " + name}
		if r.Cardinality != "" {
			parts = append(parts, "("+r.Cardinality+")")
		}
		if s := oneLine(r.Summary); s != "" {
			parts = append(parts, "— "+s)
		}
		m.line(strings.Join(parts, " "))
	}
}

func (m *markdownSink) contextTable(t ContextTable) {
	m.para()
	intro := "Fields readable as `ctx.<name>`"
	if t.Shape != "" {
		intro += " (shape `" + t.Shape + "`)"
	}
	m.line(intro + ":")
	m.para()
	m.line("| Field | Type | Description |")
	m.line("|---|---|:---|")
	for _, r := range t.Rows {
		// Annotations are collected and appended once, because a field can be
		// more than one of these at a time — a site-added field that some
		// deliveries omit is both added and optional.
		var notes []string
		switch {
		case r.Added:
			notes = append(notes, "*(added here)*")
		case r.Universal:
			notes = append(notes, "*(every `ctx` carries this)*")
		}
		if r.Optional {
			notes = append(notes, "*(not always present)*")
		}

		desc := oneLine(r.Summary)
		if len(notes) > 0 {
			desc = strings.TrimSuffix(desc, ".") + ". " + strings.Join(notes, " ")
		}
		m.line(fmt.Sprintf("| `ctx.%s` | %s | %s |", r.Name, r.Type, desc))
	}
	if t.OpenFields {
		m.para()
		m.line("*This shape is open: a particular site may carry fields beyond these.*")
	}
	// Detail for the fields that carry any, below the table — the same shape
	// as an attribute table followed by its per-attribute detail.
	for _, r := range t.Rows {
		if r.Doc == "" {
			continue
		}
		m.para()
		m.line("**`ctx." + r.Name + "`**")
		m.para()
		m.line(strings.TrimRight(r.Doc, "\n"))
	}
}

func (m *markdownSink) memberTable(t MemberTable) {
	if len(t.Rows) == 0 && !t.FreeMembers {
		return
	}
	prefix := strings.Join(t.Prefix, ".")
	// A value column only where the values are the language's rather than the
	// machine's; sys.hostname has a value here and it is not worth printing.
	values := t.Constant && t.HasValues()

	m.para()
	if len(t.Rows) > 0 {
		m.line("Readable as `" + prefix + ".<name>`:")
		m.para()
		if values {
			m.line("| Name | Type | Value | Description |")
			m.line("|---|---|---|:---|")
		} else {
			m.line("| Name | Type | Description |")
			m.line("|---|---|:---|")
		}
		for _, r := range t.Rows {
			desc := oneLine(r.Summary)
			if r.HasMembers {
				desc = strings.TrimSuffix(desc, ".") + ". *(has members of its own)*"
			}
			if values {
				val := ""
				if r.Value != "" {
					val = "`" + r.Value + "`"
				}
				m.line(fmt.Sprintf("| `%s` | %s | %s | %s |", memberPath(r), r.Type, val, desc))
				continue
			}
			m.line(fmt.Sprintf("| `%s` | %s | %s |", memberPath(r), r.Type, desc))
		}
	}
	if t.FreeMembers {
		m.para()
		m.line("*Any name may be read here: what exists is decided outside the " +
			"configuration language, so it is not listed and not checked.*")
	}
	for _, r := range t.Rows {
		if r.Doc == "" {
			continue
		}
		m.para()
		m.line("**`" + memberPath(r) + "`**")
		m.para()
		m.line(strings.TrimRight(r.Doc, "\n"))
	}
}

func (m *markdownSink) seeAlso(s SeeAlso) {
	if len(s.Items) == 0 {
		return
	}
	m.para()
	for _, l := range s.Items {
		switch {
		case l.Target != "":
			m.line("- [" + l.Text + "](" + l.Target + ")")
		case m.opts.LinkArgv != nil && len(l.Argv) > 0:
			if target := m.opts.LinkArgv(l.Argv); target != "" {
				m.line("- [" + l.Text + "](" + target + ")")
				continue
			}
			m.line("- " + l.Text)
		default:
			m.line("- " + l.Text)
		}
	}
}

// typeLabel combines the coarse value type with the hint that refines it, since
// most VCL attributes are `expression` and the hint is what says which kind.
func typeLabel(typ string, hint config.Hint) string {
	h := string(hint)
	if h == "" || h == typ {
		return typ
	}
	return typ + " (" + h + ")"
}

func clampLevel(n int) int {
	if n < 1 {
		return 1
	}
	if n > 6 {
		return 6
	}
	return n
}

// oneLine collapses a summary to a single line. Summaries are supposed to be
// one line already; this makes a stray newline a formatting non-event rather
// than a broken table.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func backtickAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = "`" + v + "`"
	}
	return out
}
