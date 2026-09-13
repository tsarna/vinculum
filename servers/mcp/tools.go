package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

		if err := checkArgs(rawArgs, def.Params); err != nil {
			// A tool error rather than a protocol error: the arguments are the
			// model's, so it should see what was wrong and call again.
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("invalid arguments to %s: %v", def.Name, err)}},
				IsError: true,
			}, nil
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

// jsonArgsToCty converts JSON-unmarshaled arguments to typed cty values,
// substituting each param's default for the ones the caller left out. A client
// is free to ignore the default published in the input schema, so applying it
// here is what makes the attribute mean anything.
func jsonArgsToCty(rawArgs map[string]any, params []ParamDef) map[string]cty.Value {
	result := make(map[string]cty.Value, len(rawArgs))
	for _, p := range params {
		v, ok := rawArgs[p.Name]
		// A null is treated as absent where there is a default to fall back
		// on, as checkArgs treats it: sending `"kind": null` and leaving kind
		// out are the same request. Without a default it still arrives as null.
		if (!ok || v == nil) && p.DefaultVal != nil {
			v, ok = p.DefaultVal, true
		}
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

// checkArgs enforces what a tool's input schema publishes. A client is free to
// ignore the schema, so type, required and enum mean nothing unless the server
// checks them — and a config written to trust its schema would otherwise meet
// the values it rules out. An absent or null optional argument is left to its
// default, which already matched its type at config time.
func checkArgs(rawArgs map[string]any, params []ParamDef) error {
	for _, p := range params {
		v, ok := rawArgs[p.Name]
		if !ok || v == nil {
			if p.Required {
				return fmt.Errorf("missing required argument %q", p.Name)
			}
			continue
		}
		if got := jsonTypeName(v); got != p.Type {
			return fmt.Errorf("argument %q must be %s, not %s", p.Name, withArticle(p.Type), withArticle(got))
		}
		if len(p.Enum) > 0 && !inEnum(v, p.Enum) {
			return fmt.Errorf("argument %q must be one of %s, not %s", p.Name, enumList(p.Enum), jsonLiteral(v))
		}
	}
	return nil
}

// jsonTypeName names a decoded JSON value in the vocabulary a param's type uses.
func jsonTypeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func withArticle(typeName string) string {
	if strings.IndexAny(typeName[:1], "aeiou") == 0 {
		return "an " + typeName
	}
	return "a " + typeName
}

// inEnum reports whether v is one of the enum's entries. The type has already
// been checked, so a string or boolean compares directly. A number cannot: JSON
// decodes to float64, while an entry was stored from a cty literal as whatever
// Go type CtyToAny chose — an int for a whole number — and printing them to
// compare breaks from a million up, where float64 prints as 1e+06.
func inEnum(v any, enum []any) bool {
	f, isNumber := v.(float64)
	for _, e := range enum {
		if isNumber {
			if n, ok := asFloat(e); ok && n == f {
				return true
			}
			continue
		}
		if e == v {
			return true
		}
	}
	return false
}

// asFloat widens a numeric enum entry. CtyToAny returns an int for a whole
// number and a float64 otherwise; int64 is what a ParamDef built by hand holds.
func asFloat(x any) (float64, bool) {
	switch n := x.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func enumList(enum []any) string {
	parts := make([]string, len(enum))
	for i, e := range enum {
		parts[i] = jsonLiteral(e)
	}
	return strings.Join(parts, ", ")
}

func jsonLiteral(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func anyToCty(v any) cty.Value {
	switch val := v.(type) {
	case string:
		return cty.StringVal(val)
	case float64:
		return cty.NumberFloatVal(val)
	case int:
		// A whole-number default is stored as an int (go2cty2go.CtyToAny), and
		// it has to arrive as the number it is: falling through to the string
		// case below made `default = 5` compare unequal to 5.
		return cty.NumberIntVal(int64(val))
	case int64:
		return cty.NumberIntVal(val)
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
	// A null carries a type — cty.NullVal(cty.String) *is* a string — so the
	// branch below would take it and panic in AsString. This is the ordinary
	// shape of a miss rather than an exotic one: man::page() returns null for a
	// topic nothing is named, so a config serving documentation reaches it on
	// the first bad lookup a model makes.
	if val.IsNull() {
		return nil, fmt.Errorf("tool action returned null; expected a string, mcp::error(), or mcp::image(). Wrap an expression that may be null in coalesce() or cond()")
	}
	if !val.IsKnown() {
		return nil, fmt.Errorf("tool action returned an unknown value; expected a string, mcp::error(), or mcp::image()")
	}

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

	return nil, fmt.Errorf("tool action returned unsupported type %s; expected string, mcp::error(), or mcp::image()", val.Type().FriendlyName())
}
