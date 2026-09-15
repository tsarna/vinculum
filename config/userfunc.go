package config

import (
	"fmt"
	"sync/atomic"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/userfunc"
	jqfunc "github.com/tsarna/hcl-jqfunc"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// The function and jq bodies are decoded by the userfunc and hcl-jqfunc
// extensions rather than by a struct of ours, so these exist only to describe
// them to `vinculum schema`. Keep them in step with those extensions' schemas.

type functionBody struct {
	Params        hcl.Expression `hcl:"params"`
	VariadicParam hcl.Expression `hcl:"variadic_param,optional"`
	Result        hcl.Expression `hcl:"result"`
}

type jqFunctionBody struct {
	Params hcl.Expression `hcl:"params,optional"`
	Query  string         `hcl:"query"`
}

func init() {
	RegisterBlockSchema("function", TypeSchema{
		Sample:  &functionBody{},
		Summary: "Defines a user-callable function from a single expression.",
		Doc: `The function is available by name in every expression.

For anything beyond one expression — typed parameters, locals, branching, loops, or
error handling — a ` + "`func`" + ` in a functy (` + "`.cty`" + `) file is more expressive and is
callable from VCL in exactly the same way.

	function "circle_area" {
	    params = [radius]
	    result = 3.14159 * radius * radius
	}`,
		Attrs: map[string]AttrMeta{
			"params": {
				Summary: "Parameter names, as identifiers rather than strings.",
				Doc:     "Write `params = [a, b]`, not `params = [\"a\", \"b\"]`. Each name is in scope in `result`.",
			},
			"variadic_param": {
				Summary: "Name that collects any extra arguments into a list.",
			},
			"result": {
				Summary: "Expression the function returns.",
				Doc:     "Evaluated with the parameter names in scope.",
				Hint:    HintExpression,
			},
		},
	})

	RegisterBlockSchema("jq", TypeSchema{
		Sample:  &jqFunctionBody{},
		Summary: "Defines a user-callable function backed by a jq query.",
		Doc: `The resulting function takes the input value as its first argument, followed by
any declared ` + "`params`" + `, which are visible inside the query with a ` + "`$`" + ` prefix.

A string input is parsed as JSON, queried, and re-encoded — except that a single
string result is returned as-is rather than double-encoded. Any other input is
passed through as an HCL value and the result returned as one.

	jq "calculate_price" {
	    params = [tax_rate, discount]
	    query  = ".price * (1 + $tax_rate) * (1 - $discount)"
	}`,
		Attrs: map[string]AttrMeta{
			"params": {
				Summary: "Parameter names, as identifiers rather than strings.",
				Doc:     "Each becomes `$name` inside the query.",
			},
			"query": {
				Summary: "The jq query to evaluate.",
				Doc:     "Runs against the function's first argument.",
			},
		},
	})
}

// extractUserFunctions extracts user-defined functions from HCL bodies.
// It processes both HCL native functions ("function" blocks) and jq functions ("jq" blocks).
// A positive maxCallDepth limits how deeply the "function" blocks may recurse;
// see ConfigBuilder.WithMaxCallDepth.
func extractUserFunctions(bodies []hcl.Body, evalCtx *hcl.EvalContext, maxCallDepth int) (map[string]function.Function, []hcl.Body, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	// One limit for every function block in every body, since recursion can be
	// mutual.
	limit := &callLimit{max: int64(maxCallDepth)}

	remainingBodies := make([]hcl.Body, 0)
	allFuncs := make(map[string]function.Function)

	for _, body := range bodies {
		var funcs map[string]function.Function
		var remainingBody hcl.Body
		var funcdiags hcl.Diagnostics
		if maxCallDepth > 0 {
			funcs, remainingBody, funcdiags = decodeDepthLimitedFunctions(body, evalCtx, limit)
		} else {
			funcs, remainingBody, funcdiags = userfunc.DecodeUserFunctions(body, "function", func() *hcl.EvalContext {
				return evalCtx
			})
		}
		jqfuncs, remainingBody, jqdiags := jqfunc.DecodeJqFunctions(remainingBody, "jq")

		diags = diags.Extend(funcdiags)
		diags = diags.Extend(jqdiags)
		if diags.HasErrors() {
			return nil, nil, diags
		}

		remainingBodies = append(remainingBodies, remainingBody)

		for _, funcset := range []map[string]function.Function{funcs, jqfuncs} {
			for name, fn := range funcset {
				if _, exists := allFuncs[name]; exists {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Duplicate function",
						Detail:   fmt.Sprintf("Function %s is already defined", name),
					})
				}
				allFuncs[name] = fn
			}
		}
	}

	if diags.HasErrors() {
		return nil, nil, diags
	}

	return allFuncs, remainingBodies, diags
}

var functionBlockSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{{Type: "function", LabelNames: []string{"name"}}},
}

// functionAttrSchema is functionBody's, which describes the same block.
var functionAttrSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "params", Required: true},
		{Name: "variadic_param"},
		{Name: "result", Required: true},
	},
}

// callLimit is the call depth shared by every depth-limited function of one
// config.
type callLimit struct {
	max   int64
	depth atomic.Int64
	// exceeded holds the error from the call that went past max, until the
	// chain that made it has unwound.
	exceeded atomic.Pointer[error]
}

// decodeDepthLimitedFunctions decodes `function` blocks as the userfunc
// extension does, except that a call made while limit.max calls are already in
// progress fails rather than recursing further.
//
// It cannot be a wrapper around the extension's functions. The extension learns
// a function's type by evaluating its body, then evaluates the body again for
// the value, so a call n levels deep costs 2^n evaluations — a recursion that
// terminates well inside any sane limit still runs for longer than anyone will
// wait, and a wrapper cannot reach inside to stop it. These functions report a
// dynamic return type and evaluate the body once. The value is the same; only
// the type check that precedes it is lost, which is why this is for configs
// that will be checked rather than run.
func decodeDepthLimitedFunctions(body hcl.Body, evalCtx *hcl.EvalContext, limit *callLimit) (map[string]function.Function, hcl.Body, hcl.Diagnostics) {
	content, remain, diags := body.PartialContent(functionBlockSchema)
	if diags.HasErrors() {
		return nil, remain, diags
	}

	funcs := make(map[string]function.Function)
	for _, block := range content.Blocks {
		attrs, blockDiags := block.Body.Content(functionAttrSchema)
		diags = diags.Extend(blockDiags)
		if blockDiags.HasErrors() {
			continue
		}
		var variadic hcl.Expression
		if a := attrs.Attributes["variadic_param"]; a != nil {
			variadic = a.Expr
		}

		params, paramDiags := keywordList(attrs.Attributes["params"].Expr, "Invalid param element", "Each parameter name must be an identifier.")
		diags = diags.Extend(paramDiags)
		if paramDiags.HasErrors() {
			continue
		}
		spec := &function.Spec{Type: function.StaticReturnType(cty.DynamicPseudoType)}
		for _, p := range params {
			spec.Params = append(spec.Params, function.Parameter{Name: p, Type: cty.DynamicPseudoType})
		}

		var varParam string
		if variadic != nil {
			if varParam = hcl.ExprAsKeyword(variadic); varParam == "" {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid variadic_param",
					Detail:   "The variadic parameter name must be an identifier.",
					Subject:  variadic.Range().Ptr(),
				})
				continue
			}
			spec.VarParam = &function.Parameter{Name: varParam, Type: cty.DynamicPseudoType}
		}

		name, result := block.Labels[0], attrs.Attributes["result"].Expr
		spec.Impl = func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			d := limit.depth.Add(1)
			defer func() {
				if limit.depth.Add(-1) == 0 {
					limit.exceeded.Store(nil)
				}
			}()
			if d > limit.max {
				err := fmt.Errorf("function %q was called more than %d deep; a function that calls itself needs a condition that stops it", name, limit.max)
				limit.exceeded.Store(&err)
				return cty.DynamicVal, err
			}

			ctx := evalCtx.NewChild()
			ctx.Variables = make(map[string]cty.Value, len(params)+1)
			for i, p := range params {
				ctx.Variables[p] = args[i]
			}
			if spec.VarParam != nil {
				ctx.Variables[varParam] = cty.TupleVal(args[len(params):])
			}
			val, valDiags := result.Value(ctx)
			if err := limit.exceeded.Load(); valDiags.HasErrors() && err != nil {
				// Every level would otherwise wrap the one below it, and the
				// answer would be the same sentence a hundred times over.
				return cty.DynamicVal, *err
			}
			if valDiags.HasErrors() {
				// Diagnostics implement error, and the caller unwraps them.
				return cty.DynamicVal, valDiags
			}
			return val, nil
		}
		funcs[name] = function.New(spec)
	}
	return funcs, remain, diags
}

// keywordList reads a list of bare identifiers, such as `params = [a, b]`.
func keywordList(expr hcl.Expression, summary, detail string) ([]string, hcl.Diagnostics) {
	exprs, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		return nil, diags
	}
	names := make([]string, 0, len(exprs))
	for _, e := range exprs {
		name := hcl.ExprAsKeyword(e)
		if name == "" {
			return nil, diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  summary,
				Detail:   detail,
				Subject:  e.Range().Ptr(),
			})
		}
		names = append(names, name)
	}
	return names, diags
}
