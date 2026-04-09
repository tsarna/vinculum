package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/tsarna/vinculum/procedure"
	"github.com/zclconf/go-cty/cty/function"
)

var procedureBlockSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "procedure", LabelNames: []string{"name"}},
	},
}

// extractProcedureFunctions processes procedure blocks early from HCL bodies,
// compiling each into a cty function. Returns remaining bodies with procedure
// blocks removed.
func extractProcedureFunctions(bodies []hcl.Body, config *Config, evalCtxFn func() *hcl.EvalContext) (map[string]function.Function, []hcl.Body, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	allFuncs := make(map[string]function.Function)
	remainingBodies := make([]hcl.Body, 0, len(bodies))

	for _, body := range bodies {
		content, remainBody, partialDiags := body.PartialContent(procedureBlockSchema)
		diags = diags.Extend(partialDiags)
		remainingBodies = append(remainingBodies, remainBody)

		for _, block := range content.Blocks {
			name := block.Labels[0]

			syntaxBody, ok := block.Body.(*hclsyntax.Body)
			if !ok {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Unsupported HCL format",
					Detail:   "Procedure blocks must use HCL native syntax.",
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}

			// Parse spec block
			spec, remaining, specDiags := procedure.ParseSpec(syntaxBody)
			diags = diags.Extend(specDiags)
			if specDiags.HasErrors() {
				continue
			}

			// Compile the procedure body
			stmts, compileDiags := procedure.Compile(remaining, block.DefRange.Filename)
			diags = diags.Extend(compileDiags)
			if compileDiags.HasErrors() {
				continue
			}

			// Build the function
			fn := procedure.BuildFunction(spec, stmts, evalCtxFn)

			if _, exists := allFuncs[name]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate procedure",
					Detail:   fmt.Sprintf("Procedure %q is already defined.", name),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}

			allFuncs[name] = fn
		}
	}

	return allFuncs, remainingBodies, diags
}
