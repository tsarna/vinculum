package schemadoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// WalkOptions controls a walk.
type WalkOptions struct {
	// BaseLevel is the heading level of the node's own heading. 1 for a
	// free-standing page; a generated region inside doc/config.md sets it to
	// match its surroundings.
	BaseLevel int
	// MaxDepth limits how far sub-blocks are expanded inline, counted from the
	// node being walked. Zero means unlimited. Sub-blocks past the limit are
	// still listed, with the argv that reaches them.
	MaxDepth int
	// NoHeading suppresses the node's own top-level heading, for a caller that
	// supplies its own (a doc/ region under a hand-written heading).
	NoHeading bool
}

func (o WalkOptions) baseLevel() int {
	if o.BaseLevel < 1 {
		return 1
	}
	return o.BaseLevel
}

// Walk renders one node as a flat sequence of events.
func Walk(n Node, opts WalkOptions) []Event {
	w := &walker{doc: n.Doc, opts: opts}
	level := opts.baseLevel()

	if !opts.NoHeading {
		w.emit(Heading{Level: level, Text: n.Title()})
	}
	if bc := n.Breadcrumb(); bc != "" {
		w.emit(Prose{Markdown: bc})
	}

	switch n.shape {
	case shapeBlock:
		w.walkBlock(n, level)
	case shapeVariant:
		w.walkVariant(n, level)
	case shapeNested:
		w.walkNested(n, level)
	case shapeAttr:
		w.walkAttr(n, level)
	case shapeContext:
		w.walkContext(n, level)
	}

	// The hand-written page last, after everything generated: it is where to
	// go for what the schema cannot say — worked examples, and the prose that
	// explains why rather than what.
	//
	// It gets its own heading at the page's level rather than trailing the
	// last thing emitted. A sink that indents by heading depth would otherwise
	// file the page's own footer under whatever sub-block happened to come
	// last, which reads as documentation of that sub-block.
	if page := n.DocPage(); page != "" {
		w.emit(Heading{Level: level + 1, Text: "See also"})
		w.emit(SeeAlso{Items: []Link{{Text: page, Target: page}}})
	}
	return w.events
}

type walker struct {
	doc    *config.SchemaDocument
	opts   WalkOptions
	events []Event
}

func (w *walker) emit(e Event) { w.events = append(w.events, e) }

// describe emits the curated summary and documentation of a node, in that
// order. Both are optional; a body with neither gets a note instead, so a
// coverage gap is visible rather than blank.
func (w *walker) describe(summary, doc string, undocumented bool) {
	if summary != "" {
		w.emit(Prose{Markdown: summary})
	}
	if doc != "" {
		w.emit(Prose{Markdown: doc})
	}
	if undocumented && summary == "" && doc == "" {
		w.emit(Note{Text: "This type carries no curated documentation yet."})
	}
}

// walkBlock renders a top-level block. A typed block has no body of its own —
// its content is the variant list — while a plain block inlines one.
func (w *walker) walkBlock(n Node, level int) {
	block := n.block

	if block.VariantLabel == "" {
		if block.Body != nil {
			w.emit(synopsisFor(blockHeader(n.Path[0], block.Labels), block.Body))
		}
		w.describe(n.Summary(), n.Description(), block.Undocumented)
		if block.Body != nil {
			w.walkBody(n.Path, block.Body, level, 1)
		}
		return
	}

	w.emit(Synopsis{Lines: []string{
		blockHeader(n.Path[0], block.Labels) + " {",
		"    # attributes depend on the " + block.VariantLabel + " label; see below",
		"}",
	}})
	w.describe(n.Summary(), n.Description(), block.Undocumented)

	rows := make([]BlockRow, 0, len(block.Variants))
	for _, name := range sortedBodyNames(block.Variants) {
		body := block.Variants[name]
		summary := body.Summary
		if body.Conditional {
			summary = strings.TrimRight(summary, " ") + " (available only in some configurations)"
		}
		rows = append(rows, BlockRow{
			Name:    name,
			Summary: strings.TrimSpace(summary),
			Path:    []string{n.Path[0], name},
			DocPage: body.DocPage,
		})
	}
	w.emit(Heading{Level: level + 1, Text: "Types"})
	w.emit(BlockTable{Rows: rows})
}

// walkVariant renders one type-variant body: `client "mqtt"`.
func (w *walker) walkVariant(n Node, level int) {
	w.emit(synopsisFor(variantHeader(n.Path[0], n.Path[1], n.labels), n.body))
	if n.body.Conditional {
		w.emit(Note{Text: "This type is only available in configurations that enable it."})
	}
	w.describe(n.Summary(), n.Description(), n.body.Undocumented)
	w.walkBody(n.Path, n.body, level, 1)
}

