package functions

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/customdecode"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("misc", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"typeof": TypeOfFunc,
			"error":  ErrorFunc,
			"cond":   CondFunc,
			"switch": SwitchFunc,
			"try":    TryFunc,
		}
	})
}

// TypeOfFunc returns the friendly name of the type of a given value
var TypeOfFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "value", Type: cty.DynamicPseudoType},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return cty.StringVal(args[0].Type().FriendlyName()), nil
	},
})

var ErrorFunc = function.New(&function.Spec{
	Description: "Returns an error with the given message",
	Params: []function.Parameter{
		{Name: "message", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return args[0], errors.New(args[0].AsString())
	},
})

// CondFunc is a lazy, multi-branch conditional.
//
// Usage: cond(c1, r1, c2, r2, ..., else). Conditions are evaluated in order;
// only the result expression paired with the first truthy condition is
// evaluated. If no condition is truthy, the trailing "else" expression is
// evaluated. Unevaluated expressions produce no side effects.
var CondFunc = function.New(&function.Spec{
	Description: "Lazy conditional: cond(c1, r1, c2, r2, ..., else). Evaluates conditions in order; only the selected result expression is evaluated.",
	VarParam: &function.Parameter{
		Name: "exprs",
		Type: customdecode.ExpressionClosureType,
	},
	// DynamicPseudoType from Type keeps evaluation single-pass. cty calls Type
	// before Impl; evaluating closures in Type (as upstream tryfunc does) would
	// cause side effects to run twice.
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) < 3 || len(args)%2 == 0 {
			return cty.NilType, fmt.Errorf("cond requires an odd number of arguments >= 3 (got %d)", len(args))
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		for i := 0; i+1 < len(args); i += 2 {
			cv, diags := customdecode.ExpressionClosureFromVal(args[i]).Value()
			if diags.HasErrors() {
				return cty.NilVal, diagsToError(fmt.Sprintf("cond: condition #%d", i/2+1), diags)
			}
			bv, err := convert.Convert(cv, cty.Bool)
			if err != nil {
				return cty.NilVal, fmt.Errorf("cond: condition #%d: %w", i/2+1, err)
			}
			if bv.IsNull() {
				return cty.NilVal, fmt.Errorf("cond: condition #%d is null", i/2+1)
			}
			if bv.True() {
				return evalClosure(args[i+1], fmt.Sprintf("cond: result #%d", i/2+1))
			}
		}
		return evalClosure(args[len(args)-1], "cond: else")
	},
})

// SwitchFunc dispatches on a single value against a series of (match, result)
// pairs, with an optional trailing default.
//
// Usage: switch(on, v1, r1, v2, r2, ..., default?). The trailing default is
// optional; with it the total argument count is even, without it odd.
//
// `on` is evaluated exactly once. Then for each (vN, rN) pair, vN is evaluated
// and compared to `on` for equality; on the first match, rN is evaluated and
// returned. If no vN matches, the default (when present) is evaluated and
// returned; if no default was given, switch() errors. Case values past the
// matching arm are not evaluated, nor are unselected results or the default.
//
// Equality uses cty.Value.RawEquals — exact structural equality. Type
// mismatches (e.g. number 200 vs string "200") count as no match rather than
// erroring.
var SwitchFunc = function.New(&function.Spec{
	Description: "Switch dispatch: switch(on, v1, r1, v2, r2, ..., default?). Evaluates `on` once, then each vN until a match; returns the matching rN, or the optional default. Errors if nothing matches and no default was given. Each branch is evaluated at most once.",
	VarParam: &function.Parameter{
		Name: "exprs",
		Type: customdecode.ExpressionClosureType,
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) < 3 {
			return cty.NilType, fmt.Errorf("switch requires at least 3 arguments (got %d)", len(args))
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		hasDefault := len(args)%2 == 0
		caseEnd := len(args)
		if hasDefault {
			caseEnd = len(args) - 1
		}
		on, err := evalClosure(args[0], "switch: on")
		if err != nil {
			return cty.NilVal, err
		}
		for i := 1; i+1 < caseEnd; i += 2 {
			caseNum := (i + 1) / 2
			v, err := evalClosure(args[i], fmt.Sprintf("switch: case #%d value", caseNum))
			if err != nil {
				return cty.NilVal, err
			}
			if on.RawEquals(v) {
				return evalClosure(args[i+1], fmt.Sprintf("switch: case #%d result", caseNum))
			}
		}
		if hasDefault {
			return evalClosure(args[len(args)-1], "switch: default")
		}
		return cty.NilVal, errors.New("switch: no case matched and no default was given")
	},
})

// TryFunc evaluates each argument in order and returns the first whose
// evaluation produces no diagnostics. Unlike upstream tryfunc.TryFunc, this
// returns cty.DynamicPseudoType from the Type callback so each selected
// expression is evaluated exactly once (upstream's concrete-type inference
// evaluates the successful branch twice, which is unsafe for side-effectful
// expressions).
var TryFunc = function.New(&function.Spec{
	Description: "Try each expression in order; return the first that evaluates without error. Evaluates each expression at most once (unlike stock HCL try()).",
	VarParam: &function.Parameter{
		Name: "exprs",
		Type: customdecode.ExpressionClosureType,
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) == 0 {
			return cty.NilType, errors.New("try requires at least one argument")
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		var accum hcl.Diagnostics
		for _, a := range args {
			v, diags := customdecode.ExpressionClosureFromVal(a).Value()
			if diags.HasErrors() {
				accum = append(accum, diags...)
				continue
			}
			// If the value has unknowns we cannot guarantee that a later
			// known-state evaluation would not fail, so bail out dynamically
			// (same conservative behavior as upstream tryfunc).
			if !v.IsWhollyKnown() {
				return cty.DynamicVal, nil
			}
			return v, nil
		}
		return cty.NilVal, diagsToError("no try expression succeeded", accum)
	},
})

func evalClosure(v cty.Value, ctxLabel string) (cty.Value, error) {
	rv, diags := customdecode.ExpressionClosureFromVal(v).Value()
	if diags.HasErrors() {
		return cty.NilVal, diagsToError(ctxLabel, diags)
	}
	return rv, nil
}

func diagsToError(prefix string, diags hcl.Diagnostics) error {
	if len(diags) == 0 {
		return errors.New(prefix)
	}
	var buf strings.Builder
	buf.WriteString(prefix)
	buf.WriteString(":\n")
	for _, d := range diags {
		if d.Subject != nil {
			fmt.Fprintf(&buf, "- %s (at %s)\n  %s\n", d.Summary, d.Subject, d.Detail)
		} else {
			fmt.Fprintf(&buf, "- %s\n  %s\n", d.Summary, d.Detail)
		}
	}
	return errors.New(strings.TrimRight(buf.String(), "\n"))
}
