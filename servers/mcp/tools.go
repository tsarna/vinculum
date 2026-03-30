package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tsarna/vinculum/functions"
	"github.com/zclconf/go-cty/cty"
)

// ToolDef holds the parsed definition of a single MCP tool.
type ToolDef struct {
	Name        string
	Description string
	Params      []ParamDef
	Action      hcl.Expression
}

func registerTools(s *Server, defs []ToolDef) error {
	for _, def := range defs {
		def := def // capture for closure
		schema, err := BuildToolInputSchema(def.Params)
		if err != nil {
			return fmt.Errorf("tool %q: %w", def.Name, err)
		}
		s.sdkServer.AddTool(&sdkmcp.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: json.RawMessage(schema),
		}, makeToolHandler(s, def))
	}
	return nil
}

func makeToolHandler(s *Server, def ToolDef) sdkmcp.ToolHandler {
	return func(goCtx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		// Unmarshal JSON arguments into typed cty values
		var rawArgs map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &rawArgs); err != nil {
				return nil, fmt.Errorf("parsing tool arguments: %w", err)
			}
		}

		args := jsonArgsToCty(rawArgs, def.Params)

		evalCtx, err := buildToolEvalContext(goCtx, s.parentEvalCtx, s.name, def.Name, args)
		if err != nil {
			return nil, fmt.Errorf("building eval context: %w", err)
		}

		result, diags := def.Action.Value(evalCtx)
		if diags.HasErrors() {
			// Return as tool error, not protocol error, so the LLM can see it
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: diags.Error()}},
				IsError: true,
			}, nil
		}

		return ctyToCallToolResult(result)
	}
}

// jsonArgsToCty converts JSON-unmarshaled arguments to typed cty values.
func jsonArgsToCty(rawArgs map[string]any, params []ParamDef) map[string]cty.Value {
	if len(rawArgs) == 0 {
		return nil
	}
	result := make(map[string]cty.Value, len(rawArgs))
	for _, p := range params {
		v, ok := rawArgs[p.Name]
		if !ok {
			continue
		}
		result[p.Name] = anyToCty(v)
	}
	// Also include any args not in param list (pass through)
	for k, v := range rawArgs {
		if _, exists := result[k]; !exists {
			result[k] = anyToCty(v)
		}
	}
	return result
}

func anyToCty(v any) cty.Value {
	switch val := v.(type) {
	case string:
		return cty.StringVal(val)
	case float64:
		return cty.NumberFloatVal(val)
	case bool:
		if val {
			return cty.True
		}
		return cty.False
	case nil:
		return cty.NullVal(cty.DynamicPseudoType)
	default:
		// Fallback: convert to string representation
		return cty.StringVal(fmt.Sprintf("%v", v))
	}
}

func ctyToCallToolResult(val cty.Value) (*sdkmcp.CallToolResult, error) {
	if val.Type() == cty.String {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: val.AsString()}},
		}, nil
	}

	if r := functions.GetMCPResult(val); r != nil {
		switch r.Kind {
		case "error":
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: r.Text}},
				IsError: true,
			}, nil
		case "image":
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.ImageContent{
					Data:     r.Data,
					MIMEType: r.MIMEType,
				}},
			}, nil
		default:
			return nil, fmt.Errorf("mcp_result kind %q is not valid for tool result", r.Kind)
		}
	}

	return nil, fmt.Errorf("tool action returned unsupported type %s; expected string, mcp_error(), or mcp_image()", val.Type().FriendlyName())
}
