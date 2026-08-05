package config

import (
	"strconv"
	"strings"

	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty/function"
)

// What is known about a callable function, in a shape a renderer can lay out.
//
// help() returns text, which is right for an expression and wrong for a page:
// the parameter list is a fixed-width block that cannot be re-wrapped for a
// narrow terminal or turned into a Markdown table. This is the same information
// before it becomes text.
//
// It is deliberately not functy's own AST. Half of Vinculum's functions have no
// functy declaration at all — they are plain cty functions contributed by
// libraries — and both sources project into this, so a renderer handles one
// shape rather than two.

// FuncDoc is a function's documentation.
type FuncDoc struct {
	Name string
	// Signatures is one rendered calling convention per form. More than one
	// means an overload set: a host function whose argument shapes differ per
	// arity is not one function with optional parameters, and showing it as one
	// would lie.
	Signatures []string
	// Doc is the prose describing the function, "" when undocumented.
	Doc string
	// Params are the documented parameters, unioned across the forms with the
	// first occurrence of a name winning — matching how functy renders them.
	Params []FuncParam
}

// FuncParam is one parameter of a function.
type FuncParam struct {
	// Name is how the parameter is written in a signature: "*rest" for a
	// variadic, "name?" for one that is optional with no default, else the bare
	// name.
	Name string
	// Type is the annotated type, "" when unannotated (dynamic).
	Type string
	// Default is the source text of a default value, "" when there is none.
	Default string
	// Doc describes the parameter, "" when undocumented.
	Doc string
	// Required reports whether a caller must supply it.
	Required bool
}

// FuncDoc returns what is known about one callable function, or reports false
// when nothing of that name is callable.
//
// The precedence is help()'s, and deliberately: a declaration beats the eval
// context, because a declaration exists precisely when the cty metadata is
// wrong — the get/set/count family fakes an optional *leading* ctx with a
// trailing variadic, and reflects as the useless `get(thing, ...args)`.
func (c *Config) FuncDoc(name string) (FuncDoc, bool) {
	if c == nil {
		return FuncDoc{}, false
	}
	res := c.functyResult()

	if decls := res.LookupFuncDecls(name); len(decls) > 0 {
		return funcDocFromDecls(name, decls), true
	}
	if c.evalCtx != nil {
		if fn, ok := c.evalCtx.Functions[name]; ok {
			return funcDocFromCty(name, fn), true
		}
	}
	if decls := res.LookupBareFuncDecls(name); len(decls) > 0 {
		return funcDocFromDecls(name, decls), true
	}
	return FuncDoc{}, false
}

// FuncNameCandidates returns the qualified names a bare name could have meant,
// when it could have meant more than one.
//
// A name that resolves, and a name that names nothing, both return nothing:
// this answers only the third case, which is otherwise indistinguishable from
// the second. Without it, `help("dup")` across two namespaces reports "no such
// function" for a function that exists twice.
func (c *Config) FuncNameCandidates(name string) []string {
	if c == nil {
		return nil
	}
	if _, ok := c.FuncDoc(name); ok {
		return nil // it resolved; there is nothing to disambiguate
	}
	if got := c.functyResult().BareNameCandidates(name); len(got) > 1 {
		return got
	}
	return nil
}

// funcDocFromDecls projects one or more functy declarations.
func funcDocFromDecls(name string, decls []*functy.FuncDecl) FuncDoc {
	doc := FuncDoc{Name: name}

	seenParam := make(map[string]bool)
	var docs []string

	for _, fn := range decls {
		doc.Signatures = append(doc.Signatures, functy.RenderFuncSignature(fn))

		for _, p := range fn.Params {
			display := p.DisplayName()
			if seenParam[display] {
				continue
			}
			seenParam[display] = true
			doc.Params = append(doc.Params, FuncParam{
				Name:     display,
				Type:     typeConstraintString(p.Type),
				Default:  p.DefaultSrc,
				Doc:      p.Doc,
				Required: !p.Optional && !p.Variadic && p.Default == nil,
			})
		}
		// Documenting the family once, above the first form, is the expected
		// style; distinct docs across forms are kept, in order.
		if fn.Doc != "" && !contains(docs, fn.Doc) {
			docs = append(docs, fn.Doc)
		}
	}
	doc.Doc = strings.Join(docs, "\n\n")
	return doc
}

// funcDocFromCty reconstructs what cty metadata can express.
//
// It cannot express an optional leading parameter, a default, or a return type
// that varies with the arguments — a function that needs any of those declares
// a functy extern, which is the path above.
func funcDocFromCty(name string, fn function.Function) FuncDoc {
	doc := FuncDoc{
		Name: name,
		Doc:  fn.Description(),
		// The signature is functy's to render: the return type has to be asked
		// for with a speculative call that can panic, and rendering it here
		// would give `vinculum man` a second spelling of what help() prints.
		Signatures: []string{functy.RenderCtySignature(name, fn)},
	}

	for i, p := range fn.Params() {
		pname := p.Name
		if pname == "" {
			pname = "arg" + strconv.Itoa(i+1)
		}
		doc.Params = append(doc.Params, FuncParam{
			Name: pname, Type: functy.TypeString(p.Type), Doc: p.Description, Required: true,
		})
	}
	if vp := fn.VarParam(); vp != nil {
		pname := vp.Name
		if pname == "" {
			pname = "args"
		}
		doc.Params = append(doc.Params, FuncParam{
			Name: "*" + pname, Type: functy.TypeString(vp.Type), Doc: vp.Description,
		})
	}
	return doc
}

// typeConstraintString is a parameter's annotated type, or "" when it carries
// none. functy renders the constraint rather than the source text, so an alias
// resolves to what it names.
func typeConstraintString(tc functy.TypeConstraint) string {
	if tc == nil {
		return ""
	}
	return tc.String()
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
