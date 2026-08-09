package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// Deferred-reference checking: resolving the references of the expressions
// nothing has evaluated yet, at the point where their namespace is finally
// knowable.
//
// Processing a block evaluates most of its body, so a reference to a name that
// does not exist is reported there with a source range. An event-time
// expression — an `action`, an `on_connect`, a computed metric's `value` — is
// stored instead, and evaluated when something happens. Without this pass it
// would carry a bad reference until the first event, and then fail identically
// at every event after it.
//
// Neither half of the check is modelled here. Which attributes are deferred
// comes from the schema: an attribute evaluated at event time carries an
// AttrMeta.Context naming the shape of `ctx` it sees, and
// `vinculum schema --strict --require-docs` in CI is what keeps that annotation
// honest, so the document says exactly which expressions to check and what
// `ctx` each one gets.
//
// The namespace comes from the finished eval context. Every runtime site builds
// its context with hclutil.NewEvalContext(...).BuildEvalContext(config.EvalCtx())
// — a child of config.evalCtx whose only own variable is `ctx` — and a test in
// hclutil fails the build if anything else assembles one. So once the Process
// phase is over, the whole namespace is config.evalCtx.Variables plus `ctx`.
//
// Function names are out of scope. Unlike variables, a site may add functions of
// its own to its child context (the http server's request functions, the MCP
// handlers'), and nothing describes those additions, so an unknown-function
// check would report working configs.

// exprCheckSkipBlockTypes names block types whose deferred expressions cannot
// be checked against the global namespace.
//
// An `editor` body is evaluated with its user-declared parameters — and, in a
// `match` block, `state` — copied into the same scope as `ctx`
// (editors/line/line.go), so its references legitimately resolve to names no
// schema describes. Editor blocks are also extracted before the block handlers
// run, so they never reach this check anyway; the entry records the reason
// rather than relying on that.
var exprCheckSkipBlockTypes = map[string]bool{
	"editor": true,
}

// exprCheckOpaqueFuncs names functions whose arguments are expected not to
// resolve. try() and can() exist in part to allow references to something that
// may be absent, so a traversal anywhere beneath one is not a mistake.
var exprCheckOpaqueFuncs = map[string]bool{
	"try": true,
	"can": true,
}

// exprCheckBlockNamespaces names the top-level namespaces whose members are
// declared by config blocks. Everything reachable from the eval context that is
// not an ambient can be checked a level deep; what this set decides is how the
// failure reads, since "No bus named ..." says more than the generic wording a
// const gets.
var exprCheckBlockNamespaces = map[string]bool{
	"bus":         true,
	"client":      true,
	"condition":   true,
	"fsm":         true,
	"metric":      true,
	"server":      true,
	"trigger":     true,
	"var":         true,
	"wire_format": true,
}

// ambientRoots returns the leading names the ambient providers contribute.
//
// These are the one part of the namespace not checked below their leading name.
// `env` is the environment of whichever process is running, so checking a member
// of it would report a `vinculum check` on a build machine as broken for naming
// a variable only the deployment sets. `sys` and `http_status` have a fixed
// shape and could be checked, but nothing in the registry distinguishes them
// from `env` — an AmbientProvider is a func returning a cty.Value and no more.
// Describing the ambient namespace in the schema is what would tell them apart.
func ambientRoots() map[string]bool {
	roots := make(map[string]bool, len(ambientProviders))
	for _, e := range ambientProviders {
		roots[e.name] = true
	}
	return roots
}

// checkDeferredReferences reports references that cannot resolve when the
// event-time expressions in blocks are eventually evaluated.
func (c *Config) checkDeferredReferences(blocks hcl.Blocks) hcl.Diagnostics {
	if len(blocks) == 0 {
		return nil
	}
	doc, _ := GenerateSchema(SchemaGenOptions{})
	if doc == nil {
		return nil
	}
	ck := &refChecker{config: c, doc: doc, ambient: ambientRoots()}
	for _, block := range blocks {
		ck.block(block)
	}
	return ck.diags
}

type refChecker struct {
	config  *Config
	doc     *SchemaDocument
	ambient map[string]bool
	diags   hcl.Diagnostics
}

// block checks one top-level block against the schema body that describes it.
func (ck *refChecker) block(block *hcl.Block) {
	if exprCheckSkipBlockTypes[block.Type] {
		return
	}
	described := ck.doc.Blocks[block.Type]
	if described == nil {
		return
	}
	body := described.Body
	if body == nil && len(block.Labels) > 0 {
		body = described.Variants[block.Labels[0]]
	}
	if body == nil {
		return
	}
	syntax, ok := block.Body.(*hclsyntax.Body)
	if !ok {
		return
	}
	ck.body(body, syntax)
}

