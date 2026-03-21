package functions

import (
	"encoding/base64"
	"fmt"
	"reflect"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// MCPResult holds typed return values from MCP action expressions.
// It is wrapped in a cty capsule so it can pass through HCL evaluation.
type MCPResult struct {
	// Kind is one of: "image", "error", "user_message", "assistant_message"
	Kind     string
	Text     string
	Data     []byte // decoded bytes for images
	MIMEType string
}

// MCPResultCapsuleType is the cty capsule type for MCPResult values.
var MCPResultCapsuleType = cty.CapsuleWithOps("mcp_result", reflect.TypeOf(MCPResult{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		r := val.(*MCPResult)
		return fmt.Sprintf("mcp_result(%s)", r.Kind)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "mcp_result"
	},
})

// NewMCPResultCapsule wraps an MCPResult in a cty capsule value.
func NewMCPResultCapsule(r MCPResult) cty.Value {
	return cty.CapsuleVal(MCPResultCapsuleType, &r)
}

// GetMCPResult extracts an MCPResult from a cty capsule value.
// Returns nil if the value is not an MCPResult capsule.
func GetMCPResult(val cty.Value) *MCPResult {
	if val.Type() != MCPResultCapsuleType {
		return nil
	}
	return val.EncapsulatedValue().(*MCPResult)
}

// MCPImageFunc returns an mcp_image function that produces image content.
var MCPImageFunc = function.New(&function.Spec{
	Description: "Returns image content for an MCP resource or tool result",
	Params: []function.Parameter{
		{Name: "data", Type: cty.String, Description: "Base64-encoded image data"},
		{Name: "mime_type", Type: cty.String, Description: "MIME type of the image"},
	},
	Type: function.StaticReturnType(MCPResultCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		b64 := args[0].AsString()
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return cty.NilVal, fmt.Errorf("mcp_image: invalid base64 data: %w", err)
		}
		return NewMCPResultCapsule(MCPResult{
			Kind:     "image",
			Data:     decoded,
			MIMEType: args[1].AsString(),
		}), nil
	},
})

// MCPErrorFunc returns an mcp_error function that signals a tool error result.
var MCPErrorFunc = function.New(&function.Spec{
	Description: "Returns an error result for an MCP tool call",
	Params: []function.Parameter{
		{Name: "message", Type: cty.String, Description: "Error message"},
	},
	Type: function.StaticReturnType(MCPResultCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return NewMCPResultCapsule(MCPResult{
			Kind: "error",
			Text: args[0].AsString(),
		}), nil
	},
})

// MCPUserMessageFunc returns an mcp_user_message function for prompt results.
var MCPUserMessageFunc = function.New(&function.Spec{
	Description: "Returns a user-role message for an MCP prompt result",
	Params: []function.Parameter{
		{Name: "content", Type: cty.String, Description: "Message content"},
	},
	Type: function.StaticReturnType(MCPResultCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return NewMCPResultCapsule(MCPResult{
			Kind: "user_message",
			Text: args[0].AsString(),
		}), nil
	},
})

// MCPAssistantMessageFunc returns an mcp_assistant_message function for prompt results.
var MCPAssistantMessageFunc = function.New(&function.Spec{
	Description: "Returns an assistant-role message for an MCP prompt result (few-shot example)",
	Params: []function.Parameter{
		{Name: "content", Type: cty.String, Description: "Message content"},
	},
	Type: function.StaticReturnType(MCPResultCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return NewMCPResultCapsule(MCPResult{
			Kind: "assistant_message",
			Text: args[0].AsString(),
		}), nil
	},
})

// GetMcpFunctions returns all MCP-specific cty functions for injection into
// per-request eval contexts. These are NOT added to the global stdlib context.
func GetMcpFunctions() map[string]function.Function {
	return map[string]function.Function{
		"mcp_image":             MCPImageFunc,
		"mcp_error":             MCPErrorFunc,
		"mcp_user_message":      MCPUserMessageFunc,
		"mcp_assistant_message": MCPAssistantMessageFunc,
	}
}
