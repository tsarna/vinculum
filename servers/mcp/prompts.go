package mcp

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tsarna/vinculum/functions"
	"github.com/zclconf/go-cty/cty"
)

// PromptDef holds the parsed definition of a single MCP prompt.
type PromptDef struct {
	Name        string
	Description string
	Params      []ParamDef
	Action      hcl.Expression
}

func registerPrompts(s *Server, defs []PromptDef) {
	for _, def := range defs {
		def := def // capture for closure

		var promptArgs []*sdkmcp.PromptArgument
		for _, p := range def.Params {
			promptArgs = append(promptArgs, &sdkmcp.PromptArgument{
				Name:        p.Name,
				Description: p.Description,
				Required:    p.Required,
			})
		}

		s.sdkServer.AddPrompt(&sdkmcp.Prompt{
			Name:        def.Name,
			Description: def.Description,
			Arguments:   promptArgs,
		}, makePromptHandler(s, def))
	}
}

func makePromptHandler(s *Server, def PromptDef) sdkmcp.PromptHandler {
	return func(goCtx context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
		// Prompt arguments are always map[string]string at the protocol level
		args := make(map[string]cty.Value, len(req.Params.Arguments))
		for k, v := range req.Params.Arguments {
			args[k] = cty.StringVal(v)
		}

		evalCtx, err := buildPromptEvalContext(goCtx, s.parentEvalCtx, s.name, def.Name, args)
		if err != nil {
			return nil, fmt.Errorf("building eval context: %w", err)
		}

		result, diags := def.Action.Value(evalCtx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("prompt action: %s", diags.Error())
		}

		messages, err := ctyToPromptMessages(result)
		if err != nil {
			return nil, err
		}

		return &sdkmcp.GetPromptResult{
			Description: def.Description,
			Messages:    messages,
		}, nil
	}
}

func ctyToPromptMessages(val cty.Value) ([]*sdkmcp.PromptMessage, error) {
	// Single MCPResult capsule
	if r := functions.GetMCPResult(val); r != nil {
		msg, err := mcpResultToPromptMessage(r)
		if err != nil {
			return nil, err
		}
		return []*sdkmcp.PromptMessage{msg}, nil
	}

	// List or tuple of MCPResult capsules
	valType := val.Type()
	if valType.IsListType() || valType.IsTupleType() {
		var messages []*sdkmcp.PromptMessage
		var i int
		for it := val.ElementIterator(); it.Next(); i++ {
			_, elem := it.Element()
			r := functions.GetMCPResult(elem)
			if r == nil {
				return nil, fmt.Errorf("prompt action list element %d is not an mcp_user_message() or mcp_assistant_message()", i)
			}
			msg, err := mcpResultToPromptMessage(r)
			if err != nil {
				return nil, fmt.Errorf("prompt action list element %d: %w", i, err)
			}
			messages = append(messages, msg)
		}
		return messages, nil
	}

	return nil, fmt.Errorf("prompt action returned unsupported type %s; expected mcp_user_message() or mcp_assistant_message()", val.Type().FriendlyName())
}

func mcpResultToPromptMessage(r *functions.MCPResult) (*sdkmcp.PromptMessage, error) {
	switch r.Kind {
	case "user_message":
		return &sdkmcp.PromptMessage{
			Role:    sdkmcp.Role("user"),
			Content: &sdkmcp.TextContent{Text: r.Text},
		}, nil
	case "assistant_message":
		return &sdkmcp.PromptMessage{
			Role:    sdkmcp.Role("assistant"),
			Content: &sdkmcp.TextContent{Text: r.Text},
		}, nil
	default:
		return nil, fmt.Errorf("mcp_result kind %q is not valid for prompt message; use mcp_user_message() or mcp_assistant_message()", r.Kind)
	}
}
