package mcp

import (
	"context"
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callArgs encodes a map as json.RawMessage for use in CallToolParams.Arguments.
// CallToolParams.Arguments is typed as 'any'; using json.RawMessage ensures the
// client sends it as a JSON object rather than base64-encoding it as []byte.
func callArgs(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return json.RawMessage(b)
}

func TestToolEchoString(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{
			Name:        "echo",
			Description: "Echo input",
			Params: []ParamDef{
				{Name: "text", Type: "string", Required: true},
			},
			Action: parseExpr(t, `ctx.args.text`),
		},
	}, nil)

	cs := connectInMemory(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "echo",
		Arguments: callArgs(map[string]any{"text": "hello world"}),
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	txt, ok := res.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "hello world", txt.Text)
}

func TestToolStringInterpolation(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{
			Name:        "greet",
			Description: "Greet someone",
			Params: []ParamDef{
				{Name: "name", Type: "string", Required: true},
			},
			Action: parseExpr(t, `"Hello, ${ctx.args.name}!"`),
		},
	}, nil)

	cs := connectInMemory(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "greet",
		Arguments: callArgs(map[string]any{"name": "World"}),
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	txt, ok := res.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Hello, World!", txt.Text)
}

func TestToolListsTools(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{Name: "tool_a", Description: "Tool A", Action: parseExpr(t, `"a"`)},
		{Name: "tool_b", Description: "Tool B", Action: parseExpr(t, `"b"`)},
	}, nil)

	cs := connectInMemory(t, srv)

	list, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, list.Tools, 2)
}

func TestToolContextAttributes(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{
			Name:        "meta",
			Description: "Return metadata",
			Action:      parseExpr(t, `"server=${ctx.server_name} tool=${ctx.tool_name}"`),
		},
	}, nil)

	cs := connectInMemory(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "meta"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	txt, ok := res.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "server=test tool=meta", txt.Text)
}

func TestToolMcpError(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{
			Name:        "failing",
			Description: "Always fails",
			Action:      parseExpr(t, `mcp_error("something went wrong")`),
		},
	}, nil)

	cs := connectInMemory(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "failing"})
	require.NoError(t, err) // protocol-level no error
	assert.True(t, res.IsError)
	txt, ok := res.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "something went wrong", txt.Text)
}

func TestToolSchemaGenerated(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{
			Name:        "typed_tool",
			Description: "A typed tool",
			Params: []ParamDef{
				{Name: "query", Type: "string", Required: true, Description: "Search query"},
				{Name: "limit", Type: "number"},
				{Name: "active", Type: "boolean"},
			},
			Action: parseExpr(t, `ctx.args.query`),
		},
	}, nil)

	cs := connectInMemory(t, srv)

	list, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, list.Tools, 1)

	// InputSchema should be a map with the right structure
	schema, ok := list.Tools[0].InputSchema.(map[string]any)
	require.True(t, ok, "InputSchema should be a map")
	assert.Equal(t, "object", schema["type"])

	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "query")
	assert.Contains(t, props, "limit")
	assert.Contains(t, props, "active")

	required, ok := schema["required"].([]any)
	require.True(t, ok)
	assert.Contains(t, required, "query")
}
