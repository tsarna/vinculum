package procedure

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// requiredMarker is the Go type backing the "required" sentinel capsule value.
type requiredMarker struct{}

// RequiredType is the cty capsule type for the "required" sentinel.
var RequiredType = cty.CapsuleWithOps("required", reflect.TypeOf(requiredMarker{}), &cty.CapsuleOps{})

// RequiredVal is the sentinel value that marks a procedure parameter as required.
var RequiredVal = cty.CapsuleVal(RequiredType, &requiredMarker{})

// isRequired returns true if the value is the "required" sentinel.
func isRequired(v cty.Value) bool {
	return v.Type() == RequiredType
}

// ProcSpec holds the parsed spec block information for a procedure.
type ProcSpec struct {
	ParamNames    []string    // parameter names in source order
	ParamDefaults []cty.Value // parallel to ParamNames; cty.NilVal means required
	VariadicParam string      // empty if no variadic parameter
}

// ParamsEvalContext returns an EvalContext with the "required" sentinel bound,
// for use when evaluating parameter default expressions.
func ParamsEvalContext() *hcl.EvalContext {
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"required": RequiredVal,
		},
	}
}

// ParseSpec extracts and parses the spec block from a procedure body.
// It returns the ProcSpec and the remaining body (with spec removed).
// If no spec block is present, returns an empty ProcSpec.
func ParseSpec(body *hclsyntax.Body) (*ProcSpec, *hclsyntax.Body, hcl.Diagnostics) {
	spec := &ProcSpec{}

	// Find spec blocks
	var specBlocks []*hclsyntax.Block
	var otherBlocks []*hclsyntax.Block
	for _, block := range body.Blocks {
		if block.Type == "spec" {
			specBlocks = append(specBlocks, block)
		} else {
			otherBlocks = append(otherBlocks, block)
		}
	}

	if len(specBlocks) == 0 {
		return spec, body, nil
	}

	if len(specBlocks) > 1 {
		return nil, nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate spec block",
			Detail:   "A procedure may have at most one spec block.",
			Subject:  specBlocks[1].DefRange().Ptr(),
		}}
	}

	specBlock := specBlocks[0]

	// Verify spec is before all other items
	specEnd := specBlock.Range().End.Byte
	for _, attr := range body.Attributes {
		if attr.SrcRange.Start.Byte < specEnd {
			return nil, nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Statement before spec",
				Detail:   "The spec block must appear before any statements in the procedure body.",
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
	}
	for _, block := range otherBlocks {
		if block.Range().Start.Byte < specEnd {
			return nil, nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Block before spec",
				Detail:   "The spec block must appear before any other blocks in the procedure body.",
				Subject:  block.DefRange().Ptr(),
			}}
		}
	}

	if len(specBlock.Labels) > 0 {
		return nil, nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid spec block",
			Detail:   "The spec block takes no labels.",
			Subject:  specBlock.DefRange().Ptr(),
		}}
	}

	// Parse spec body
	diags := parseSpecBody(specBlock.Body, spec)
	if diags.HasErrors() {
		return nil, nil, diags
	}

	// Build remaining body without the spec block
	remaining := &hclsyntax.Body{
		Attributes: body.Attributes,
		Blocks:     otherBlocks,
		SrcRange:   body.SrcRange,
		EndRange:   body.EndRange,
	}

	return spec, remaining, nil
}

func parseSpecBody(body *hclsyntax.Body, spec *ProcSpec) hcl.Diagnostics {
	var diags hcl.Diagnostics

	// Process variadic_param attribute
	if attr, ok := body.Attributes["variadic_param"]; ok {
		vars := attr.Expr.Variables()
		if len(vars) == 1 && len(vars[0]) == 1 {
			spec.VariadicParam = vars[0].RootName()
		} else {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid variadic_param",
				Detail:   "variadic_param must be a single identifier.",
				Subject:  attr.SrcRange.Ptr(),
			})
			return diags
		}
	}

	// Check for unknown attributes
	for name, attr := range body.Attributes {
		switch name {
		case "variadic_param", "returns":
			// known, skip
		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unsupported spec attribute",
				Detail:   fmt.Sprintf("Unknown attribute %q in spec block.", name),
				Subject:  attr.SrcRange.Ptr(),
			})
		}
	}
	if diags.HasErrors() {
		return diags
	}

	// Process params block
	for _, block := range body.Blocks {
		if block.Type != "params" {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unknown block in spec",
				Detail:   fmt.Sprintf("Unexpected block type %q in spec. Only params is allowed.", block.Type),
				Subject:  block.DefRange().Ptr(),
			})
			continue
		}

		if len(block.Labels) > 0 {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid params block",
				Detail:   "The params block takes no labels.",
				Subject:  block.DefRange().Ptr(),
			})
			continue
		}

		paramDiags := parseParams(block.Body, spec)
		diags = diags.Extend(paramDiags)
	}

	return diags
}

