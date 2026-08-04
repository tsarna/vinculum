package schemadoc

import (
	"strings"

	"github.com/tsarna/vinculum/config"
)

// Kind is the namespace a topic lives in — the analogue of a man section, and
// what `vinculum man --type` selects.
type Kind string

const (
	// KindBlock is a top-level VCL block, one of its type variants, or a
	// member (attribute or sub-block) reached through one.
	KindBlock Kind = "block"
	// KindContext is a `ctx` shape, named by an attribute's context field.
	KindContext Kind = "context"
	// KindFunction is a callable function.
	KindFunction Kind = "function"
)

// Kinds are the kinds a topic may be resolved in, in the order they are tried.
//
// KindFunction is declared above but absent here until something resolves in
// it: offering a reader a --type that finds nothing would be worse than not
// offering it.
var Kinds = []Kind{KindBlock, KindContext}

// ValidKind reports whether s names a kind.
func ValidKind(s string) bool {
	for _, k := range Kinds {
		if string(k) == s {
			return true
		}
	}
	return false
}

// nodeShape discriminates what a Node points at. A Node carries exactly one of
// the schema pointers below, and this says which.
type nodeShape int

const (
	// shapeBlock is a top-level block: `subscription`, or `client` as a whole.
	shapeBlock nodeShape = iota
	// shapeVariant is one type-variant body of a typed block: `client "mqtt"`.
	shapeVariant
	// shapeNested is a sub-block of some body: the `tls` inside `client "mqtt"`.
	shapeNested
	// shapeAttr is a single attribute.
	shapeAttr
	// shapeContext is a `ctx` shape.
	shapeContext
)

// Node is one addressable topic: whatever a resolved path points at, together
// with the document it came from and the path that names it.
//
// The document is carried along because rendering a node reaches back into it —
// an attribute inlines the `ctx` shape it names, and a block links to its
// siblings.
type Node struct {
	Kind Kind
	// Path is the argv that names this node, e.g. ["client", "mqtt", "tls"].
	Path []string
	// Doc is the document Path was resolved against.
	Doc *config.SchemaDocument

	shape  nodeShape
	block  *config.SchemaBlock
	body   *config.SchemaBody
	nested *config.SchemaNestedBlock
	attr   *config.SchemaAttr
	ctx    *config.SchemaContext
	// labels are the enclosing block's label names, so a variant or sub-block
	// can render its own header line in a synopsis.
	labels []string
}

// BlockNode returns a node for a top-level block.
func BlockNode(doc *config.SchemaDocument, name string, block *config.SchemaBlock) Node {
	return Node{Kind: KindBlock, Path: []string{name}, Doc: doc, shape: shapeBlock, block: block, labels: block.Labels}
}

// VariantNode returns a node for one type-variant body of a typed block.
func VariantNode(doc *config.SchemaDocument, blockType, variant string, block *config.SchemaBlock, body *config.SchemaBody) Node {
	return Node{
		Kind: KindBlock, Path: []string{blockType, variant}, Doc: doc,
		shape: shapeVariant, block: block, body: body, labels: block.Labels,
	}
}

// NestedNode returns a node for a sub-block reached through path.
func NestedNode(doc *config.SchemaDocument, path []string, nested *config.SchemaNestedBlock) Node {
	return Node{Kind: KindBlock, Path: path, Doc: doc, shape: shapeNested, nested: nested, labels: nested.Labels}
}

// AttrNode returns a node for a single attribute reached through path.
func AttrNode(doc *config.SchemaDocument, path []string, attr *config.SchemaAttr) Node {
	return Node{Kind: KindBlock, Path: path, Doc: doc, shape: shapeAttr, attr: attr}
}

// ContextNode returns a node for a `ctx` shape.
func ContextNode(doc *config.SchemaDocument, name string, ctx *config.SchemaContext) Node {
	return Node{Kind: KindContext, Path: []string{name}, Doc: doc, shape: shapeContext, ctx: ctx}
}

