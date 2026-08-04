package schemadoc

import (
	"strings"

	"github.com/tsarna/vinculum/config"
)

// Index renders the front page: what topics exist and how to reach one.
//
// It lists blocks and `ctx` shapes but not type variants, which are listed
// under the block they belong to. 43 variants would bury the 15 blocks a
// reader arriving with no particular question is looking for.
func Index(doc *config.SchemaDocument, opts WalkOptions) []Event {
	if doc == nil {
		return nil
	}
	level := opts.baseLevel()
	var events []Event

	if !opts.NoHeading {
		events = append(events, Heading{Level: level, Text: "Vinculum configuration language"})
	}
	events = append(events, Prose{
		Markdown: "Reference for the block language, generated from the same decode " +
			"structs the parser uses — it describes exactly what this binary can parse.",
	})

	blocks := Topics(doc, KindBlock)
	if len(blocks) > 0 {
		events = append(events, Heading{Level: level + 1, Text: "Blocks"})
		events = append(events, BlockTable{Rows: topicRows(blocks)})
	}

	shapes := Topics(doc, KindContext)
	if len(shapes) > 0 {
		events = append(events, Heading{Level: level + 1, Text: "Context shapes"})
		events = append(events, Prose{
			Markdown: "The shapes of `ctx` that an expression may be evaluated against. " +
				"Each is named by the attributes that see it.",
		})
		events = append(events, BlockTable{Rows: topicRows(shapes)})
	}

	return events
}

func topicRows(nodes []Node) []BlockRow {
	rows := make([]BlockRow, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, BlockRow{
			Name:    n.Path[len(n.Path)-1],
			Summary: n.Summary(),
			Path:    n.Path,
		})
	}
	return rows
}

// Everything renders the whole language as one document: the index, then every
// block, every type variant, and every `ctx` shape.
//
// It exists so that `--format markdown` means something without a file to
// update — a single-page reference to pipe somewhere, or to diff between two
// releases to see what the language gained.
func Everything(doc *config.SchemaDocument, opts WalkOptions) []Event {
	if doc == nil {
		return nil
	}
	level := opts.baseLevel()
	events := Index(doc, opts)

	events = append(events, Heading{Level: level + 1, Text: "Blocks"})
	for _, block := range Topics(doc, KindBlock) {
		events = append(events, Walk(block, WalkOptions{BaseLevel: level + 2, MaxDepth: opts.MaxDepth})...)
		for _, variant := range sortedBodyNames(block.block.Variants) {
			node := VariantNode(doc, block.Path[0], variant, block.block, block.block.Variants[variant])
			events = append(events, Walk(node, WalkOptions{BaseLevel: level + 3, MaxDepth: opts.MaxDepth})...)
		}
	}

	events = append(events, Heading{Level: level + 1, Text: "Context shapes"})
	for _, shape := range Topics(doc, KindContext) {
		events = append(events, Walk(shape, WalkOptions{BaseLevel: level + 2})...)
	}
	return events
}

// Usage renders the short "how to use this" footer that follows a topic index
// or a failed lookup, in the idiom of one front door.
//
// examples are complete invocations; a caller passes the ones that make sense
// where it is printing.
func Usage(examples ...string) Prose {
	var b strings.Builder
	b.WriteString("Give a topic to read it:\n")
	for _, e := range examples {
		b.WriteString("\n    ")
		b.WriteString(e)
	}
	return Prose{Markdown: b.String()}
}
