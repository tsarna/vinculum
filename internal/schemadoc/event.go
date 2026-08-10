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

// HasDefaults reports whether any row states a default, which is what decides
// whether a sink spends a column on them. Most bodies have none, and an empty
// column in every table would cost more than it tells.
func (t AttrTable) HasDefaults() bool {
	for _, r := range t.Rows {
		if r.Default != "" {
			return true
		}
	}
	return false
}

// AttrRow is one attribute in the overview.
type AttrRow struct {
	Name     string
	Type     string
	Required bool
	Summary  string
	// Hint is the value-completion hint, e.g. "duration" or "topic-pattern".
	Hint config.Hint
	// Default is the value used when the attribute is omitted, written as it
	// would be written in a config file. Empty when there is no default worth
	// stating.
	Default string
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
	Name string
	Type string
	// Summary is the one-line description shown in the table; Doc is the
	// detail that follows it, rendered per field below the table the way an
	// attribute's detail follows the attribute table.
	Summary  string
	Doc      string
	Optional bool
	// Universal is true for a field every `ctx` carries.
	Universal bool
	// Added is true for a field this particular site contributes to an open
	// shape, rather than one the shape itself declares.
	Added bool
}

// MemberTable lists what may follow a dot: the members of a namespace, or of a
// member that is itself an object.
//
// Distinct from ContextTable, which it resembles. A `ctx` field is readable
// only inside the expression that sees it and is spelled `ctx.<name>`; a
// namespace member is readable in every expression and is spelled with its own
// root. They also differ in what a reader needs: a member may carry a literal
// value, and may be addressable one level further down.
type MemberTable struct {
	// Prefix is the dotted path the rows are read under, e.g. ["sys"] or
	// ["sys", "signals"].
	Prefix []string
	Rows   []MemberRow
	// FreeMembers is true when Rows is a floor rather than the whole list: the
	// remaining names come from the environment or the host rather than from
	// the language.
	FreeMembers bool
	// Constant is true when the values are the same in every process, which is
	// what makes a row's Value meaningful.
	Constant bool
}

// HasValues reports whether any row states a value, which is what decides
// whether a sink spends a column on them.
func (t MemberTable) HasValues() bool {
	for _, r := range t.Rows {
		if r.Value != "" {
			return true
		}
	}
	return false
}

// MemberRow is one member in the overview.
type MemberRow struct {
	Name string
	Type string
	// Value is the member's literal value, for a namespace whose values are the
	// same in every process. Empty otherwise.
	Value string
	// Summary is the one-line description shown in the table; Doc is the detail
	// rendered below it, the way an attribute's detail follows its table.
	Summary string
	Doc     string
	// HasMembers is true when the member is an object with members of its own,
	// so a sink can say the path continues.
	HasMembers bool
	// Path is the argv that names the member, for a sink that links.
	Path []string
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

// Results is what a keyword search found: one row per hit, each naming the
// invocation that reads it.
//
// Distinct from Menu, which it resembles: a menu resolves one name that turned
// out to mean several things, and needs no descriptions because the reader
// already knows what they were looking for. A search result is the opposite —
// the reader has a word and not a topic, so the summary column is the whole
// point of the event.
type Results struct {
	// Intro is the line above the rows, e.g. `3 topics match "keep alive":`.
	Intro string
	Rows  []ResultRow
}

// ResultRow is one search hit.
type ResultRow struct {
	// Command is the pre-rendered invocation that reads the hit, spelled by the
	// caller in its own idiom the way Menu.Items are.
	Command string
	// Detail spells the matched thing when Command names its container rather
	// than it — `ctx.topic` for a field of a context shape. Empty otherwise.
	Detail string
	// Summary is the one-line description of whatever matched.
	Summary string
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
func (MemberTable) isEvent()  {}
func (Constraints) isEvent()  {}
func (SeeAlso) isEvent()      {}
func (Menu) isEvent()         {}
func (Results) isEvent()      {}
func (Example) isEvent()      {}
