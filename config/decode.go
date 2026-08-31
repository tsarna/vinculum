package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
)

// DecodeBody decodes an HCL body into a gohcl struct and then enforces the one
// thing gohcl's own struct tags cannot: a required `hcl.Expression` attribute.
//
// Every block in this repository decodes through here rather than through
// gohcl.DecodeBody directly, and a test fails the build if anything else calls
// gohcl.DecodeBody, because the alternative is remembering — and the whole
// reason this exists is that thirty declarations had already forgotten.
//
// # Why gohcl cannot do it
//
// gohcl's ImpliedBodySchema (gohcl/schema.go) never marks an hcl.Expression
// attribute required, whatever the tag says:
//
//	case field.Type.AssignableTo(exprType):
//	    // If we're decoding to hcl.Expression then absense can be
//	    // indicated via a null value, so we don't specify that
//	    // the field is required during decoding.
//	    required = false
//
// So `hcl:"bus"` on a server, with no `,optional`, reads as required to every
// human who opens the file and is enforced by nothing. The attribute goes
// missing, gohcl assigns a synthetic expression evaluating to a null, and the
// block builds. What happens next was the whole spread of the bug: sometimes a
// hand-written check caught it, sometimes a value check downstream reported
// "got dynamic" — naming a type where the fault was a missing argument — and
// sometimes, as with `trigger "start"` and no `action`, the configuration was
// simply accepted and did nothing.
//
// # What it reports
//
// hclsyntax's own wording, deliberately: an author who omits a required
// argument should see one diagnostic shape, not two that differ by whether the
// attribute happened to be an expression.
func DecodeBody(body hcl.Body, ctx *hcl.EvalContext, target any) hcl.Diagnostics {
	diags := gohcl.DecodeBody(body, ctx, target)
	if diags.HasErrors() {
		return diags
	}

	return diags.Extend(requiredExpressions(reflect.ValueOf(target), body.MissingItemRange()))
}

// requiredExpressions walks a decoded gohcl struct and reports every
// `hcl.Expression` attribute whose tag omits `,optional` and which the body did
// not provide.
//
// It recurses into nested blocks, which need it as much as top-level bodies do:
// a mqtt `will` with no `topic`, a redis `channel_subscription` with no
// `channel`. `subject` is where to report an omission from the body currently
// being walked; a nested block that captured its own `,def_range` reports
// against its own header instead, which is nearer the mistake than the parent's
// closing brace.
func requiredExpressions(v reflect.Value, subject hcl.Range) hcl.Diagnostics {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	if v.Kind() == reflect.Slice {
		var diags hcl.Diagnostics
		for i := range v.Len() {
			diags = diags.Extend(requiredExpressions(v.Index(i), subject))
		}
		return diags
	}

	if v.Kind() != reflect.Struct {
		return nil
	}

	ty := v.Type()

	// A block that captured its own definition range reports against that
	// rather than against whatever the caller passed down.
	if own, ok := defRange(v, ty); ok {
		subject = own
	}

	var diags hcl.Diagnostics
	for i := range ty.NumField() {
		name, kind, optional, ok := hclTag(ty.Field(i))
		if !ok {
			continue
		}

		switch kind {
		case "":
			if optional || !ty.Field(i).Type.AssignableTo(exprType) {
				continue
			}
			if !IsExpressionProvided(v.Field(i).Interface().(hcl.Expression)) {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Missing required argument",
					Detail:   fmt.Sprintf("The argument %q is required, but no definition was found.", name),
					Subject:  subject.Ptr(),
				})
			}

		case "block":
			diags = diags.Extend(requiredExpressions(v.Field(i), subject))
		}
	}

	return diags
}

// defRange returns the range a struct captured with `hcl:",def_range"`, if it
// has one and it was populated. A zero range means the struct was not decoded
// from a block header that has one, so it is no better a subject than the
// caller's.
func defRange(v reflect.Value, ty reflect.Type) (hcl.Range, bool) {
	for i := range ty.NumField() {
		if _, kind, _, ok := hclTag(ty.Field(i)); !ok || kind != "def_range" {
			continue
		}
		r, ok := v.Field(i).Interface().(hcl.Range)
		if ok && r.End.Byte > 0 {
			return r, true
		}
	}
	return hcl.Range{}, false
}

// hclTag reads a struct field's `hcl` tag the way gohcl does: a name, then an
// optional kind ("optional", "block", "label", "remain", "def_range", …). An
// attribute has no kind, so the zero kind and optional are reported separately.
func hclTag(field reflect.StructField) (name, kind string, optional, ok bool) {
	tag, ok := field.Tag.Lookup("hcl")
	if !ok {
		return "", "", false, false
	}

	name, kind, _ = strings.Cut(tag, ",")
	if kind == "optional" {
		return name, "", true, true
	}
	return name, kind, false, true
}

var exprType = reflect.TypeFor[hcl.Expression]()
