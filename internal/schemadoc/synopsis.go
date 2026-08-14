package schemadoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// synopsisMaxAttrs caps how many attributes a synopsis lists before it says how
// many it left out. A synopsis that fills a screen is not a synopsis; the full
// list follows immediately below it in the attribute section.
const synopsisMaxAttrs = 12

// synopsisFor renders the HCL skeleton of a body: the header line the config
// author would type, required attributes, then optional ones, then sub-blocks.
//
// header is the block header without the trailing brace, e.g.
// `client "mqtt" "<name>"`. An empty header renders the body alone, which is
// what a free-standing sub-block wants.
func synopsisFor(header string, body *config.SchemaBody) Synopsis {
	var lines []string
	lines = append(lines, header+" {")

	required, optional := splitByRequired(body.Attributes)
	names := append(append([]*config.SchemaAttr{}, required...), optional...)

	width := 0
	shown := names
	if len(shown) > synopsisMaxAttrs {
		shown = shown[:synopsisMaxAttrs]
	}
	for _, a := range shown {
		if len(a.Name) > width {
			width = len(a.Name)
		}
	}

	for _, a := range shown {
		// Only what departs from the default is annotated. Optional is the
		// majority case — twelve of thirteen attributes on a condition block —
		// and marking it says nothing while burying the two marks that do.
		var notes []string
		if a.Required {
			notes = append(notes, "required")
		}
		if a.Deprecated != "" {
			notes = append(notes, "deprecated")
		}
		comment := ""
		if len(notes) > 0 {
			comment = "  # " + strings.Join(notes, ", ")
		}
		lines = append(lines, strings.TrimRight(
			fmt.Sprintf("    %-*s = %s%s", width, a.Name, synopsisValue(a), comment), " "))
	}
	if n := len(names) - len(shown); n > 0 {
		lines = append(lines, fmt.Sprintf("    # … %d more attribute%s", n, plural(n)))
	}

	if body.FreeAttributes {
		lines = append(lines, "    # … attribute names are yours to choose")
	}

	for _, name := range sortedBlockNames(body.Blocks) {
		nested := body.Blocks[name]
		lines = append(lines, "")
		lines = append(lines, "    "+blockHeader(name, nested.Labels)+" { … }  # "+cardinality(nested))
	}

	lines = append(lines, "}")
	return Synopsis{Lines: lines}
}

// splitByRequired partitions attributes into required and optional, each sorted
// by name. Required first is how a reader scans a synopsis: what must I set.
func splitByRequired(attrs []*config.SchemaAttr) (required, optional []*config.SchemaAttr) {
	for _, a := range attrs {
		if a.Required {
			required = append(required, a)
		} else {
			optional = append(optional, a)
		}
	}
	byName := func(s []*config.SchemaAttr) {
		sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
	}
	byName(required)
	byName(optional)
	return required, optional
}

// synopsisValue is the placeholder shown after `=`. The coarse type is more
// informative than an invented literal would be, and the hint refines it where
// the schema has one worth showing.
//
// The hint describes the *element* — `brokers` is a list of URLs, not a URL —
// so a list type re-wraps whatever the hint produced rather than replacing it.
func synopsisValue(a *config.SchemaAttr) string {
	v := synopsisScalar(a)
	if a.Type == "list" && v != a.Type {
		return "[" + v + "]"
	}
	return v
}

func synopsisScalar(a *config.SchemaAttr) string {
	switch a.Hint {
	case config.HintDuration:
		return `"5s"`
	case config.HintListenAddr:
		return `":8080"`
	case config.HintURL:
		return `"https://…"`
	case config.HintCronExpr:
		return `"0 * * * *"`
	case config.HintBusRef:
		return "bus.<name>"
	case config.HintClientRef:
		return "client.<name>"
	case config.HintServerRef:
		return "server.<name>"
	case config.HintMetricRef:
		return "metric.<name>"
	case config.HintVarRef:
		return "var.<name>"
	}
	if len(a.Enum) > 0 {
		return strings.Join(quoteAll(a.Enum), " | ")
	}
	return a.Type
}

// blockHeader renders a block's header: its type followed by a placeholder per
// label, e.g. `handle "<route>"`.
func blockHeader(name string, labels []string) string {
	var b strings.Builder
	b.WriteString(name)
	for _, l := range labels {
		b.WriteString(` "<`)
		b.WriteString(l)
		b.WriteString(`>"`)
	}
	return b.String()
}

// variantHeader renders the header of a typed block's variant, e.g.
// `client "mqtt" "<name>"`. The first label is the variant selector and so is
// spelled literally; the rest are placeholders.
func variantHeader(blockType, variant string, labels []string) string {
	var b strings.Builder
	b.WriteString(blockType)
	b.WriteString(` "`)
	b.WriteString(variant)
	b.WriteString(`"`)
	for _, l := range labels[min(1, len(labels)):] {
		b.WriteString(` "<`)
		b.WriteString(l)
		b.WriteString(`>"`)
	}
	return b.String()
}

// cardinality renders how many times a sub-block may appear.
func cardinality(b *config.SchemaNestedBlock) string {
	switch {
	case b.Repeatable && b.Required:
		return "1..n"
	case b.Repeatable:
		return "0..n"
	case b.Required:
		return "required"
	default:
		return "optional"
	}
}

// cardinalitySentence says how many times a sub-block may appear, as a
// sentence for a Note rather than the terse form a synopsis comment wants.
func cardinalitySentence(b *config.SchemaNestedBlock) string {
	switch {
	case b.Repeatable && b.Required:
		return "Required; one or more."
	case b.Repeatable:
		return "May appear any number of times."
	case b.Required:
		return "Required; exactly one."
	default:
		return "Optional; at most one."
	}
}

func sortedBlockNames(m map[string]*config.SchemaNestedBlock) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func quoteAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = `"` + v + `"`
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
