package schemadoc

import (
	"sort"
	"strings"
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
	// FuncHelp renders one function, or reports false when there is no such
	// function.
	FuncHelp(name string) (string, bool)
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
	if _, ok := cat.FuncHelp(path[0]); !ok {
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

// walkFunction renders one function.
//
// The body is whatever the catalog produced, emitted verbatim. Rendering it
// structurally — a synopsis per overload, the parameters as a table — needs
// functy to export the declarations behind its own renderer; until then,
// reproducing its output is the honest option, and it keeps `vinculum man
// --type function send` and `help("send")` word for word identical.
func (w *walker) walkFunction(n Node) {
	if text, ok := n.funcs.FuncHelp(n.Path[0]); ok {
		w.emit(Preformatted{Text: text})
	}
}