func parseParams(body *hclsyntax.Body, spec *ProcSpec) hcl.Diagnostics {
	if len(body.Blocks) > 0 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unexpected block in params",
			Detail:   "The params block should only contain attribute definitions.",
			Subject:  body.Blocks[0].DefRange().Ptr(),
		}}
	}

	// Sort attributes by byte offset to get source order
	type paramAttr struct {
		name   string
		attr   *hclsyntax.Attribute
		offset int
	}
	var params []paramAttr
	for name, attr := range body.Attributes {
		params = append(params, paramAttr{name: name, attr: attr, offset: attr.SrcRange.Start.Byte})
	}
	sort.Slice(params, func(i, j int) bool {
		return params[i].offset < params[j].offset
	})

	// Evaluate defaults and validate required-before-optional ordering
	seenOptional := false
	for _, p := range params {
		// Check for reserved names
		switch {
		case p.name == "return" || p.name == "break" || p.name == "continue":
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Reserved parameter name",
				Detail:   fmt.Sprintf("%q is a reserved name and cannot be used as a parameter.", p.name),
				Subject:  p.attr.SrcRange.Ptr(),
			}}
		case strings.HasPrefix(p.name, "_"):
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Discard parameter name",
				Detail:   fmt.Sprintf("Parameter name %q starts with _ and would be treated as a discard.", p.name),
				Subject:  p.attr.SrcRange.Ptr(),
			}}
		}

		// Evaluate the default value with "required" sentinel available
		val, diags := p.attr.Expr.Value(ParamsEvalContext())
		if diags.HasErrors() {
			return diags
		}

		if isRequired(val) {
			// Required parameter
			if seenOptional {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Required parameter after optional",
					Detail:   fmt.Sprintf("Required parameter %q must come before all optional parameters.", p.name),
					Subject:  p.attr.SrcRange.Ptr(),
				}}
			}
			spec.ParamNames = append(spec.ParamNames, p.name)
			spec.ParamDefaults = append(spec.ParamDefaults, cty.NilVal)
		} else {
			// Optional parameter with default (null is a valid default)
			seenOptional = true
			spec.ParamNames = append(spec.ParamNames, p.name)
			spec.ParamDefaults = append(spec.ParamDefaults, val)
		}
	}

	return nil
}

// BuildFunction builds a cty/function.Function from the compiled procedure.
//
// The cty function framework does not support optional parameters directly.
// We place only the required parameters in Spec.Params and use VarParam to
// capture any additional positional arguments (optional params + user-level
// variadic). The Impl closure then maps these back to parameter names,
// applying defaults for any that were not provided.
func BuildFunction(spec *ProcSpec, stmts []Statement, evalCtxFn func() *hcl.EvalContext) function.Function {
	// Split required from optional params
	var requiredParams []function.Parameter
	for i, name := range spec.ParamNames {
		if spec.ParamDefaults[i] == cty.NilVal {
			requiredParams = append(requiredParams, function.Parameter{
				Name:      name,
				Type:      cty.DynamicPseudoType,
				AllowNull: true,
			})
		}
	}
	numRequired := len(requiredParams)

	funcSpec := &function.Spec{
		Params: requiredParams,
		// VarParam collects optional args (and user-level variadic if present)
		VarParam: &function.Parameter{
			Name: "args",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			// args[0..numRequired-1] are the required params
			// args[numRequired..] are the extra positional args from VarParam
			extraArgs := args[numRequired:]
			numOptional := len(spec.ParamNames) - numRequired

			// Validate: not too many args (unless variadic)
			maxExtra := numOptional
			if spec.VariadicParam == "" && len(extraArgs) > maxExtra {
				return cty.NilVal, fmt.Errorf(
					"too many arguments; expected at most %d, got %d",
					len(spec.ParamNames), numRequired+len(extraArgs),
				)
			}

			scope := NewScope(nil)

			// Bind required params
			for i := 0; i < numRequired; i++ {
				scope.Set(spec.ParamNames[i], args[i])
			}

			// Bind optional params (from extra args or defaults)
			for i := numRequired; i < len(spec.ParamNames); i++ {
				extraIdx := i - numRequired
				if extraIdx < len(extraArgs) {
					scope.Set(spec.ParamNames[i], extraArgs[extraIdx])
				} else {
					scope.Set(spec.ParamNames[i], spec.ParamDefaults[i])
				}
			}

			// Bind variadic param
			if spec.VariadicParam != "" {
				varStart := numOptional
				if varStart < len(extraArgs) {
					varArgs := extraArgs[varStart:]
					vals := make([]cty.Value, len(varArgs))
					copy(vals, varArgs)
					scope.Set(spec.VariadicParam, cty.TupleVal(vals))
				} else {
					scope.Set(spec.VariadicParam, cty.EmptyTupleVal)
				}
			}

			parentCtx := evalCtxFn()
			result, _, diags := Execute(stmts, scope, parentCtx)
			if diags.HasErrors() {
				return cty.NilVal, fmt.Errorf("%s", diags.Error())
			}
			return result, nil
		},
	}

	return function.New(funcSpec)
}
