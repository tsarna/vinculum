package mcp

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/internal/hclutil"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// buildResourceEvalContext builds a per-request eval context for a resource handler.
func buildResourceEvalContext(
	goCtx context.Context,
	parent *hcl.EvalContext,
	serverName, uri string,
	templateVars map[string]string,
	extraFuncs map[string]function.Function,
) (*hcl.EvalContext, hcl.Diagnostics) {
	b := hclutil.NewContext(goCtx).
		WithStringAttribute("server_name", serverName).
		WithStringAttribute("uri", uri).
		WithFunctions(extraFuncs)
	for k, v := range templateVars {
		b = b.WithStringAttribute(k, v)
	}
	return b.BuildEvalContext(parent)
}

// buildToolEvalContext builds a per-request eval context for a tool handler.
func buildToolEvalContext(
	goCtx context.Context,
	parent *hcl.EvalContext,
	serverName, toolName string,
	args map[string]cty.Value,
	extraFuncs map[string]function.Function,
) (*hcl.EvalContext, hcl.Diagnostics) {
	var argsVal cty.Value
	if len(args) == 0 {
		argsVal = cty.EmptyObjectVal
	} else {
		argsVal = cty.ObjectVal(args)
	}
	return hclutil.NewContext(goCtx).
		WithStringAttribute("server_name", serverName).
		WithStringAttribute("tool_name", toolName).
		WithAttribute("args", argsVal).
		WithFunctions(extraFuncs).
		BuildEvalContext(parent)
}

// buildPromptEvalContext builds a per-request eval context for a prompt handler.
func buildPromptEvalContext(
	goCtx context.Context,
	parent *hcl.EvalContext,
	serverName, promptName string,
	args map[string]cty.Value,
	extraFuncs map[string]function.Function,
) (*hcl.EvalContext, hcl.Diagnostics) {
	var argsVal cty.Value
	if len(args) == 0 {
		argsVal = cty.EmptyObjectVal
	} else {
		argsVal = cty.ObjectVal(args)
	}
	return hclutil.NewContext(goCtx).
		WithStringAttribute("server_name", serverName).
		WithStringAttribute("prompt_name", promptName).
		WithAttribute("args", argsVal).
		WithFunctions(extraFuncs).
		BuildEvalContext(parent)
}
