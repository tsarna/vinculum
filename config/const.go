package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/types"
)

type ConstBlockHandler struct {
	BlockHandlerBase

	consts hcl.Attributes
	// functyConstraints maps a functy-declared const name to its type
	// constraint (if the .cty declared `const x: T = …`), applied via Coerce
	// after the value is evaluated in the shared const pass.
	functyConstraints map[string]types.TypeConstraint
}

func NewConstBlockHandler() *ConstBlockHandler {
	return &ConstBlockHandler{
		consts:            make(hcl.Attributes),
		functyConstraints: make(map[string]types.TypeConstraint),
	}
}

func (b *ConstBlockHandler) Preprocess(block *hcl.Block) hcl.Diagnostics {
	attrs, diags := block.Body.JustAttributes()
	if diags.HasErrors() {
		return diags
	}

	for name, attr := range attrs {
		if _, exists := b.consts[name]; exists {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate attribute",
				Detail:   fmt.Sprintf("Attribute %s at %v is already defined at %v", name, attr.NameRange, b.consts[name].NameRange),
				Subject:  &attr.NameRange,
			})
		}
		b.consts[name] = attr
	}

	return diags
}

func (b *ConstBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	// Fold functy top-level `const` declarations into the same pool, so they
	// participate in the same attribute-level dependency sort and evaluation as
	// VCL consts (a functy const may reference a VCL const, or vice versa, in any
	// order) and share the cross-surface duplicate-name check.
	diags := b.foldFunctyConsts(config)
	if diags.HasErrors() {
		return diags
	}

	// Build set of already-known variable root names (ambients, etc.)
	// so dependency sorting doesn't reject references to them.
	known := make(map[string]bool, len(config.Constants))
	for k := range config.Constants {
		known[k] = true
	}

	attrs, sortDiags := SortAttributesByDependencies(b.consts, known)
	diags = diags.Extend(sortDiags)
	if diags.HasErrors() {
		return diags
	}

	for _, attribute := range attrs {
		value, evalDiags := attribute.Expr.Value(config.evalCtx)
		diags = diags.Extend(evalDiags)
		if evalDiags.HasErrors() {
			continue
		}
		// Enforce a functy const's declared type constraint (if any).
		if constraint := b.functyConstraints[attribute.Name]; constraint != nil {
			coerced, err := constraint.Coerce(value)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Const type mismatch",
					Detail:   fmt.Sprintf("Const %q does not satisfy its declared type: %s", attribute.Name, err),
					Subject:  attribute.Expr.Range().Ptr(),
				})
				continue
			}
			value = coerced
		}
		config.Constants[attribute.Name] = value
	}

	return diags
}

// foldFunctyConsts merges the functy Result.Consts into the const attribute pool
// as synthesized hcl.Attributes (Name + unevaluated Expr), recording any declared
// type constraint. Duplicate names — across VCL consts and other functy consts —
// are reported, reusing the same collision surface as VCL const blocks.
func (b *ConstBlockHandler) foldFunctyConsts(config *Config) hcl.Diagnostics {
	var diags hcl.Diagnostics
	if config.functyState == nil || config.functyState.result == nil {
		return diags
	}

	for _, decl := range config.functyState.result.Consts {
		if decl.Expr == nil {
			continue
		}
		if existing, dup := b.consts[decl.Name]; dup {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate attribute",
				Detail:   fmt.Sprintf("Const %s at %v is already defined at %v", decl.Name, decl.DefRange, existing.NameRange),
				Subject:  decl.DefRange.Ptr(),
			})
			continue
		}
		b.consts[decl.Name] = &hcl.Attribute{
			Name:      decl.Name,
			Expr:      decl.Expr,
			Range:     decl.DefRange,
			NameRange: decl.DefRange,
		}
		if decl.Type != nil {
			b.functyConstraints[decl.Name] = decl.Type
		}
	}

	return diags
}
