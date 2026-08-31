package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

// Renamed block attributes, and why they need a mechanism of their own.
//
// config/renames.go covers functions and `ctx` fields, the two things the
// deferred-reference checker resolves by name. It cannot cover a block
// attribute: gohcl rejects an unknown attribute while decoding the body, long
// before the checker runs, and all it can say is
//
//	An argument named "auto_ack" is not expected here.
//
// which is true, useless, and gives an upgrading author nothing to search for.
//
// The interception is to look for the retired names *before* decoding, with a
// schema that lists only them. hcl's PartialContent hands back exactly the
// attributes asked for and leaves everything else in the remaining body, so
// this reads the old spelling as itself rather than parsing it back out of
// somebody else's error text.
//
// Retired names are declared per site rather than globally, and that is
// load-bearing: `auto_delete` is retired on `client "sqs_receiver"` and is a
// perfectly good attribute of a `rabbitmq` receiver's `declare` block, which is
// nested inside a body that does retire `auto_ack`. A global table would have
// to guess; a scoped one is told.

// RenamedAttr records what one retired block attribute became.
type RenamedAttr struct {
	// Now is the attribute to use instead, or empty when the thing was removed
	// with no replacement — Note then has to carry the explanation.
	Now string

	// Since is the release the change landed in.
	Since string

	// Note is appended to the diagnostic. For a rename that also changed how
	// values are spelled, this is where the mapping goes, because the old
	// value is an expression and translating it automatically would mean
	// evaluating it to say what it should have been.
	Note string
}

// RenameSpec says which retired attributes to look for in a body, and which of
// its nested blocks to look inside. Nesting is explicit because a receiver's
// retired attribute lives one block down from where processing starts —
// `consumer` on redis_stream, `receiver` on rabbitmq and kafka — while the
// blocks beside it must be left alone.
type RenameSpec struct {
	// Attrs are the retired attributes of this body.
	Attrs map[string]RenamedAttr

	// Blocks are the nested blocks to descend into.
	Blocks []RenamedInBlock
}

// RenamedInBlock names one nested block type and what to look for inside it.
type RenamedInBlock struct {
	Type   string
	Labels []string
	Spec   RenameSpec
}

// CheckRenamedAttrs reports every retired attribute written in body, or in the
// nested blocks the spec names. Call it before decoding, so the author is told
// what their attribute became rather than that it is not expected here.
//
// It reports; it does not repair. A configuration naming a retired attribute
// does not load, which is the point: the replacement usually differs in more
// than spelling, and silently accepting the old name would leave two ways to
// say one thing for as long as anyone remembered to maintain both.
func CheckRenamedAttrs(body hcl.Body, spec RenameSpec) hcl.Diagnostics {
	if len(spec.Attrs) == 0 && len(spec.Blocks) == 0 {
		return nil
	}

	schema := &hcl.BodySchema{}
	for name := range spec.Attrs {
		schema.Attributes = append(schema.Attributes, hcl.AttributeSchema{Name: name})
	}
	for _, b := range spec.Blocks {
		schema.Blocks = append(schema.Blocks, hcl.BlockHeaderSchema{
			Type:       b.Type,
			LabelNames: b.Labels,
		})
	}

	// Everything not named in the schema stays in the remaining body, and the
	// diagnostics PartialContent produces are about the schema rather than
	// about the configuration — a missing block this pass does not care about.
	// Only what it found is of interest.
	content, _, _ := body.PartialContent(schema)

	var diags hcl.Diagnostics
	for name, attr := range content.Attributes {
		diags = append(diags, renamedAttrDiag(name, spec.Attrs[name], attr.NameRange))
	}
	for _, block := range content.Blocks {
		for _, b := range spec.Blocks {
			if b.Type == block.Type {
				diags = append(diags, CheckRenamedAttrs(block.Body, b.Spec)...)
			}
		}
	}
	return diags
}

// renamedAttrDiag renders one retired attribute.
func renamedAttrDiag(old string, r RenamedAttr, at hcl.Range) *hcl.Diagnostic {
	summary := fmt.Sprintf("%q was removed", old)
	if r.Now != "" {
		summary = fmt.Sprintf("%q is now %q", old, r.Now)
	}
	if r.Since != "" {
		summary += " (since " + r.Since + ")"
	}

	detail := r.Note
	if r.Now != "" && detail == "" {
		detail = fmt.Sprintf("Use %q instead.", r.Now)
	}

	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  at.Ptr(),
	}
}