// body checks the deferred expressions of one body and recurses into the
// sub-blocks the schema describes.
func (ck *refChecker) body(schema *SchemaBody, body *hclsyntax.Body) {
	// An undocumented body — a plugin's block type with no registered schema —
	// says nothing about which of its attributes are deferred.
	if schema.Undocumented || ck.disabled(body) {
		return
	}

	for _, attr := range schema.Attributes {
		if attr.Context == "" {
			continue
		}
		if present, ok := body.Attributes[attr.Name]; ok {
			ck.expression(attr, present.Expr)
		}
	}

	// A sub-block is looked up by type alone. Every nested label in the tree is
	// an identity — receiver "<name>", handle "<route>" — except auth "<mode>",
	// where it selects a mode, and one body carries the union of every mode's
	// attributes because one AuthConfig struct decodes them all. Checking that
	// union is right rather than merely tolerable: only an attribute the config
	// actually wrote is checked, and each carries its own context. The one shape
	// this could not express is an attribute whose context differs per mode,
	// which one struct field with one AttrMeta cannot describe either.
	for _, nested := range body.Blocks {
		if described, ok := schema.Blocks[nested.Type]; ok {
			ck.body(&described.SchemaBody, nested.Body)
		}
	}
}

// disabled reports whether the body carries `disabled = true`. Nothing is
// created from a disabled block, so it publishes no name; a reference to one
// is already documented as the config author's to resolve, and a disabled
// block's own expressions are never evaluated.
func (ck *refChecker) disabled(body *hclsyntax.Body) bool {
	attr, ok := body.Attributes["disabled"]
	if !ok {
		return false
	}
	val, diags := attr.Expr.Value(ck.config.evalCtx)
	if diags.HasErrors() {
		return false
	}
	val, err := convert.Convert(val, cty.Bool)
	if err != nil || val.IsNull() || !val.IsKnown() {
		return false
	}
	return val.True()
}

// expression checks every reference one deferred expression makes.
func (ck *refChecker) expression(attr *SchemaAttr, expr hclsyntax.Expression) {
	for _, traversal := range checkableTraversals(expr) {
		root := traversal.RootName()
		if root == "ctx" {
			ck.ctxField(attr, traversal)
			continue
		}
		val, ok := ck.config.evalCtx.Variables[root]
		if !ok {
			ck.unknownRoot(traversal)
			continue
		}
		ck.member(root, val, traversal)
	}
}

// ctxField checks `ctx.<name>` against the fields the attribute's context
// provides.
//
// Those come from two places, because a `ctx` is assembled in two steps. The
// shape holds what every site building it sets; the attribute holds what this
// site adds on top. `decode-error` is the case the split exists for: five
// fields are fixed by MakeDecodeErrorHook, and then each receiver contributes
// its own transport identity, so `ctx.routing_key` is a rabbitmq receiver's to
// read and `ctx.mqtt_topic` an mqtt one's. Neither list is the answer alone —
// the shape would report a correct routing_key as unknown, and the attribute
// knows nothing of `raw` or `error`.
//
// A shape carrying per-site additions is marked OpenFields, which tells a
// consumer reading the shape by itself to treat an unlisted name as
// unknown-but-possible rather than wrong. That is not the position here: this
// is reading one attribute, and for one attribute the union is the whole set.
// The schema rejects a site that adds a name the shape or a universal field
// already has, so the two cannot overlap or shadow.
func (ck *refChecker) ctxField(attr *SchemaAttr, traversal hcl.Traversal) {
	name, ok := firstAttrStep(traversal)
	if !ok {
		return // bare `ctx`, as passed to send() and friends
	}
	shape := ck.doc.Contexts[attr.Context]
	if shape == nil {
		return
	}

	// Collected as it goes, so the failure can say what was available. The
	// order is the shape's own, then the site's, which is the order worth
	// reading them in.
	known := make([]string, 0, len(shape.Fields)+len(attr.ContextFields))
	for _, f := range shape.Fields {
		if f.Name == name {
			return
		}
		known = append(known, f.Name)
	}
	for _, f := range attr.ContextFields {
		if f.Name == name {
			return
		}
		known = append(known, f.Name)
	}

	ck.report(traversal, fmt.Sprintf("Unknown ctx field %q", name),
		fmt.Sprintf("%s is evaluated with a %q context, which has no such field. It provides: %s.",
			attr.Name, attr.Context, joinNames(known)))
}

