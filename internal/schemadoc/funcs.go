package schemadoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// Functions as a second corpus.
//
// Everything else this package documents comes from config.SchemaDocument,
// which is generated from the decode structs and describes what the parser
// accepts. The callable functions are not in it and should not be: they come
// from an assembled eval context, which exists only once a Config has been
// built, and half of them are contributed by libraries that know nothing about
// the config language.
//
// So they are a separate corpus with a separate entry point, and a front door
// that wants both searches both and unions the candidates. That union is what
// makes the cross-kind ambiguity real — `assert` is both a block type and a
// function — and it is spelled out at each call site rather than hidden inside
// Resolve, because only a caller that has built a Config has a catalog to
// search.

// FuncCatalog is the set of callable functions a front door can document.
//
// *config.Config satisfies it. The interface exists so this package does not
// import a built Config just to read two methods off it — and so a test can
// supply a handful of functions instead of booting a runtime.
type FuncCatalog interface {
	// FuncNames returns every callable name, sorted.
	FuncNames() []string
	// FuncDoc returns what is known about one function, or reports false when
	// there is no such function.
	FuncDoc(name string) (config.FuncDoc, bool)
	// FuncNameCandidates returns the qualified names a bare name could have
	// meant, when it could have meant more than one and so resolved to none.
	FuncNameCandidates(name string) []string
}

// FuncNode returns a node for one function.
func FuncNode(cat FuncCatalog, name string) Node {
	return Node{Kind: KindFunction, Path: []string{name}, shape: shapeFunction, funcs: cat}
}

// ResolveFuncs returns the function, if any, that path names.
//
// A function name is a whole path: functions have no members, so a longer path
// never names one. (A qualified functy name — `time::now` — is a single
// element, not two.)
func ResolveFuncs(cat FuncCatalog, kind Kind, path []string) []Node {
	if cat == nil || len(path) != 1 {
		return nil
	}
	if kind != "" && kind != KindFunction {
		return nil
	}
	if _, ok := cat.FuncDoc(path[0]); !ok {
		return nil
	}
	return []Node{FuncNode(cat, path[0])}
}

// FuncLeadingNames returns every function name, for completion and near-miss
// suggestions.
func FuncLeadingNames(cat FuncCatalog, kind Kind) []string {
	if cat == nil || (kind != "" && kind != KindFunction) {
		return nil
	}
	names := append([]string(nil), cat.FuncNames()...)
	sort.Strings(names)
	return names
}

// AmbiguousFuncName returns the qualified names a bare function name could have
// meant, when it could have meant more than one.
//
// A bare name declared in two namespaces resolves to nothing, exactly as a
// misspelling does. Without this the two are reported the same way — "no topic
// named dup" for a function that exists twice — which sends a reader looking for
// a typo that is not there.
func AmbiguousFuncName(cat FuncCatalog, kind Kind, path []string) []string {
	if cat == nil || len(path) != 1 {
		return nil
	}
	if kind != "" && kind != KindFunction {
		return nil
	}
	return cat.FuncNameCandidates(path[0])
}

// AmbiguousFuncMenu renders those candidates as the menu that resolves them.
func AmbiguousFuncMenu(query []string, names []string, spell Speller) Menu {
	items := make([]string, 0, len(names))
	for _, n := range names {
		items = append(items, spell(KindFunction, []string{n}, false))
	}
	return Menu{
		Intro: fmt.Sprintf("%q is a function in more than one namespace, choose one of:",
			strings.Join(query, " ")),
		Items: items,
	}
}

// SuggestFuncs returns function names that are a near miss for the query, on
// the same terms as Suggest does for topics.
func SuggestFuncs(cat FuncCatalog, kind Kind, path []string) []Node {
	if cat == nil || len(path) == 0 {
		return nil
	}
	query := strings.ToLower(path[0])

	type scored struct {
		name string
		dist int
	}
	var near []scored
	for _, name := range FuncLeadingNames(cat, kind) {
		if d := editDistance(query, strings.ToLower(name)); d <= suggestMaxDistance {
			near = append(near, scored{name, d})
		}
	}
	sort.SliceStable(near, func(i, j int) bool {
		if near[i].dist != near[j].dist {
			return near[i].dist < near[j].dist
		}
		return near[i].name < near[j].name
	})

	out := make([]Node, 0, len(near))
	for _, s := range near {
		out = append(out, FuncNode(cat, s.name))
		if len(out) >= suggestMax {
			break
		}
	}
	return out
}

// walkFunction renders one function the way a block is rendered: the calling
// convention as a synopsis, the description as prose, and the parameters as a
// table.
//
// Structured rather than help()'s single rendered block, because that block
// cannot re-wrap — its aligned parameter column is fixed at the width it was
// built for, so a long parameter description runs off a narrow terminal and
// stays a code fence in Markdown where a table belongs.
func (w *walker) walkFunction(n Node, level int) {
	doc, ok := n.funcs.FuncDoc(n.Path[0])
	if !ok {
		return
	}

	// One synopsis per form. An overload set is several calling conventions,
	// not one with optional parameters: parsetime(s) reads a timestamp while
	// parsetime(format, s) reads a format and then a timestamp.
	if len(doc.Signatures) > 0 {
		w.emit(Synopsis{Lines: doc.Signatures})
	}
	if doc.Doc != "" {
		w.emit(Prose{Markdown: doc.Doc})
	}
	if len(doc.Params) == 0 {
		return
	}
	rows := make([]AttrRow, 0, len(doc.Params))
	for _, p := range doc.Params {
		rows = append(rows, AttrRow{
			Name:     p.Name,
			Type:     p.Type,
			Required: p.Required,
			Summary:  paramSummary(p),
		})
	}
	w.emit(Heading{Level: level + 1, Text: "Parameters"})
	w.emit(AttrTable{Rows: rows})
}

// paramSummary is the parameter's description, with its default folded in —
// the default is part of what a caller needs to know and has no column of its
// own.
func paramSummary(p config.FuncParam) string {
	if p.Default == "" {
		return p.Doc
	}
	if p.Doc == "" {
		return "Defaults to `" + p.Default + "`."
	}
	return strings.TrimRight(p.Doc, ".") + ". Defaults to `" + p.Default + "`."
}
