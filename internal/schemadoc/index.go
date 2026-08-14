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

	// The two languages are listed apart. Resolution, completion, and search
	// treat them alike — a topic is a topic — but a reader scanning the index
	// for what may go in a config file is asking a question the `.vinit` blocks
	// are not an answer to, and one table cannot say so.
	blocks, bootstrap := partitionByFile(Topics(doc, KindBlock))
	if len(blocks) > 0 {
		events = append(events, Heading{Level: level + 1, Text: "Blocks"})
		events = append(events, BlockTable{Rows: topicRows(blocks)})
	}
	if len(bootstrap) > 0 {
		events = append(events, Heading{Level: level + 1, Text: "Bootstrap blocks (`.vinit`)"})
		events = append(events, Prose{
			Markdown: "Blocks of the `.vinit` bootstrap format, processed before any `.vcl` file " +
				"is parsed. They are written in a separate file, in a much smaller expression " +
				"language — see [vinit.md](vinit.md).",
		})
		events = append(events, BlockTable{Rows: topicRows(bootstrap)})
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

	namespaces := Topics(doc, KindNamespace)
	if len(namespaces) > 0 {
		prose := "The names an expression may start from, wherever it appears."
		// The second sentence exists to explain an absence — the roots a block
		// publishes are not listed here — so it is worth saying only where
		// there are any. A document of `.vinit` alone has none.
		if hasBlockNamespace(doc) {
			prose += " The blocks above publish names of their own — `bus.<name>`, " +
				"`var.<name>` — which are documented with the block that declares them."
		}
		events = append(events, Heading{Level: level + 1, Text: "Namespaces"})
		events = append(events, Prose{Markdown: prose})
		events = append(events, BlockTable{Rows: topicRows(namespaces)})
	}

	return events
}

// hasBlockNamespace reports whether any namespace's members come from blocks
// the config declares, as `bus.<name>` does.
func hasBlockNamespace(doc *config.SchemaDocument) bool {
	for _, ns := range doc.Namespaces {
		if ns.Kind == config.NamespaceBlock {
			return true
		}
	}
	return false
}

// partitionByFile splits block topics into the .vcl blocks and the .vinit ones,
// each keeping the order it arrived in.
func partitionByFile(nodes []Node) (vcl, vinit []Node) {
	for _, n := range nodes {
		if n.fileKind() == config.FileVinit {
			vinit = append(vinit, n)
			continue
		}
		vcl = append(vcl, n)
	}
	return vcl, vinit
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

	events = append(events, Heading{Level: level + 1, Text: "Namespaces"})
	for _, ns := range Topics(doc, KindNamespace) {
		events = append(events, Walk(ns, WalkOptions{BaseLevel: level + 2})...)
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
