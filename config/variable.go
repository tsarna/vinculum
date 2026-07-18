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
	// functyVarInit holds the unevaluated initializer for each functy-declared
	// top-level var, evaluated in FinishProcessing (after consts and VCL vars are
	// resolved).
	functyVarInit map[string]hcl.Expression
}

func NewVariableBlockHandler() *VariableBlockHandler {
	return &VariableBlockHandler{
		variables:     make(map[string]*types.Variable),
		functyVarInit: make(map[string]hcl.Expression),
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
	// Fold functy top-level `var` declarations into the same var pool: a functy
	// var becomes equivalent to a VCL `var` block — mutable, exposed as
	// var.<name>, registered in CtyVarMap — and shares the duplicate-name check.
	diags := h.foldFunctyVars(config)

	for name, v := range h.variables {
		config.CtyVarMap[name] = types.NewVariableCapsule(v)
	}
	if len(config.CtyVarMap) > 0 {
		config.Constants["var"] = cty.ObjectVal(config.CtyVarMap)
	}
	return diags
}

// foldFunctyVars creates a types.Variable for each functy top-level `var`,
// applying its declared type constraint and stashing its initializer for
// evaluation in FinishProcessing (after consts/VCL vars resolve). Duplicate names
// — across VCL vars and other functy vars — are reported.
func (h *VariableBlockHandler) foldFunctyVars(config *Config) hcl.Diagnostics {
	var diags hcl.Diagnostics
	if config.functyState == nil || config.functyState.result == nil {
		return diags
	}

	for _, decl := range config.functyState.result.Vars {
		if decl.Namespace != "" {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Namespaced var is not supported",
				Detail: fmt.Sprintf(
					"var %q is declared in namespace %q, but a vinculum `var` is global — it becomes a mutable var.%s with no namespace to scope it to. Declare top-level `var`s in the global namespace.",
					decl.Name, decl.Namespace, decl.Name),
				Subject: decl.DefRange.Ptr(),
			})
			continue
		}
		if _, dup := h.variables[decl.Name]; dup {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate variable",
				Detail:   fmt.Sprintf("Variable %q is already defined", decl.Name),
				Subject:  decl.DefRange.Ptr(),
			})
			continue
		}
		v := types.NewVariable(cty.NullVal(cty.DynamicPseudoType))
		if decl.Type != nil {
			v.SetConstraint(decl.Type)
		}
		h.variables[decl.Name] = v
		if decl.Expr != nil {
			h.functyVarInit[decl.Name] = decl.Expr
		}
	}

	return diags
}

// setFunctyVarValues evaluates functy top-level var initializers and sets each
// var's initial value (enforcing its type constraint via Set). It is called from
// Build() right after the FinishPreprocessing pass — so consts are resolved and
// the values are in place before the Process phase, where consumers (asserts,
// subscriptions) read them. VCL var blocks set their value in Process; functy
// vars have no block, so they are handled here. A functy var initializer may
// reference consts/ambients/functions; referencing another var's initial value
// is not ordering-guaranteed (as with VCL vars).
func (h *VariableBlockHandler) setFunctyVarValues(config *Config) hcl.Diagnostics {
	var diags hcl.Diagnostics
	for name, expr := range h.functyVarInit {
		val, evalDiags := expr.Value(config.evalCtx)
		diags = diags.Extend(evalDiags)
		if evalDiags.HasErrors() {
			continue
		}
		if _, err := h.variables[name].Set(context.Background(), []cty.Value{val}); err != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to set variable initial value",
				Detail:   fmt.Sprintf("var %q: %s", name, err),
				Subject:  expr.Range().Ptr(),
			})
		}
	}
	return diags
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

	// Handle optional type constraint. The `type` attribute accepts the functy
	// type-annotation grammar in two forms, resolved through the shared functy
	// TypeResolver so VCL and .cty declarations accept identical syntax and the
	// same host-registered named types:
	//   - a type spec: `type = number`, `type = list(string)`, `type = bus`
	//     — the expression is handed to ResolveType, not evaluated as a value;
	//   - a string of a type spec (back-compat): `type = "number"` — when the
	//     expression evaluates to a string, it is parsed with ParseType.
	if typeAttr, hasType := attrs["type"]; hasType {
		constraint, cDiags := resolveVarTypeConstraint(config, typeAttr)
		diags = diags.Extend(cDiags)
		if diags.HasErrors() {
			return diags
		}
		v.SetConstraint(constraint)
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

// resolveVarTypeConstraint resolves a `var` block's `type` attribute into a type
// constraint via the shared functy TypeResolver. If the attribute evaluates to a
// string it is parsed as a type spec (back-compat: `type = "number"`); otherwise
// the attribute expression itself is resolved as a type spec (`type = number`,
// `type = list(string)`, `type = bus`) without first evaluating it as a value.
func resolveVarTypeConstraint(config *Config, typeAttr *hcl.Attribute) (types.TypeConstraint, hcl.Diagnostics) {
	resolver := config.functyState.resolver()

	// Try the string form first: a value that evaluates to a string is a type
	// spec written as a string. Evaluation errors are expected for the type-spec
	// form (e.g. `number` is not a value), so they are not surfaced here — we
	// fall through to ResolveType.
	if val, valDiags := typeAttr.Expr.Value(config.evalCtx); !valDiags.HasErrors() && val.Type() == cty.String {
		constraint, diags := resolver.ParseType([]byte(val.AsString()), "<var type>")
		// The quoted-string form is deprecated in favor of the type-spec form.
		// Surfaced as a warn-severity diagnostic (non-fatal); the CLI prints
		// warnings (see cmd.reportWarnings).
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagWarning,
			Summary:  "Deprecated var type string",
			Detail: fmt.Sprintf(
				"Writing a var type as a quoted string is deprecated; write it as a type spec instead: type = %s (not type = %q). The string form will be removed in a future release.",
				val.AsString(), val.AsString()),
			Subject: typeAttr.Expr.Range().Ptr(),
		})
		return constraint, diags
	}

	constraint, diags := resolver.ResolveType(typeAttr.Expr)
	return constraint, diags
}