// walkNested renders a sub-block reached directly.
func (w *walker) walkNested(n Node, level int) {
	body := &n.nested.SchemaBody
	w.emit(synopsisFor(blockHeader(n.Path[len(n.Path)-1], n.nested.Labels), body))
	// Where it appears is already on the breadcrumb line; this says how often.
	w.emit(Note{Text: cardinalitySentence(n.nested)})
	w.describe(n.Summary(), n.Description(), body.Undocumented)
	w.walkBody(n.Path, body, level, 1)
}

// walkAttr renders a single attribute, with the `ctx` shape it names inlined —
// an attribute's context is most of what a reader came for.
func (w *walker) walkAttr(n Node, level int) {
	a := n.attr
	w.emit(AttrTable{Rows: []AttrRow{attrRow(a)}})
	w.describe(a.Summary, a.Doc, false)
	if a.Deprecated != "" {
		w.emit(Note{Text: "Deprecated: " + a.Deprecated})
	}
	if len(a.Enum) > 0 {
		w.emit(Note{Text: "One of: " + strings.Join(quoteAll(a.Enum), ", ") + "."})
	}
	w.emitContext(a, level+1)
}

// walkContext renders a `ctx` shape on its own.
func (w *walker) walkContext(n Node, level int) {
	w.describe(n.Summary(), n.Description(), false)
	w.emit(contextTable(n.Path[0], n.ctx, nil))

	if users := w.contextUsers(n.Path[0]); len(users.Items) > 0 {
		w.emit(Heading{Level: level + 1, Text: "Evaluated by"})
		w.emit(users)
	}
}

// walkBody renders the members of a body: its attributes, its constraints, and
// its sub-blocks — recursively, until MaxDepth.
func (w *walker) walkBody(path []string, body *config.SchemaBody, level, depth int) {
	if body.FreeAttributes {
		w.emit(Note{Text: "Attribute names here are chosen by you rather than fixed by the parser."})
	}

	if len(body.Attributes) > 0 {
		rows := make([]AttrRow, 0, len(body.Attributes))
		required, optional := splitByRequired(body.Attributes)
		for _, a := range append(append([]*config.SchemaAttr{}, required...), optional...) {
			rows = append(rows, attrRow(a))
		}
		w.emit(Heading{Level: level + 1, Text: "Attributes"})
		w.emit(AttrTable{Rows: rows})

		for _, a := range append(append([]*config.SchemaAttr{}, required...), optional...) {
			if a.Doc == "" && len(a.Enum) == 0 && a.Deprecated == "" && a.Context == "" {
				continue // the overview row said everything there is to say
			}
			w.emit(AttrDetail{
				Name: a.Name, Type: a.Type, Doc: a.Doc,
				Enum: a.Enum, Deprecated: a.Deprecated, Context: a.Context,
			})
		}
	}

	if len(body.Constraints) > 0 {
		w.emit(Constraints{Items: body.Constraints})
	}

	w.emitBodyContexts(body, level+1)

	if len(body.Blocks) == 0 {
		return
	}

	// Past the depth limit, sub-blocks are listed rather than expanded, with
	// the argv that reaches each — a pointer, not a dead end.
	if w.opts.MaxDepth > 0 && depth >= w.opts.MaxDepth {
		rows := make([]BlockRow, 0, len(body.Blocks))
		for _, name := range sortedBlockNames(body.Blocks) {
			nested := body.Blocks[name]
			rows = append(rows, BlockRow{
				Name: name, Labels: nested.Labels, Cardinality: cardinality(nested),
				Summary: nested.Summary, Path: append(append([]string{}, path...), name),
			})
		}
		w.emit(Heading{Level: level + 1, Text: "Blocks"})
		w.emit(BlockTable{Rows: rows})
		return
	}

	for _, name := range sortedBlockNames(body.Blocks) {
		nested := body.Blocks[name]
		sub := append(append([]string{}, path...), name)
		w.emit(Heading{Level: level + 1, Text: "`" + blockHeader(name, nested.Labels) + "`"})
		w.emit(Note{Text: cardinalitySentence(nested)})
		w.describe(nested.Summary, nested.Doc, nested.Undocumented)
		w.walkBody(sub, &nested.SchemaBody, level+1, depth+1)
	}
}

