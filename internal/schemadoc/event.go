package schemadoc

import "github.com/tsarna/vinculum/config"

// Event is one unit of rendered documentation. A walk over a Node produces a
// flat sequence of these, and each sink decides how to present them: the
// Markdown sink emits GFM, the terminal sink emits ANSI wrapped to the
// terminal width, the plain sink emits neither.
//
// The vocabulary is deliberately small and specific to what the schema
// contains. It is not a general document model, and it should not grow one: a
// new event is warranted when the schema gains a new kind of fact, not when a
// sink wants a new visual treatment.
type Event interface {
	isEvent()
}

// Heading starts a section. Level is absolute, already offset by the walk's
// base level, so a sink never has to know where in a host document it sits.
type Heading struct {
	Level int
	Text  string
}

// Synopsis is an HCL skeleton of a block body: its header line, its attributes
// with their coarse types, and its sub-blocks. Lines are pre-rendered and
// already indented; a sink emits them verbatim inside a code block.
type Synopsis struct {
	Lines []string
}

// Prose is curated documentation, as Markdown. It is the only event whose
// content is authored by hand rather than derived, and so the only one a
// non-Markdown sink has to interpret rather than format.
type Prose struct {
	Markdown string
}

// Note is a short generated remark about the shape of a body — that its
// attribute names are free-form, that a variant is conditionally available,
// that a block carries no curated documentation yet. Distinct from Prose
// because it is generated, so a sink may style it as an aside.
type Note struct {
	Text string
}

// AttrTable is the compact overview of a body's attributes: one line each.
// Attributes whose documentation does not fit on that line additionally get an
// AttrDetail.
//
// Tables carry no title of their own; a preceding Heading names them, so a
// sink has one mechanism for section naming rather than two.
type AttrTable struct {
	Rows []AttrRow
}

// AttrRow is one attribute in the overview.
type AttrRow struct {
	Name     string
	Type     string
	Required bool
	Summary  string
	// Hint is the value-completion hint, e.g. "duration" or "topic-pattern".
	Hint config.Hint
	// Deprecated is non-empty when the attribute is deprecated.
	Deprecated string
}

// AttrDetail is the full documentation of one attribute: everything the
// overview row could not carry.
type AttrDetail struct {
	Name string
	Type string
	// Doc is the attribute's rich Markdown.
	Doc string
	// Enum is the closed set of legal values, if any.
	Enum []string
	// Deprecated is non-empty when the attribute is deprecated.
	Deprecated string
	// Context names the `ctx` shape this attribute's expression sees.
	Context string
}

// BlockTable lists sub-blocks or type variants — anything addressable one level
// down from the current node.
type BlockTable struct {
	Rows []BlockRow
}

// BlockRow is one entry in a BlockTable.
type BlockRow struct {
	Name string
	// Labels are the entry's label names, e.g. ["route"].
	Labels []string
	// Cardinality is a rendered "required" / "optional" / "0..n".
	Cardinality string
	Summary     string
	// Path is the argv that names the entry, for a sink that links.
	Path []string
	// DocPage is the hand-written reference page for the entry, if it has one.
	DocPage string
}

// ContextTable describes the `ctx` an expression is evaluated against.
type ContextTable struct {
	// Shape is the shape's name, e.g. "message".
	Shape   string
	Summary string
	Rows    []ContextRow
	// OpenFields is true when Rows is a floor rather than the whole list.
	OpenFields bool
}

// ContextRow is one field readable as ctx.<name>.
type ContextRow struct {
	Name     string
	Type     string
	Summary  string
	Optional bool
	// Universal is true for a field every `ctx` carries.
	Universal bool
	// Added is true for a field this particular site contributes to an open
	// shape, rather than one the shape itself declares.
	Added bool
}

// Constraints are the advisory cross-attribute rules of a body.
type Constraints struct {
	Items []config.Constraint
}

// SeeAlso is a list of cross-references: the hand-written reference page for a
// type, its sibling variants, the `ctx` shapes its attributes name.
type SeeAlso struct {
	Items []Link
}

// Link is one cross-reference. Target is a doc/ page (possibly with a
// #fragment) when the link leaves the generated document, and empty when the
// reference is only nameable as an argv.
type Link struct {
	Text   string
	Target string
	// Argv is the `vinculum man` path that reaches the target, if any.
	Argv []string
}

// Menu is a disambiguation list: the intro line and the exact commands that
// each name one candidate. The commands are pre-rendered by the caller so the
// same walk serves `vinculum man`, `:man`, and `help()`, which spell them
// differently.
type Menu struct {
	Intro string
	Items []string
}

// Preformatted is text that is already laid out and must not be re-wrapped —
// currently a function's help as functy renders it, whose alignment carries the
// calling convention.
//
// Distinct from Synopsis, which is an HCL skeleton this package generated and a
// sink may syntax-colour; this is opaque text from elsewhere.
type Preformatted struct {
	Text string
}

// Example is a worked configuration example. Reserved: the schema carries no
// structured examples yet (see SCHEMA-OUTPUT-SPEC.md), and this exists so that
// when it does, adding them is a walk change rather than a vocabulary change.
type Example struct {
	Caption string
	Source  string
}

func (Heading) isEvent()      {}
func (Synopsis) isEvent()     {}
func (Prose) isEvent()        {}
func (Note) isEvent()         {}
func (AttrTable) isEvent()    {}
func (AttrDetail) isEvent()   {}
func (BlockTable) isEvent()   {}
func (ContextTable) isEvent() {}
func (Constraints) isEvent()  {}
func (SeeAlso) isEvent()      {}
func (Menu) isEvent()         {}
func (Preformatted) isEvent() {}
func (Example) isEvent()      {}
