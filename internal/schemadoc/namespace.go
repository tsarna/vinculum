package schemadoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// Namespace topics: the names an expression may start from, and the members
// reached through them.
//
// Unlike a `ctx` shape, a namespace has members that are addressable in their
// own right — `vinculum man sys pid` is a page — so resolution descends rather
// than refusing a path longer than one element.
//
// Only provider namespaces are topics. See KindNamespace for why the roots a
// config fills in are not.

// isTopicNamespace reports whether a namespace is addressable as a topic.
func isTopicNamespace(ns *config.SchemaNamespace) bool {
	return ns != nil && ns.Kind != config.NamespaceBlock
}

// resolveNamespace resolves a namespace name and, if the path continues, the
// member it names.
//
// Descent follows described members only. That is the whole list for a fixed
// namespace, and for a free one it is exactly the part that is the language's
// to know: `sys signals bynumber` resolves, `sys signals SIGUSR1` does not,
// because which signals exist is the host's business rather than the schema's.
func resolveNamespace(doc *config.SchemaDocument, path []string) []Node {
	ns, ok := doc.Namespaces[path[0]]
	if !ok || !isTopicNamespace(ns) {
		return nil
	}

	self := NamespaceNode(doc, path[0], ns)
	members := ns.Members
	for i, name := range path[1:] {
		m := findMember(members, name)
		if m == nil {
			return nil
		}
		self = MemberNode(doc, path[:i+2], ns, m)
		members = m.Members
	}
	return []Node{self}
}

