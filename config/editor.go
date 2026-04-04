package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty/function"
)

// EditorProcessor compiles an editor block's type-specific body into a cty function.
// It is called once per editor block during early config processing.
type EditorProcessor func(
	config *Config,
	evalCtxFn func() *hcl.EvalContext,
	def *EditorDefinition,
) (function.Function, hcl.Diagnostics)

// EditorDefinition holds the parsed outer editor block, common across all editor types.
type EditorDefinition struct {
	Type          string
	Name          string
	Params        []string // parameter names extracted from params expression
	VariadicParam string   // optional variadic parameter name
	Body          hcl.Body // remaining body for type-specific parsing
	DefRange      hcl.Range
}

var editorRegistry = map[string]EditorProcessor{}

// RegisterEditorType registers an editor type. Sub-packages call this from init().
func RegisterEditorType(typeName string, p EditorProcessor) {
	recordPlugin("editor." + typeName)
	editorRegistry[typeName] = p
}

// editorOuterBody is used to decode the common attributes of an editor block,
// leaving the type-specific content in RemainingBody.
type editorOuterBody struct {
	Params        hcl.Expression `hcl:"params,optional"`
	VariadicParam hcl.Expression `hcl:"variadic_param,optional"`
	RemainingBody hcl.Body       `hcl:",remain"`
}

var editorBlockSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "editor", LabelNames: []string{"type", "name"}},
	},
}

// extractEditorFunctions processes editor blocks early from HCL bodies,
// producing cty functions. Returns remaining bodies with editor blocks removed.
func extractEditorFunctions(bodies []hcl.Body, config *Config, evalCtxFn func() *hcl.EvalContext) (map[string]function.Function, []hcl.Body, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	allFuncs := make(map[string]function.Function)
	remainingBodies := make([]hcl.Body, 0, len(bodies))

	for _, body := range bodies {
		content, remainBody, partialDiags := body.PartialContent(editorBlockSchema)
		diags = diags.Extend(partialDiags)
		remainingBodies = append(remainingBodies, remainBody)

		for _, block := range content.Blocks {
			editorType := block.Labels[0]
			editorName := block.Labels[1]

			processor, ok := editorRegistry[editorType]
			if !ok {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Unknown editor type",
					Detail:   fmt.Sprintf("Unknown editor type %q", editorType),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}

			// Decode common outer attributes, leaving the rest for the type processor
			outer := &editorOuterBody{}
			decodeDiags := gohcl.DecodeBody(block.Body, evalCtxFn(), outer)
			diags = diags.Extend(decodeDiags)
			if decodeDiags.HasErrors() {
				continue
			}

			def := &EditorDefinition{
				Type:     editorType,
				Name:     editorName,
				Body:     outer.RemainingBody,
				DefRange: block.DefRange,
			}

			// Extract parameter names from params expression (list of identifiers)
			if outer.Params != nil {
				for _, traversal := range outer.Params.Variables() {
					def.Params = append(def.Params, traversal.RootName())
				}
			}

			// Extract variadic param name from variadic_param expression (single identifier)
			if outer.VariadicParam != nil {
				vars := outer.VariadicParam.Variables()
				if len(vars) == 1 {
					def.VariadicParam = vars[0].RootName()
				} else if len(vars) > 1 {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Invalid variadic_param",
						Detail:   "variadic_param must be a single identifier",
						Subject:  outer.VariadicParam.StartRange().Ptr(),
					})
					continue
				}
			}

			fn, procDiags := processor(config, evalCtxFn, def)
			diags = diags.Extend(procDiags)
			if procDiags.HasErrors() {
				continue
			}

			if _, exists := allFuncs[editorName]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate editor",
					Detail:   fmt.Sprintf("Editor %q is already defined", editorName),
					Subject:  block.DefRange.Ptr(),
				})
				continue
			}

			allFuncs[editorName] = fn
		}
	}

	return allFuncs, remainingBodies, diags
}
