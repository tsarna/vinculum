package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

type ConstBlockHandler struct {
	BlockHandlerBase

	consts hcl.Attributes
}

func NewConstBlockHandler() *ConstBlockHandler {
	return &ConstBlockHandler{
		consts: make(hcl.Attributes),
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
	attrs, diags := SortAttributesByDependencies(b.consts)
	if diags.HasErrors() {
		return diags
	}

	for _, attribute := range attrs {
		value, evalDiags := attribute.Expr.Value(config.evalCtx)
		diags = diags.Extend(evalDiags)
		config.Constants[attribute.Name] = value
	}

	return diags
}
