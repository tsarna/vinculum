package mcp

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	// The three builders below. Every one carries server_name and an args
	// object; what differs is how the request is identified and where args
	// comes from.
	cfg.RegisterContextSchema("mcp-resource", cfg.ContextSchema{
		Summary: "Evaluated when a client reads an MCP resource.",
		Fields: []cfg.ContextField{
			{Name: "server_name", Type: "string", Summary: "Name of the enclosing `server \"mcp\"` block."},
			{
				Name: "uri", Type: "string",
				Summary: "The URI that was requested.",
				Doc:     "The concrete URI, not the template — `\"file:///logs/app.log\"` rather than `\"file:///logs/{name}\"`.",
			},
			{
				Name: "args", Type: cfg.CtxTypeObject,
				Summary: "Variables captured from the URI template.",
				Doc:     "Empty for a static resource, which has nothing to capture.",
			},
		},
	})

	cfg.RegisterContextSchema("mcp-tool", cfg.ContextSchema{
		Summary: "Evaluated when a client calls an MCP tool.",
		Fields: []cfg.ContextField{
			{Name: "server_name", Type: "string", Summary: "Name of the enclosing `server \"mcp\"` block."},
			{Name: "tool_name", Type: "string", Summary: "Name of the tool being called."},
			{
				Name: "args", Type: cfg.CtxTypeObject,
				Summary: "The call's arguments, keyed by the tool's declared `param` names.",
				Doc:     "Already validated against each param's declared type.",
			},
		},
	})

	cfg.RegisterContextSchema("mcp-prompt", cfg.ContextSchema{
		Summary: "Evaluated when a client requests an MCP prompt.",
		Fields: []cfg.ContextField{
			{Name: "server_name", Type: "string", Summary: "Name of the enclosing `server \"mcp\"` block."},
			{Name: "prompt_name", Type: "string", Summary: "Name of the prompt being requested."},
			{
				Name: "args", Type: cfg.CtxTypeObject,
				Summary: "The request's arguments, keyed by the prompt's declared `param` names.",
			},
		},
	})
}

// buildResourceEvalContext builds a per-request eval context for a resource handler.
func buildResourceEvalContext(
	goCtx context.Context,
	parent *hcl.EvalContext,
	serverName, uri string,
	templateVars map[string]string,
) (*hcl.EvalContext, error) {
	var argsVal cty.Value
	if len(templateVars) == 0 {
		argsVal = cty.EmptyObjectVal
	} else {
		m := make(map[string]cty.Value, len(templateVars))
		for k, v := range templateVars {
			m[k] = cty.StringVal(v)
		}
		argsVal = cty.ObjectVal(m)
	}
	return hclutil.NewEvalContext(goCtx).
		WithStringAttribute("server_name", serverName).
		WithStringAttribute("uri", uri).
		WithAttribute("args", argsVal).
		BuildEvalContext(parent)
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
