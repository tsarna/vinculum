package procedure

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// Execute runs a compiled list of statements in the given scope.
// It returns the procedure result value and any signal that should propagate.
// If no return is encountered, the result is cty.NullVal(cty.DynamicPseudoType).
func Execute(stmts []Statement, scope *Scope, parentCtx *hcl.EvalContext) (cty.Value, *Signal, hcl.Diagnostics) {
	for _, stmt := range stmts {
		sig, diags := executeStatement(stmt, scope, parentCtx)
		if diags.HasErrors() {
			return cty.NilVal, nil, diags
		}
		if sig != nil && sig.Kind != SignalNone {
			return sig.Value, sig, nil
		}
	}
	return cty.NullVal(cty.DynamicPseudoType), nil, nil
}

func executeStatement(stmt Statement, scope *Scope, parentCtx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	evalCtx := scopeEvalContext(scope, parentCtx)

	switch s := stmt.(type) {
	case *Assignment:
		val, diags := s.Expr.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		scope.Set(s.Name, val)
		return nil, nil

	case *Return:
		val, diags := s.Expr.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		return &Signal{Kind: SignalReturn, Value: val}, nil

	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unknown statement type",
			Detail:   "Internal error: unrecognized IR node.",
		}}
	}
}

// scopeEvalContext builds an HCL EvalContext from the scope chain and the
// parent context (which provides functions and config-level variables).
func scopeEvalContext(scope *Scope, parentCtx *hcl.EvalContext) *hcl.EvalContext {
	vars := make(map[string]cty.Value)
	// Copy parent variables first (config constants, env, sys, etc.)
	if parentCtx != nil {
		for k, v := range parentCtx.Variables {
			vars[k] = v
		}
	}
	// Overlay procedure scope variables (these take precedence)
	for k, v := range scope.ToMap() {
		vars[k] = v
	}

	ctx := &hcl.EvalContext{
		Variables: vars,
	}
	if parentCtx != nil {
		ctx.Functions = parentCtx.Functions
	}
	return ctx
}
