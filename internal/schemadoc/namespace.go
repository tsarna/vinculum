package schemadoc

import (
	"sort"

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
func (w *walker) walkNamespace(n Node, level int) {
	w.describe(n.Summary(), n.Description(), n.ns.Undocumented)
	w.emit(memberTable(n.Path, n.ns.Members, n.ns.FreeMembers, n.ns.Constant))
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
func memberTable(prefix []string, members []*config.SchemaMember, free, constant bool) MemberTable {
	t := MemberTable{Prefix: prefix, FreeMembers: free, Constant: constant}
	for _, m := range members {
		t.Rows = append(t.Rows, memberRow(prefix, m, true))
	}
	return t
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

func sortedNamespaceKeys(m map[string]*config.SchemaNamespace) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
