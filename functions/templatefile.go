package functions

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// MakeTemplateFileFunc constructs a templatefile(path, vars) function.
// It reads an HCL template file, evaluates it with the given vars merged into
// the standard VCL variable scope (env, sys, var, metric, etc.), and returns
// the result. The path is resolved relative to baseDir.
//
// constants is the live Config.Constants map (captured by reference so that
// var/metric/bus/server/client entries added during block processing are
// visible at evaluation time). funcsGetter returns all available functions
// excluding templatefile itself, preventing recursion.
func MakeTemplateFileFunc(
	baseDir string,
	constants map[string]cty.Value,
	funcsGetter func() map[string]function.Function,
) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "path", Type: cty.String},
			{Name: "vars", Type: cty.DynamicPseudoType},
		},
		Type: function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			path := args[0].AsString()
			vars := args[1]

			// Resolve path relative to baseDir
			if !filepath.IsAbs(path) {
				path = filepath.Join(baseDir, path)
			}
			path = filepath.Clean(path)

			// Read template file
			src, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					return cty.NilVal, fmt.Errorf("no file exists at %s", path)
				}
				return cty.NilVal, fmt.Errorf("failed to read template %s: %w", path, err)
			}

			// Parse as HCL template expression
			expr, diags := hclsyntax.ParseTemplate(src, path, hcl.Pos{Line: 1, Column: 1})
			if diags.HasErrors() {
				return cty.NilVal, diags
			}

			// Build child variables from vars argument
			varMap := make(map[string]cty.Value)
			if !vars.IsNull() && vars.IsKnown() {
				ty := vars.Type()
				if !ty.IsObjectType() && !ty.IsMapType() {
					return cty.NilVal, fmt.Errorf("vars must be an object, got %s", ty.FriendlyName())
				}
				for k, v := range vars.AsValueMap() {
					if !hclsyntax.ValidIdentifier(k) {
						return cty.NilVal, fmt.Errorf("invalid template variable name %q: must be a valid identifier", k)
					}
					varMap[k] = v
				}
			}

			// Parent context: standard VCL variables + all functions (minus templatefile)
			parentCtx := &hcl.EvalContext{
				Variables: constants,
				Functions: funcsGetter(),
			}
			// Child context: template-specific vars (shadow any same-named constants)
			ctx := parentCtx.NewChild()
			ctx.Variables = varMap

			result, diags := expr.Value(ctx)
			if diags.HasErrors() {
				return cty.NilVal, diags
			}

			return result, nil
		},
	})
}
