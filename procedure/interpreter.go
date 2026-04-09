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

	case *Break:
		condVal, diags := s.Condition.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		if condVal.True() {
			return &Signal{Kind: SignalBreak}, nil
		}
		return nil, nil

	case *Continue:
		condVal, diags := s.Condition.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		if condVal.True() {
			return &Signal{Kind: SignalContinue}, nil
		}
		return nil, nil

	case *IfChain:
		return executeIfChain(s, scope, parentCtx)

	case *Range:
		return executeRange(s, scope, parentCtx)

	case *Return:
		val, diags := s.Expr.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		return &Signal{Kind: SignalReturn, Value: val}, nil

	case *While:
		return executeWhile(s, scope, parentCtx)

	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unknown statement type",
			Detail:   "Internal error: unrecognized IR node.",
		}}
	}
}

// executeIfChain evaluates an if/elif/else chain. It tests each branch
// condition in order and executes the body of the first truthy branch.
// If no branch matches, the else body (if any) is executed.
func executeIfChain(chain *IfChain, scope *Scope, parentCtx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	for _, branch := range chain.Branches {
		evalCtx := scopeEvalContext(scope, parentCtx)
		condVal, diags := branch.Condition.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		if condVal.True() {
			childScope := NewScope(scope)
			_, sig, execDiags := Execute(branch.Body, childScope, parentCtx)
			if execDiags.HasErrors() {
				return nil, execDiags
			}
			return sig, nil
		}
	}
	// No branch matched — execute else if present
	if chain.Else != nil {
		childScope := NewScope(scope)
		_, sig, execDiags := Execute(chain.Else, childScope, parentCtx)
		if execDiags.HasErrors() {
			return nil, execDiags
		}
		return sig, nil
	}
	return nil, nil
}

// executeWhile runs a while loop. On each iteration it evaluates the condition,
// creates a child scope, and executes the body. Break consumes the signal and
// exits; Continue consumes the signal and proceeds to the next iteration;
// Return propagates upward.
func executeWhile(w *While, scope *Scope, parentCtx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	for {
		evalCtx := scopeEvalContext(scope, parentCtx)
		condVal, diags := w.Condition.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		if !condVal.True() {
			break
		}

		childScope := NewScope(scope)
		_, sig, execDiags := Execute(w.Body, childScope, parentCtx)
		if execDiags.HasErrors() {
			return nil, execDiags
		}
		if sig != nil {
			switch sig.Kind {
			case SignalBreak:
				return nil, nil // consume break, exit loop
			case SignalContinue:
				continue // consume continue, next iteration
			case SignalReturn:
				return sig, nil // propagate return
			}
		}
	}
	return nil, nil
}

// executeRange iterates over a collection (list, tuple, set, or map/object),
// binding each element to the item variable. For maps/objects, the item is an
// object with .key and .value attributes. Signal handling matches executeWhile.
func executeRange(r *Range, scope *Scope, parentCtx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	evalCtx := scopeEvalContext(scope, parentCtx)
	collVal, diags := r.Collection.Value(evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	collType := collVal.Type()

	var items []cty.Value

	switch {
	case collType.IsListType() || collType.IsTupleType() || collType.IsSetType():
		for it := collVal.ElementIterator(); it.Next(); {
			_, v := it.Element()
			items = append(items, v)
		}
	case collType.IsObjectType() || collType.IsMapType():
		for it := collVal.ElementIterator(); it.Next(); {
			k, v := it.Element()
			entry := cty.ObjectVal(map[string]cty.Value{
				"key":   k,
				"value": v,
			})
			items = append(items, entry)
		}
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid range collection",
			Detail:   "range requires a list, set, map, or object value.",
			Subject:  r.Collection.Range().Ptr(),
		}}
	}

	for _, item := range items {
		childScope := NewScope(scope)
		childScope.Set(r.ItemName, item)

		_, sig, execDiags := Execute(r.Body, childScope, parentCtx)
		if execDiags.HasErrors() {
			return nil, execDiags
		}
		if sig != nil {
			switch sig.Kind {
			case SignalBreak:
				return nil, nil
			case SignalContinue:
				continue
			case SignalReturn:
				return sig, nil
			}
		}
	}
	return nil, nil
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
