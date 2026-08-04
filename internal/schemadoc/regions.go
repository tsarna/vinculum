package schemadoc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// Generated regions inside hand-written Markdown.
//
// doc/ is not generated and should not become generated: it carries worked
// examples, the reasoning behind a design, and a syntax primer — none of which
// the schema knows. But parts of it *are* mechanically derivable, and those
// parts are where the drift has actually happened (config.md listed cron and
// signals as top-level blocks long after they became trigger types).
//
// So a page marks the derivable parts and keeps the rest:
//
//	<!-- vinculum:begin block-index client level=3 -->
//	… generated, rewritten by `vinculum schema --format markdown --update` …
//	<!-- vinculum:end block-index client -->
//
// The author chooses the granularity per section, which matters because the
// granularity is genuinely mixed: `client` wants a list of types linking out,
// while `subscription` wants its attributes in full.

// Region kinds.
const (
	// RegionBlockIndex lists a typed block's variants, linked to their pages.
	RegionBlockIndex = "block-index"
	// RegionBlockBody renders a block or variant in full.
	RegionBlockBody = "block-body"
	// RegionContext renders one `ctx` shape.
	RegionContext = "context"
)

// Region is one marked region of a document.
type Region struct {
	Kind string
	Args []string
	// Level is the heading level generated headings start at, so a region
	// sits correctly under whatever hand-written heading precedes it.
	Level int

	// beginLine and endLine are the marker lines themselves, 0-based. The
	// content between them is what gets replaced.
	beginLine, endLine int
}

// String renders the region the way its marker spells it, for diagnostics.
func (r Region) String() string {
	return strings.TrimSpace(r.Kind + " " + strings.Join(r.Args, " "))
}

var (
	beginRe = regexp.MustCompile(`^<!--\s*vinculum:begin\s+(.*?)\s*-->\s*$`)
	endRe   = regexp.MustCompile(`^<!--\s*vinculum:end\s+(.*?)\s*-->\s*$`)
)

// ParseRegions finds the marked regions in a document.
//
// A begin without an end, an end without a begin, a nested pair, or an end
// naming something other than what its begin named are all errors rather than
// best-effort recoveries: every one of them would otherwise silently swallow
// or duplicate part of a hand-written page.
func ParseRegions(src string) ([]Region, error) {
	var regions []Region
	var open *Region
	inFence := false

	for i, line := range strings.Split(src, "\n") {
		// A marker inside a fenced code block is an example of a marker, not
		// a marker. Without this, the page documenting this feature is
		// rewritten by it — its example replaced with a real generated index.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		if m := beginRe.FindStringSubmatch(line); m != nil {
			if open != nil {
				return nil, fmt.Errorf("line %d: %q begins inside %q, which has not ended",
					i+1, m[1], open.String())
			}
			r, err := parseMarker(m[1])
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			r.beginLine = i
			open = &r
			continue
		}

		m := endRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if open == nil {
			return nil, fmt.Errorf("line %d: %q ends a region that never began", i+1, m[1])
		}
		// The end marker repeats what the begin marker named, so a mismatched
		// pair is caught here rather than by swallowing everything between one
		// region's begin and another's end.
		if got, want := strings.Fields(m[1]), append([]string{open.Kind}, open.Args...); !equalStrings(got, want) {
			return nil, fmt.Errorf("line %d: %q ends %q", i+1, m[1], open.String())
		}
		open.endLine = i
		regions = append(regions, *open)
		open = nil
	}

	if open != nil {
		return nil, fmt.Errorf("line %d: %q is never ended", open.beginLine+1, open.String())
	}
	return regions, nil
}

// parseMarker reads "<kind> <arg>… [level=N]".
func parseMarker(text string) (Region, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return Region{}, fmt.Errorf("empty region marker")
	}

	r := Region{Kind: fields[0], Level: 2}
	for _, f := range fields[1:] {
		if value, ok := strings.CutPrefix(f, "level="); ok {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 6 {
				return Region{}, fmt.Errorf("bad level %q: want 1..6", value)
			}
			r.Level = n
			continue
		}
		r.Args = append(r.Args, f)
	}

	switch r.Kind {
	case RegionBlockIndex, RegionContext:
		if len(r.Args) != 1 {
			return Region{}, fmt.Errorf("%s takes one argument, got %d", r.Kind, len(r.Args))
		}
	case RegionBlockBody:
		if len(r.Args) == 0 {
			return Region{}, fmt.Errorf("%s takes a topic path", r.Kind)
		}
	default:
		return Region{}, fmt.Errorf("unknown region kind %q", r.Kind)
	}
	return r, nil
}