// findMember returns the named member, or nil.
func findMember(members []*config.SchemaMember, name string) *config.SchemaMember {
	for _, m := range members {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// namespaceTopics returns the addressable namespaces, sorted.
func namespaceTopics(doc *config.SchemaDocument) []Node {
	var out []Node
	for _, name := range sortedNamespaceKeys(doc.Namespaces) {
		if ns := doc.Namespaces[name]; isTopicNamespace(ns) {
			out = append(out, NamespaceNode(doc, name, ns))
		}
	}
	return out
}

// memberNames returns the names that could continue a namespace path.
func memberNames(members []*config.SchemaMember, add func(string)) {
	for _, m := range members {
		add(m.Name)
	}
}

// walkNamespace renders a namespace: what it is, and what may follow the dot.
//
// A block namespace has no members to list — they are the names in the
// configuration — so what it gets instead is a pointer to the block that
// declares them. Saying nothing would read as "there is nothing here", which is
// the opposite of what `bus.<name>` means.
func (w *walker) walkNamespace(n Node, level int) {
	if w.describeNamespace(n) {
		return
	}
	w.emit(memberTable(n.Path, n.ns.Members, n.ns.FreeMembers, n.ns.Constant))
}

// describeNamespace emits what a namespace is, and reports whether that is all
// there is to say — which it is for a block namespace, whose members are the
// names in the configuration rather than anything the schema knows.
func (w *walker) describeNamespace(n Node) (done bool) {
	w.describe(n.Summary(), n.Description(), n.ns.Undocumented)
	if n.ns.Kind != config.NamespaceBlock {
		return false
	}
	w.emit(Note{Text: "One name here for each `" + n.ns.Block +
		"` block, so what exists is what your configuration declares."})
	w.emit(SeeAlso{Items: []Link{{
		Text:   "`" + n.ns.Block + "`",
		Target: n.ns.DocPage,
		Argv:   []string{n.ns.Block},
	}}})
	return true
}

// namespacesSection renders what every namespace is, under its own heading,
// without listing any namespace's members.
//
// Members are a separate region because a page wants them selectively:
// `sys`'s twenty-six are facts a reader looks up, while `http_status`'s sixty
// are one fact said sixty times and belong in `vinculum man http_status`. This
// is the same split the block regions make between `block-body` and
// `block-attrs`.
//
// It walks the nodes rather than reading the document directly, so a page and
// `vinculum man` cannot describe a namespace differently. What it leaves off is
// the "See also" footer Walk appends: inside doc/config.md every one of those
// links would point at the page it is already on.
func namespacesSection(doc *config.SchemaDocument, level int) []Event {
	w := &walker{doc: doc}
	for _, name := range sortedNamespaceKeys(doc.Namespaces) {
		ns := doc.Namespaces[name]
		node := NamespaceNode(doc, name, ns)
		w.emit(Heading{Level: level, Text: node.Title()})
		w.describeNamespace(node)
	}
	return w.events
}

// namespaceMembersSection renders one namespace's members alone, under whatever
// heading the page supplies.
func namespaceMembersSection(doc *config.SchemaDocument, name string, level int) ([]Event, error) {
	ns, ok := doc.Namespaces[name]
	if !ok {
		return nil, fmt.Errorf("%s: no such namespace", name)
	}
	if ns.Kind == config.NamespaceBlock {
		// Its names come from the configuration, so an empty region here would
		// claim the root carries nothing rather than that the region was wrong.
		return nil, fmt.Errorf("%s: a block namespace has no members to list; they are the names your configuration declares", name)
	}
	return []Event{memberTable([]string{name}, ns.Members, ns.FreeMembers, ns.Constant)}, nil
}

// walkMember renders one member: the row that says what it is, then its own
// documentation, then what may follow its dot if anything may.
//
// The row is emitted without its Doc, the way walkAttr's one-row table is: the
// prose that follows is the same text, and a sink that renders per-row detail
// would otherwise print it twice.
func (w *walker) walkMember(n Node, level int) {
	w.emit(MemberTable{
		Prefix:   n.Path[:len(n.Path)-1],
		Rows:     []MemberRow{memberRow(n.Path[:len(n.Path)-1], n.member, false)},
		Constant: n.ns.Constant,
	})
	w.describe(n.Summary(), n.Description(), false)

	if len(n.member.Members) > 0 || n.member.FreeMembers {
		w.emit(Heading{Level: level + 1, Text: "Members"})
		w.emit(memberTable(n.Path, n.member.Members, n.member.FreeMembers, n.ns.Constant))
	}
}

// memberTable renders the members read under a dotted prefix. free and constant
// describe the node the members hang off rather than the namespace root: a fixed
// namespace can have a free member (`sys.signals`), and reading the root's flag
// would describe it wrongly.
// Members of members are flattened into the same table rather than nested,
// because a row is keyed by its whole dotted path: `sys.signals.bynumber` says
// where it lives, so it needs no hierarchy to sit in.
func memberTable(prefix []string, members []*config.SchemaMember, free, constant bool) MemberTable {
	t := MemberTable{Prefix: prefix, FreeMembers: free, Constant: constant}
	t.Rows = memberRows(prefix, members)
	return t
}

func memberRows(prefix []string, members []*config.SchemaMember) []MemberRow {
	var rows []MemberRow
	for _, m := range members {
		rows = append(rows, memberRow(prefix, m, true))
		if len(m.Members) > 0 {
			rows = append(rows, memberRows(append(append([]string{}, prefix...), m.Name), m.Members)...)
		}
	}
	return rows
}

func memberRow(prefix []string, m *config.SchemaMember, withDoc bool) MemberRow {
	r := MemberRow{
		Name:       m.Name,
		Type:       m.Type,
		Value:      m.Value,
		Summary:    m.Summary,
		HasMembers: len(m.Members) > 0 || m.FreeMembers,
		Path:       append(append([]string{}, prefix...), m.Name),
	}
	if withDoc {
		r.Doc = m.Doc
	}
	return r
}

// memberPath spells a row the way it is written in an expression. It is the
// row's whole path rather than the table's prefix plus its name, because a
// table flattens nested members into itself: `sys.functy.version` sits in the
// table prefixed `sys`.
func memberPath(r MemberRow) string {
	if len(r.Path) > 0 {
		return strings.Join(r.Path, ".")
	}
	return r.Name
}

func sortedNamespaceKeys(m map[string]*config.SchemaNamespace) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
