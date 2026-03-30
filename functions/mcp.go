package functions

import (
	"encoding/base64"
	"fmt"
	"reflect"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("mcp", func(_ *cfg.Config) map[string]function.Function {
		return GetMcpFunctions()
	})
}

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
//
// Accepted call forms:
//
//	mcp_image(base64_string, mime_type)  - original form, both args required
//	mcp_image(bytes_capsule)             - mime_type taken from bytes content type
//	mcp_image(bytes_capsule, mime_type)  - mime_type overrides bytes content type
var MCPImageFunc = function.New(&function.Spec{
	Description: "Returns image content for an MCP resource or tool result",
	Params: []function.Parameter{
		{Name: "data", Type: cty.DynamicPseudoType, Description: "Base64-encoded image string or bytes capsule"},
	},
	VarParam: &function.Parameter{
		Name:        "mime_type",
		Type:        cty.String,
		Description: "MIME type of the image (required when data is a base64 string; optional when data is a bytes capsule)",
	},
	Type: function.StaticReturnType(MCPResultCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		var imageData []byte
		var mimeType string

		switch {
		case args[0].Type() == cty.String:
			var err error
			imageData, err = base64.StdEncoding.DecodeString(args[0].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("mcp_image: invalid base64 data: %w", err)
			}
			if len(args) < 2 {
				return cty.NilVal, fmt.Errorf("mcp_image: mime_type is required when data is a base64 string")
			}
			mimeType = args[1].AsString()
		default:
			b, err := types.GetBytesFromValue(args[0])
			if err != nil {
				return cty.NilVal, fmt.Errorf("mcp_image: data must be a base64 string or bytes value, got %s", args[0].Type().FriendlyName())
			}
			imageData = b.Data
			mimeType = b.ContentType
			if len(args) > 1 {
				mimeType = args[1].AsString()
			}
		}

		return NewMCPResultCapsule(MCPResult{
			Kind:     "image",
			Data:     imageData,
			MIMEType: mimeType,
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

// GetMcpFunctions returns all MCP-specific cty functions.
// These are included in the global function set so they are available in any
// action expression, including bus subscriptions that construct MCP values
// for async handlers.
func GetMcpFunctions() map[string]function.Function {
	return map[string]function.Function{
		"mcp_image":             MCPImageFunc,
		"mcp_error":             MCPErrorFunc,
		"mcp_user_message":      MCPUserMessageFunc,
		"mcp_assistant_message": MCPAssistantMessageFunc,
	}
}