// emitBodyContexts inlines every distinct `ctx` shape the body's own attributes
// name. Shapes are collected across the body rather than repeated per
// attribute, because several attributes usually share one.
func (w *walker) emitBodyContexts(body *config.SchemaBody, level int) {
	seen := map[string]bool{}
	var order []*config.SchemaAttr
	for _, a := range body.Attributes {
		if a.Context == "" || seen[a.Context] {
			continue
		}
		// A site's own added fields make its shape distinct from a sibling's,
		// so an attribute carrying them is always emitted separately.
		if len(a.ContextFields) == 0 {
			seen[a.Context] = true
		}
		order = append(order, a)
	}
	if len(order) == 0 {
		return
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Name < order[j].Name })
	for _, a := range order {
		w.emitContext(a, level)
	}
}

// emitContext inlines the shape one attribute's expression is evaluated
// against, including whatever fields that site adds to an open shape.
func (w *walker) emitContext(a *config.SchemaAttr, level int) {
	if a.Context == "" || w.doc == nil {
		return
	}
	shape, ok := w.doc.Contexts[a.Context]
	if !ok {
		return
	}
	w.emit(Heading{Level: level, Text: "`ctx` in `" + a.Name + "`"})
	w.emit(contextTable(a.Context, shape, a.ContextFields))
}

// contextUsers lists the attributes across the document that are evaluated
// against a shape — the answer to "where does this ctx come from".
func (w *walker) contextUsers(name string) SeeAlso {
	var items []Link
	if w.doc == nil {
		return SeeAlso{}
	}
	for _, blockType := range sortedBlockKeys(w.doc.Blocks) {
		block := w.doc.Blocks[blockType]
		if block.Body != nil {
			items = append(items, contextUsersIn([]string{blockType}, block.Body, name)...)
		}
		for _, variant := range sortedBodyNames(block.Variants) {
			items = append(items, contextUsersIn([]string{blockType, variant}, block.Variants[variant], name)...)
		}
	}
	return SeeAlso{Items: items}
}

func contextUsersIn(path []string, body *config.SchemaBody, name string) []Link {
	var items []Link
	for _, a := range body.Attributes {
		if a.Context != name {
			continue
		}
		argv := append(append([]string{}, path...), a.Name)
		items = append(items, Link{Text: pathSpelling(path) + " › `" + a.Name + "`", Argv: argv})
	}
	for _, sub := range sortedBlockNames(body.Blocks) {
		items = append(items, contextUsersIn(append(append([]string{}, path...), sub), &body.Blocks[sub].SchemaBody, name)...)
	}
	return items
}

// contextTable renders a shape's fields, with any site-specific additions
// appended and marked as such.
func contextTable(shape string, cs *config.SchemaContext, added []*config.SchemaContextField) ContextTable {
	t := ContextTable{Shape: shape, Summary: cs.Summary, OpenFields: cs.OpenFields}
	for _, f := range cs.Fields {
		t.Rows = append(t.Rows, ContextRow{
			Name: f.Name, Type: f.Type, Summary: f.Summary,
			Optional: f.Optional, Universal: f.Universal,
		})
	}
	for _, f := range added {
		t.Rows = append(t.Rows, ContextRow{
			Name: f.Name, Type: f.Type, Summary: f.Summary,
			Optional: f.Optional, Added: true,
		})
	}
	return t
}

func attrRow(a *config.SchemaAttr) AttrRow {
	return AttrRow{
		Name: a.Name, Type: a.Type, Required: a.Required,
		Summary: a.Summary, Hint: a.Hint, Deprecated: a.Deprecated,
	}
}

// ConstraintText renders one advisory rule as a sentence. The schema's own
// generated Message is preferred; this is the fallback for a constraint that
// carries none.
func ConstraintText(c config.Constraint) string {
	if c.Message != "" {
		return c.Message
	}
	attrs := make([]string, len(c.Attributes))
	for i, a := range c.Attributes {
		attrs[i] = "`" + a + "`"
	}
	switch c.Kind {
	case config.ConstraintMutuallyExclusive:
		return "Specify at most one of " + strings.Join(attrs, ", ") + "."
	case config.ConstraintAtLeastOneOf:
		return "Specify at least one of " + strings.Join(attrs, ", ") + "."
	case config.ConstraintRequiredTogether:
		return strings.Join(attrs, ", ") + " must be specified together."
	case config.ConstraintRequires:
		return attrs[0] + " requires " + strings.Join(attrs[1:], ", ") + "."
	}
	return fmt.Sprintf("%s: %s", c.Kind, strings.Join(attrs, ", "))
}

func sortedBodyNames(m map[string]*config.SchemaBody) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func sortedBlockKeys(m map[string]*config.SchemaBlock) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
