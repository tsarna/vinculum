package schemadoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/suggest"
)

// Resolve returns every topic that path could name, in kind order.
//
// It returns candidates rather than a result because ambiguity is normal
// rather than exceptional: `http` and `vws` are each both a client type and a
// server type. A caller renders one candidate, or turns the set into a menu
// (see MenuFor).
//
// This searches the config-language document only. Functions are a separate
// corpus — see ResolveFuncs — and a front door that searches both unions the
// results, which is where cross-kind ambiguity comes from (`assert` is both a
// block type and a function).
//
// An empty kind searches every kind; a non-empty one restricts the search,
// which is what `--type` does.
func Resolve(doc *config.SchemaDocument, kind Kind, path []string) []Node {
	if doc == nil || len(path) == 0 {
		return nil
	}
	var out []Node
	if kind == "" || kind == KindBlock {
		out = append(out, resolveBlock(doc, path)...)
	}
	if kind == "" || kind == KindContext {
		out = append(out, resolveContext(doc, path)...)
	}
	if kind == "" || kind == KindNamespace {
		out = append(out, resolveNamespace(doc, path)...)
	}
	return out
}

// resolveBlock resolves a path in the block namespace. The leading element is
// either a top-level block type or — the short form — the name of a type
// variant, which is what makes `vinculum man mqtt` work without naming the
// block it belongs to, and `vinculum man http` ambiguous.
func resolveBlock(doc *config.SchemaDocument, path []string) []Node {
	var out []Node

	if block, ok := doc.Blocks[path[0]]; ok {
		out = append(out, descendBlock(doc, path[0], block, path[1:])...)
	}

	for _, blockType := range sortedBlockKeys(doc.Blocks) {
		block := doc.Blocks[blockType]
		if block.VariantLabel == "" {
			continue
		}
		body, ok := block.Variants[path[0]]
		if !ok {
			continue
		}
		self := VariantNode(doc, blockType, path[0], block, body)
		out = append(out, descendBody(doc, self, self.Path, body, path[1:])...)
	}
	return out
}

// resolveContext resolves a `ctx` shape name. Shapes have no members, so a
// longer path never names one.
func resolveContext(doc *config.SchemaDocument, path []string) []Node {
	if len(path) != 1 {
		return nil
	}
	if cs, ok := doc.Contexts[path[0]]; ok {
		return []Node{ContextNode(doc, path[0], cs)}
	}
	return nil
}

// descendBlock resolves the remainder of a path from a top-level block: a
// variant for a typed block, a body member for a plain one.
func descendBlock(doc *config.SchemaDocument, name string, block *config.SchemaBlock, rest []string) []Node {
	self := BlockNode(doc, name, block)
	if len(rest) == 0 {
		return []Node{self}
	}

	if block.VariantLabel != "" {
		body, ok := block.Variants[rest[0]]
		if !ok {
			return nil
		}
		variant := VariantNode(doc, name, rest[0], block, body)
		return descendBody(doc, variant, variant.Path, body, rest[1:])
	}

	if block.Body == nil {
		return nil
	}
	return descendBody(doc, self, self.Path, block.Body, rest)
}

// descendBody resolves the remainder of a path within a body. self is what to
// return when the path ends here.
//
// A name may match a sub-block and an attribute both. That is reported as
// ambiguity rather than resolved by preferring either: silently picking one
// would document the wrong thing with no sign that it had.
func descendBody(doc *config.SchemaDocument, self Node, path []string, body *config.SchemaBody, rest []string) []Node {
	if len(rest) == 0 {
		return []Node{self}
	}

	var out []Node
	name := rest[0]
	sub := append(append([]string{}, path...), name)

	if nested, ok := body.Blocks[name]; ok {
		out = append(out, descendBody(doc, NestedNode(doc, sub, nested), sub, &nested.SchemaBody, rest[1:])...)
	}
	// An attribute has no members, so it can only end a path.
	if len(rest) == 1 {
		for _, a := range body.Attributes {
			if a.Name == name {
				out = append(out, AttrNode(doc, sub, a))
			}
		}
	}
	return out
}

// Topics returns the topics that make up the index page: every top-level block
// and every `ctx` shape. Type variants are deliberately absent — they are
// listed under the block they belong to, and 43 of them would bury the 15
// blocks a reader is looking for.
func Topics(doc *config.SchemaDocument, kind Kind) []Node {
	if doc == nil {
		return nil
	}
	var out []Node
	if kind == "" || kind == KindBlock {
		for _, name := range sortedBlockKeys(doc.Blocks) {
			out = append(out, BlockNode(doc, name, doc.Blocks[name]))
		}
	}
	if kind == "" || kind == KindContext {
		for _, name := range sortedContextKeys(doc.Contexts) {
			out = append(out, ContextNode(doc, name, doc.Contexts[name]))
		}
	}
	if kind == "" || kind == KindNamespace {
		out = append(out, namespaceTopics(doc)...)
	}
	return out
}