// body returns the node's body, or nil for a node that has none (an attribute,
// a context shape, or a typed block whose bodies are its variants).
func (n Node) bodyOf() *config.SchemaBody {
	switch n.shape {
	case shapeBlock:
		return n.block.Body
	case shapeVariant:
		return n.body
	case shapeNested:
		return &n.nested.SchemaBody
	}
	return nil
}

// Title is the node's heading text, in the spelling a config author would
// recognize: `client "mqtt"` for a variant, `tls` for a sub-block of one.
func (n Node) Title() string {
	switch n.shape {
	case shapeBlock:
		return "`" + n.Path[0] + "`"
	case shapeVariant:
		return "`" + n.Path[0] + ` "` + n.Path[1] + `"` + "`"
	case shapeNested, shapeAttr:
		return "`" + n.Path[len(n.Path)-1] + "`"
	case shapeContext:
		return "`ctx` — " + n.Path[0]
	}
	return strings.Join(n.Path, " ")
}

// Breadcrumb is the containing scope of a nested node, for the line under its
// heading: "In `client \"mqtt\"`." Empty for a top-level node.
func (n Node) Breadcrumb() string {
	if n.shape != shapeNested && n.shape != shapeAttr {
		return ""
	}
	parent := n.Path[:len(n.Path)-1]
	if len(parent) == 0 {
		return ""
	}
	return "In " + pathSpelling(parent) + "."
}

// pathSpelling renders a block path the way it is written in VCL:
// ["client","mqtt","tls"] becomes `client "mqtt"` › `tls`.
func pathSpelling(path []string) string {
	if len(path) == 0 {
		return ""
	}
	head := "`" + path[0] + "`"
	rest := path[1:]
	// A typed block's second element is its variant label, which belongs
	// inside the same backticks as the block type.
	if len(rest) > 0 {
		head = "`" + path[0] + ` "` + rest[0] + `"` + "`"
		rest = rest[1:]
	}
	parts := []string{head}
	for _, p := range rest {
		parts = append(parts, "`"+p+"`")
	}
	return strings.Join(parts, " › ")
}

// Summary returns the node's one-line description.
func (n Node) Summary() string {
	switch n.shape {
	case shapeBlock:
		if n.block.Summary != "" {
			return n.block.Summary
		}
		if n.block.Body != nil {
			return n.block.Body.Summary
		}
	case shapeVariant:
		return n.body.Summary
	case shapeNested:
		return n.nested.Summary
	case shapeAttr:
		return n.attr.Summary
	case shapeContext:
		return n.ctx.Summary
	}
	return ""
}

// DocPage returns the hand-written reference page for this node, relative to
// doc/, or "" when it has none.
//
// Only types have one. An attribute is documented where it is defined, and a
// `ctx` shape is documented by the attributes that see it.
func (n Node) DocPage() string {
	switch n.shape {
	case shapeBlock:
		if n.block.DocPage != "" {
			return n.block.DocPage
		}
		if n.block.Body != nil {
			return n.block.Body.DocPage
		}
	case shapeVariant:
		return n.body.DocPage
	case shapeNested:
		return n.nested.DocPage
	}
	return ""
}

// Description returns the node's rich Markdown documentation.
func (n Node) Description() string {
	switch n.shape {
	case shapeBlock:
		if n.block.Doc != "" {
			return n.block.Doc
		}
		if n.block.Body != nil {
			return n.block.Body.Doc
		}
	case shapeVariant:
		return n.body.Doc
	case shapeNested:
		return n.nested.Doc
	case shapeAttr:
		return n.attr.Doc
	case shapeContext:
		return n.ctx.Doc
	}
	return ""
}

// Argv is the command-line spelling that names this node, used in ambiguity
// menus and cross-references.
func (n Node) Argv() []string {
	return n.Path
}
