package config

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

type VariableBlockHandler struct {
	BlockHandlerBase
	variables map[string]*types.Variable
}

func NewVariableBlockHandler() *VariableBlockHandler {
	return &VariableBlockHandler{
		variables: make(map[string]*types.Variable),
	}
}

func (h *VariableBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "var." + block.Labels[0], nil
}

func (h *VariableBlockHandler) Preprocess(block *hcl.Block) hcl.Diagnostics {
	name := block.Labels[0]
	if _, exists := h.variables[name]; exists {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate variable",
			Detail:   fmt.Sprintf("Variable %q is already defined", name),
			Subject:  block.DefRange.Ptr(),
		}}
	}
	h.variables[name] = types.NewVariable(cty.NullVal(cty.DynamicPseudoType))
	return nil
}

func (h *VariableBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	for name, v := range h.variables {
		config.CtyVarMap[name] = types.NewVariableCapsule(v)
	}
	if len(config.CtyVarMap) > 0 {
		config.Constants["var"] = cty.ObjectVal(config.CtyVarMap)
	}
	return nil
}

func (h *VariableBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	name := block.Labels[0]
	v := h.variables[name]

	// Parse optional attributes
	attrs, diags := block.Body.JustAttributes()
	if diags.HasErrors() {
		return diags
	}

	// Handle optional nullable constraint (default true)
	v.SetNullable(true)
	if nullableAttr, hasNullable := attrs["nullable"]; hasNullable {
		nullableVal, nullableDiags := nullableAttr.Expr.Value(config.evalCtx)
		diags = diags.Extend(nullableDiags)
		if diags.HasErrors() {
			return diags
		}
		if nullableVal.Type() != cty.Bool {
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid nullable value",
				Detail:   `The "nullable" attribute must be a bool`,
				Subject:  nullableAttr.Expr.StartRange().Ptr(),
			})
		}
		v.SetNullable(nullableVal.True())
	}

	// Handle optional type constraint
	if typeAttr, hasType := attrs["type"]; hasType {
		typeVal, typeDiags := typeAttr.Expr.Value(config.evalCtx)
		diags = diags.Extend(typeDiags)
		if diags.HasErrors() {
			return diags
		}
		if typeVal.Type() != cty.String {
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid type constraint",
				Detail:   `The "type" attribute must be a string`,
				Subject:  typeAttr.Expr.StartRange().Ptr(),
			})
		}
		v.SetTypeName(typeVal.AsString())
	}

	valueAttr, hasValue := attrs["value"]
	if !hasValue {
		return nil
	}

	val, valDiags := valueAttr.Expr.Value(config.evalCtx)
	diags = diags.Extend(valDiags)
	if diags.HasErrors() {
		return diags
	}

	_, err := v.Set(context.Background(), []cty.Value{val})
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Failed to set variable initial value",
			Detail:   err.Error(),
			Subject:  valueAttr.Expr.StartRange().Ptr(),
		})
	}

	return diags
}
