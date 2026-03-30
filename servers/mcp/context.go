package mcp

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

// buildResourceEvalContext builds a per-request eval context for a resource handler.
func buildResourceEvalContext(
	goCtx context.Context,
	parent *hcl.EvalContext,
	serverName, uri string,
	templateVars map[string]string,
) (*hcl.EvalContext, error) {
	b := hclutil.NewEvalContext(goCtx).
		WithStringAttribute("server_name", serverName).
		WithStringAttribute("uri", uri)
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
) (*hcl.EvalContext, error) {
	var argsVal cty.Value
	if len(args) == 0 {
		argsVal = cty.EmptyObjectVal
	} else {
		argsVal = cty.ObjectVal(args)
	}
	return hclutil.NewEvalContext(goCtx).
		WithStringAttribute("server_name", serverName).
		WithStringAttribute("tool_name", toolName).
		WithAttribute("args", argsVal).
		BuildEvalContext(parent)
}

// buildPromptEvalContext builds a per-request eval context for a prompt handler.
func buildPromptEvalContext(
	goCtx context.Context,
	parent *hcl.EvalContext,
	serverName, promptName string,
	args map[string]cty.Value,
) (*hcl.EvalContext, error) {
	var argsVal cty.Value
	if len(args) == 0 {
		argsVal = cty.EmptyObjectVal
	} else {
		argsVal = cty.ObjectVal(args)
	}
	return hclutil.NewEvalContext(goCtx).
		WithStringAttribute("server_name", serverName).
		WithStringAttribute("prompt_name", promptName).
		WithAttribute("args", argsVal).
		BuildEvalContext(parent)
}