// RenderRegion generates the content of one region: the lines that belong
// between its markers, without a trailing newline.
func RenderRegion(doc *config.SchemaDocument, r Region) (string, error) {
	switch r.Kind {
	case RegionBlockIndex:
		block, ok := doc.Blocks[r.Args[0]]
		if !ok {
			return "", fmt.Errorf("%s: no such block type", r.Args[0])
		}
		if block.VariantLabel == "" {
			return "", fmt.Errorf("%s: not a typed block, so it has no type index", r.Args[0])
		}
		return RenderMarkdown([]Event{variantIndex(r.Args[0], block)}, MarkdownOptions{}), nil

	case RegionBlockBody:
		candidates := Resolve(doc, KindBlock, r.Args)
		if len(candidates) != 1 {
			return "", fmt.Errorf("%s: resolves to %d topics, want exactly one",
				strings.Join(r.Args, " "), len(candidates))
		}
		return RenderMarkdown(
			Walk(candidates[0], WalkOptions{BaseLevel: r.Level, NoHeading: true}),
			MarkdownOptions{}), nil

	case RegionContext:
		shape, ok := doc.Contexts[r.Args[0]]
		if !ok {
			return "", fmt.Errorf("%s: no such context shape", r.Args[0])
		}
		return RenderMarkdown(
			Walk(ContextNode(doc, r.Args[0], shape), WalkOptions{BaseLevel: r.Level, NoHeading: true}),
			MarkdownOptions{}), nil
	}
	return "", fmt.Errorf("unknown region kind %q", r.Kind)
}

// variantIndex is the linked list of a typed block's variants — the one thing
// a per-type index section consists of.
func variantIndex(blockType string, block *config.SchemaBlock) BlockTable {
	rows := make([]BlockRow, 0, len(block.Variants))
	for _, name := range sortedBodyNames(block.Variants) {
		body := block.Variants[name]
		summary := strings.TrimSpace(body.Summary)
		if body.Conditional {
			summary += " (available only in some configurations)"
		}
		rows = append(rows, BlockRow{
			Name:    blockType + ` "` + name + `"`,
			Summary: summary,
			DocPage: body.DocPage,
			Path:    []string{blockType, name},
		})
	}
	return BlockTable{Rows: rows}
}

// UpdateRegions rewrites every marked region of a document, leaving everything
// else byte for byte as it was.
//
// The second return value is the regions that changed, so a caller can report
// what is stale without diffing whole files.
func UpdateRegions(doc *config.SchemaDocument, src string) (string, []Region, error) {
	regions, err := ParseRegions(src)
	if err != nil {
		return "", nil, err
	}
	if len(regions) == 0 {
		return src, nil, nil
	}

	lines := strings.Split(src, "\n")
	var out []string
	var changed []Region
	prev := 0

	for _, r := range regions {
		content, err := RenderRegion(doc, r)
		if err != nil {
			return "", nil, fmt.Errorf("line %d: %s: %w", r.beginLine+1, r, err)
		}
		generated := strings.Split(strings.TrimRight(content, "\n"), "\n")

		existing := lines[r.beginLine+1 : r.endLine]
		if !equalStrings(trimBlankEnds(existing), generated) {
			changed = append(changed, r)
		}

		out = append(out, lines[prev:r.beginLine+1]...)
		// A blank line either side keeps the markers from running into the
		// content, which matters to Markdown and to anyone reading the source.
		out = append(out, "")
		out = append(out, generated...)
		out = append(out, "")
		prev = r.endLine
	}
	out = append(out, lines[prev:]...)

	return strings.Join(out, "\n"), changed, nil
}

// trimBlankEnds drops leading and trailing blank lines, which is what makes
// the comparison insensitive to the padding UpdateRegions itself adds.
func trimBlankEnds(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
