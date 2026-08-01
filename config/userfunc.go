package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/userfunc"
	jqfunc "github.com/tsarna/hcl-jqfunc"
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
func extractUserFunctions(bodies []hcl.Body, evalCtx *hcl.EvalContext) (map[string]function.Function, []hcl.Body, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	remainingBodies := make([]hcl.Body, 0)
	allFuncs := make(map[string]function.Function)

	for _, body := range bodies {
		funcs, remainingBody, funcdiags := userfunc.DecodeUserFunctions(body, "function", func() *hcl.EvalContext {
			return evalCtx
		})
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