// LeadingNames returns every name that can begin a path: block types, variant
// names, and `ctx` shape names.
//
// Attribute names are deliberately excluded. `action` appears in dozens of
// bodies, and a menu of dozens is not a menu — attributes resolve only as the
// continuation of a block path. (Finding one by name is what an apropos search
// would be for.)
func LeadingNames(doc *config.SchemaDocument, kind Kind) []string {
	if doc == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	if kind == "" || kind == KindBlock {
		for _, blockType := range sortedBlockKeys(doc.Blocks) {
			add(blockType)
			for _, variant := range sortedBodyNames(doc.Blocks[blockType].Variants) {
				add(variant)
			}
		}
	}
	if kind == "" || kind == KindContext {
		for _, name := range sortedContextKeys(doc.Contexts) {
			add(name)
		}
	}
	if kind == "" || kind == KindNamespace {
		for _, n := range namespaceTopics(doc) {
			add(n.Path[0])
		}
	}
	sort.Strings(out)
	return out
}

// Members returns the names that could continue path — the next word a reader
// could type. For a typed block that is its variants; for a body it is its
// attributes and sub-blocks; for anything with no members it is nothing.
//
// It is completion's half of resolution, and shares its rules: what it offers
// always resolves, and it never offers a name that Resolve would not accept in
// that position.
func Members(doc *config.SchemaDocument, kind Kind, path []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	// Every candidate contributes, because an ambiguous prefix has more than
	// one continuation and a reader has not yet chosen between them.
	for _, n := range Resolve(doc, kind, path) {
		switch n.shape {
		case shapeBlock:
			if n.block.VariantLabel != "" {
				for _, v := range sortedBodyNames(n.block.Variants) {
					add(v)
				}
				continue
			}
			if n.block.Body != nil {
				bodyMembers(n.block.Body, add)
			}
		case shapeVariant:
			bodyMembers(n.body, add)
		case shapeNested:
			bodyMembers(&n.nested.SchemaBody, add)
		case shapeNamespace:
			memberNames(n.ns.Members, add)
		case shapeMember:
			memberNames(n.member.Members, add)
		}
	}
	sort.Strings(out)
	return out
}

func bodyMembers(body *config.SchemaBody, add func(string)) {
	for _, name := range sortedBlockNames(body.Blocks) {
		add(name)
	}
	for _, a := range body.Attributes {
		add(a.Name)
	}
}

// Speller renders the invocation that names one candidate, in the idiom of the
// front door a menu is being printed at: a shell command for `vinculum man`, a
// meta-command for the REPL, a function call for help().
//
// qualify asks for the kind to be named explicitly. The caller decides when
// that is needed; see MenuFor.
type Speller func(kind Kind, path []string, qualify bool) string

// CommandSpeller spells a candidate as a `vinculum man` invocation.
func CommandSpeller(kind Kind, path []string, qualify bool) string {
	parts := []string{"vinculum", "man"}
	if qualify {
		parts = append(parts, "--type", string(kind))
	}
	return strings.Join(append(parts, path...), " ")
}

// MenuFor turns an ambiguous request into the menu that resolves it: one exact
// invocation per candidate.
//
// The kind is named only when the candidates actually differ by kind. When
// they differ by path — which is the common case, `http` being both a client
// and a server type — the longer path is the disambiguator, and it is what a
// person would rather type than a flag.
func MenuFor(query []string, candidates []Node, spell Speller) Menu {
	return Menu{
		Intro: fmt.Sprintf("%q is ambiguous, choose one of:", strings.Join(query, " ")),
		Items: menuItems(candidates, spell),
	}
}

// SuggestMenu renders near misses as the "did you mean" after a failed
// resolution. Same items, different question.
func SuggestMenu(candidates []Node, spell Speller) Menu {
	return Menu{Intro: "did you mean:", Items: menuItems(candidates, spell)}
}

func menuItems(candidates []Node, spell Speller) []string {
	qualify := distinctKinds(candidates) > 1

	seen := map[string]bool{}
	var items []string
	for _, c := range candidates {
		item := spell(c.Kind, c.Path, qualify)
		if seen[item] {
			continue
		}
		seen[item] = true
		items = append(items, item)
	}
	return items
}

func distinctKinds(candidates []Node) int {
	seen := map[Kind]bool{}
	for _, c := range candidates {
		seen[c.Kind] = true
	}
	return len(seen)
}

// suggestMax caps how many near misses are offered. A list long enough to
// scan is not a suggestion.
const suggestMax = 5

// Suggest returns topics whose name is a near miss for the query's leading
// element, for the "did you mean" after a failed resolution.
//
// It searches exactly the name set Resolve searches, so it can never suggest
// something that would not itself resolve. How near a miss has to be is the
// suggest package's to say, so that a topic and a function name are judged on
// the same terms.
func Suggest(doc *config.SchemaDocument, kind Kind, path []string) []Node {
	if doc == nil || len(path) == 0 {
		return nil
	}

	var out []Node
	for _, name := range suggest.Near(path[0], LeadingNames(doc, kind)) {
		out = append(out, Resolve(doc, kind, []string{name})...)
		if len(out) >= suggestMax {
			return out[:suggestMax]
		}
	}
	return out
}

func sortedContextKeys(m map[string]*config.SchemaContext) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
