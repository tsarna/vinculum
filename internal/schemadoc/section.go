package schemadoc

import (
	"fmt"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// Sections: rendering one part of a node rather than all of it.
//
// Walk renders a node whole — synopsis, description, attributes, contexts,
// sub-blocks — which is what a `vinculum man` page wants. A hand-written page
// wants something narrower. doc/config.md's `subscription` section has a
// hand-tuned synopsis whose inline comments (`queue_size = 100  # optional`)
// say more than the generated one can, and three worked examples the schema
// knows nothing about. What it does *not* want to maintain by hand is the
// attribute table and the `ctx` field list, which is exactly what drifts.
//
// So a page takes the derivable part and keeps the rest:
//
//	#### Attributes
//	<!-- vinculum:begin block-attrs subscription -->
//	<!-- vinculum:end block-attrs subscription -->
//
// A section emits no heading of its own — the hand-written heading above the
// region is the section, which is the whole point of embedding rather than
// generating the page.

// Section names one part of a node's documentation.
type Section string

const (
	// SectionSynopsis is the HCL skeleton alone.
	SectionSynopsis Section = "synopsis"
	// SectionAttrs is the attribute table, the per-attribute detail, and the
	// body's constraints — everything about the body's own attributes, and
	// nothing about its `ctx` shapes or the contents of its sub-blocks.
	SectionAttrs Section = "attrs"
	// SectionCtx is the `ctx` field table for one attribute, including the
	// fields that attribute's own site adds to an open shape.
	SectionCtx Section = "ctx"
)

// WalkSection renders one section of a node.
//
// Unlike Walk, this reports an error rather than emitting nothing when the
// section does not apply: a region naming a section the node does not have is
// a mistake in the page, and silence would leave a hand-written heading
// standing over a blank space.
func WalkSection(n Node, sec Section, opts WalkOptions) ([]Event, error) {
	w := &walker{doc: n.Doc, opts: opts}
	level := opts.baseLevel()

	switch sec {
	case SectionSynopsis:
		syn, ok := synopsisOf(n)
		if !ok {
			return nil, fmt.Errorf("%s has no synopsis: it has no body of its own", pathText(n))
		}
		w.emit(syn)

	case SectionAttrs:
		body := n.bodyOf()
		if body == nil {
			return nil, fmt.Errorf("%s has no attributes: it has no body of its own", pathText(n))
		}
		if !w.walkAttrsOnly(n.Path, body, level) {
			return nil, fmt.Errorf("%s declares no attributes", pathText(n))
		}

	case SectionCtx:
		if n.shape != shapeAttr {
			return nil, fmt.Errorf("%s is not an attribute, so it has no ctx", pathText(n))
		}
		if n.attr.Context == "" {
			return nil, fmt.Errorf("`%s` is not evaluated against a ctx", n.attr.Name)
		}
		shape, ok := n.Doc.Contexts[n.attr.Context]
		if !ok {
			return nil, fmt.Errorf("`%s` names ctx shape %q, which the document does not describe",
				n.attr.Name, n.attr.Context)
		}
		w.emit(contextTable(n.attr.Context, shape, n.attr.ContextFields))

	default:
		return nil, fmt.Errorf("unknown section %q", sec)
	}
	return w.events, nil
}

// walkAttrsOnly emits a body's attributes and constraints, and reports whether
// there was anything to emit.
//
// Sub-blocks are listed rather than expanded. Dropping them silently would let
// a page document `client "mqtt"` while never mentioning that it accepts a
// `tls` block, which is the same class of omission this whole feature exists
// to prevent — so they get a pointer to the region that renders them.
func (w *walker) walkAttrsOnly(path []string, body *config.SchemaBody, level int) bool {
	emitted := false

	if body.FreeAttributes {
		w.emit(Note{Text: "Attribute names here are chosen by you rather than fixed by the parser."})
		emitted = true
	}

	if w.emitAttrs(body) {
		emitted = true
	}

	if len(body.Blocks) > 0 {
		rows := make([]BlockRow, 0, len(body.Blocks))
		for _, name := range sortedBlockNames(body.Blocks) {
			nested := body.Blocks[name]
			rows = append(rows, BlockRow{
				Name: name, Labels: nested.Labels, Cardinality: cardinality(nested),
				Summary: nested.Summary, Path: append(append([]string{}, path...), name),
			})
		}
		w.emit(Heading{Level: level, Text: "Blocks"})
		w.emit(BlockTable{Rows: rows})
		emitted = true
	}

	return emitted
}

// orderedAttrs is the order attributes are documented in: required first, then
// optional, each alphabetically — a reader scanning for what they must supply
// finds it at the top.
func orderedAttrs(body *config.SchemaBody) []*config.SchemaAttr {
	required, optional := splitByRequired(body.Attributes)
	return append(append([]*config.SchemaAttr{}, required...), optional...)
}

// synopsisOf is the skeleton a node would show at the top of its own page.
func synopsisOf(n Node) (Synopsis, bool) {
	switch n.shape {
	case shapeBlock:
		if n.block.VariantLabel != "" {
			return Synopsis{Lines: []string{
				blockHeader(n.Path[0], n.block.Labels) + " {",
				"    # attributes depend on the " + n.block.VariantLabel + " label; see below",
				"}",
			}}, true
		}
		if n.block.Body == nil {
			return Synopsis{}, false
		}
		return synopsisFor(blockHeader(n.Path[0], n.block.Labels), n.block.Body), true
	case shapeVariant:
		return synopsisFor(variantHeader(n.Path[0], n.Path[1], n.labels), n.body), true
	case shapeNested:
		return synopsisFor(blockHeader(n.Path[len(n.Path)-1], n.nested.Labels), &n.nested.SchemaBody), true
	}
	return Synopsis{}, false
}

// pathText spells a node's path for a diagnostic, without Markdown backticks —
// these errors are read on a terminal, not rendered.
func pathText(n Node) string {
	return `"` + strings.Join(n.Path, " ") + `"`
}
