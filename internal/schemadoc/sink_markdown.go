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

	case Preformatted:
		m.para()
		// No language: this is a rendered signature, not source in any
		// language a highlighter would improve.
		m.line("```")
		m.line(strings.TrimRight(v.Text, "\n"))
		m.line("```")

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
	m.para()
	m.line("| Attribute | Type | Required | Description |")
	m.line("|---|---|---|:---|")
	for _, r := range t.Rows {
		req := ""
		if r.Required {
			req = "yes"
		}
		desc := oneLine(r.Summary)
		if r.Deprecated != "" {
			desc = "**Deprecated.** " + desc
		}
		m.line(fmt.Sprintf("| `%s` | %s | %s | %s |", r.Name, typeLabel(r.Type, r.Hint), req, desc))
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
		desc := oneLine(r.Summary)
		switch {
		case r.Added:
			desc = strings.TrimSuffix(desc, ".") + ". *(added here)*"
		case r.Universal:
			desc = strings.TrimSuffix(desc, ".") + ". *(every `ctx` carries this)*"
		}
		if r.Optional {
			desc = strings.TrimSuffix(desc, ".") + ". *(not always present)*"
		}
		m.line(fmt.Sprintf("| `ctx.%s` | %s | %s |", r.Name, r.Type, desc))
	}
	if t.OpenFields {
		m.para()
		m.line("*This shape is open: a particular site may carry fields beyond these.*")
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