// member checks the name a reference reads out of a value that has names in it:
// a namespace declared by config blocks, or an object-valued const.
//
// Both are settled by the time this runs and neither can change afterwards, so
// `bus.mian` and `routing.gamma` are the same mistake — a name that will not
// resolve at any event, for the whole life of the process.
//
// The two guards below are what keep that honest. A const the author reaches
// into dynamically (`routing[ctx.kind]`) yields an index step, which
// firstAttrStep declines to read; a const holding a cty map rather than an
// object has no fixed attribute set, and IsObjectType declines that.
func (ck *refChecker) member(root string, val cty.Value, traversal hcl.Traversal) {
	if ck.ambient[root] {
		return
	}
	name, ok := firstAttrStep(traversal)
	if !ok {
		return
	}
	ty := val.Type()
	if !ty.IsObjectType() || ty.HasAttribute(name) {
		return
	}

	declared := make([]string, 0, len(ty.AttributeTypes()))
	for attrName := range ty.AttributeTypes() {
		declared = append(declared, attrName)
	}
	sort.Strings(declared)

	if !exprCheckBlockNamespaces[root] {
		detail := fmt.Sprintf("The const %s provides: %s.", root, joinNames(declared))
		if len(declared) == 0 {
			detail = fmt.Sprintf("The const %s is an empty object.", root)
		}
		ck.report(traversal, fmt.Sprintf("%s has no attribute %q", root, name), detail)
		return
	}

	detail := fmt.Sprintf("Declared %s names are: %s.", root, joinNames(declared))
	if len(declared) == 0 {
		detail = fmt.Sprintf("No %s is declared by this configuration.", root)
	}
	ck.report(traversal, fmt.Sprintf("No %s named %q", root, name), detail)
}

// unknownRoot reports a reference whose leading name is in no namespace at all.
func (ck *refChecker) unknownRoot(traversal hcl.Traversal) {
	inScope := make([]string, 0, len(ck.config.evalCtx.Variables)+1)
	inScope = append(inScope, "ctx")
	for name := range ck.config.evalCtx.Variables {
		inScope = append(inScope, name)
	}
	sort.Strings(inScope)
	ck.report(traversal, fmt.Sprintf("Unknown reference %q", traversal.RootName()),
		fmt.Sprintf("This expression is evaluated when the event happens, and nothing of that name is in scope. In scope here: %s.",
			joinNames(inScope)))
}

func (ck *refChecker) report(traversal hcl.Traversal, summary, detail string) {
	rng := traversal.SourceRange()
	ck.diags = ck.diags.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  &rng,
	})
}

// firstAttrStep returns the name of the traversal's first attribute step —
// the `main` of `bus.main`. An index step (`fields["x"]`) or a traversal that
// stops at its root says nothing checkable.
func firstAttrStep(traversal hcl.Traversal) (string, bool) {
	if len(traversal) < 2 {
		return "", false
	}
	step, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return "", false
	}
	return step.Name, true
}

// joinNamesMax caps how many candidate names are offered before the list stops
// being something to read and starts being something to scroll past.
const joinNamesMax = 12

// joinNames renders candidate names as prose, in the order given: a context
// shape lists its fields in the order worth reading them, and sorting that back
// into the alphabet would bury `topic` and `msg` among the universal fields.
// Callers with no such order sort first.
func joinNames(names []string) string {
	if len(names) > joinNamesMax {
		return strings.Join(names[:joinNamesMax], ", ") + ", …"
	}
	return strings.Join(names, ", ")
}

// checkableTraversals returns the root-scope references an expression makes,
// less the ones that are not this check's business.
//
// It is hclsyntax.Variables plus one rule: a traversal beneath a try() or can()
// is expected to be allowed to fail. The local-scope handling is the same, so a
// `for` expression's iterator is not reported as an unknown name.
func checkableTraversals(expr hclsyntax.Expression) []hcl.Traversal {
	w := &traversalWalker{}
	hclsyntax.Walk(expr, w) //nolint:errcheck // the walker returns no diagnostics
	return w.traversals
}

type traversalWalker struct {
	traversals []hcl.Traversal
	locals     []map[string]struct{}
	opaque     int
}

func (w *traversalWalker) Enter(node hclsyntax.Node) hcl.Diagnostics {
	switch n := node.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		if w.opaque > 0 {
			return nil
		}
		name := n.Traversal.RootName()
		for _, locals := range w.locals {
			if _, shadowed := locals[name]; shadowed {
				return nil
			}
		}
		w.traversals = append(w.traversals, n.Traversal)
	case hclsyntax.ChildScope:
		w.locals = append(w.locals, n.LocalNames)
	case *hclsyntax.FunctionCallExpr:
		if exprCheckOpaqueFuncs[n.Name] {
			w.opaque++
		}
	}
	return nil
}

func (w *traversalWalker) Exit(node hclsyntax.Node) hcl.Diagnostics {
	switch n := node.(type) {
	case hclsyntax.ChildScope:
		w.locals = w.locals[:len(w.locals)-1]
	case *hclsyntax.FunctionCallExpr:
		if exprCheckOpaqueFuncs[n.Name] {
			w.opaque--
		}
	}
	return nil
}
