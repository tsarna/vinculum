package functions

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/userfunc"
	jqfunc "github.com/tsarna/hcl-jqfunc"
	"github.com/zclconf/go-cty/cty/function"
)

// ExtractUserFunctions extracts user-defined functions from HCL bodies
func ExtractUserFunctions(bodies []hcl.Body, evalCtx *hcl.EvalContext) (map[string]function.Function, []hcl.Body, hcl.Diagnostics) {
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
			for name, function := range funcset {
				if _, exists := allFuncs[name]; exists {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Duplicate function",
						Detail:   fmt.Sprintf("Function %s is already defined", name),
					})
				}
				allFuncs[name] = function
			}
		}
	}

	if diags.HasErrors() {
		return nil, nil, diags
	}

	return allFuncs, remainingBodies, diags
}
