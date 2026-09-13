package mcp

import (
	"context"
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
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

// A typed null used to crash the whole server: cty.NullVal(cty.String) *is* a
// string, so the conversion took the string branch and AsString panicked on the
// SDK's own goroutine, where nothing recovers. It is the ordinary shape of a
// miss — man::page() returns null for a topic nothing is named.
//
// The nulls here are *typed*. A bare `null` literal is dynamic-typed and always
// fell through to "unsupported type", so it would pass this test on the code
// that crashed. `true ? null : x` takes x's type.
func TestToolNullResultIsReportedNotPanicked(t *testing.T) {
	for name, src := range map[string]string{
		"string":  `true ? null : ""`,
		"capsule": `true ? null : mcp::error("x")`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t, nil, []ToolDef{
				{Name: "nothing", Description: "Returns a typed null", Action: parseExpr(t, src)},
			}, nil)

			cs := connectInMemory(t, srv)

			_, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "nothing"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "returned null")
			assert.Contains(t, err.Error(), "coalesce()", "the error says how to fix it")

			// And the server is still there to answer.
			_, err = cs.ListTools(context.Background(), nil)
			assert.NoError(t, err)
		})
	}
}

// A tool's input schema is published to the client, but a client may ignore it,
// so the server checks the arguments before the action sees them. Each refusal is
// a tool error, so the model reads what was wrong and can call again.
func TestToolArgumentsAreCheckedAgainstTheSchema(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{{
		Name:        "lookup",
		Description: "Look something up",
		Params: []ParamDef{
			{Name: "topic", Type: "string", Required: true},
			{Name: "kind", Type: "string", DefaultVal: "", Enum: []any{"", "block", "function"}},
			{Name: "depth", Type: "number", Enum: []any{int64(1), int64(2)}},
			{Name: "verbose", Type: "boolean"},
			// An int, as CtyToAny stores a whole-number literal, and large
			// enough that printing it would no longer match JSON's float64.
			{Name: "big", Type: "number", Enum: []any{int(1000000)}},
			{Name: "shape", Type: "string"},
		},
		Action: parseExpr(t, `"topic=${ctx.args.topic} kind=${ctx.args.kind}"`),
	}}, nil)

	cs := connectInMemory(t, srv)
	call := func(args map[string]any) *sdkmcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "lookup", Arguments: callArgs(args)})
		require.NoError(t, err)
		return res
	}
	text := func(res *sdkmcp.CallToolResult) string {
		t.Helper()
		txt, ok := res.Content[0].(*sdkmcp.TextContent)
		require.True(t, ok)
		return txt.Text
	}

	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"missing required":    {map[string]any{}, `missing required argument "topic"`},
		"null required":       {map[string]any{"topic": nil}, `missing required argument "topic"`},
		"wrong type":          {map[string]any{"topic": 5.0}, `"topic" must be a string, not a number`},
		"not in enum":         {map[string]any{"topic": "x", "kind": "bogus"}, `"kind" must be one of "", "block", "function", not "bogus"`},
		"number not in enum":  {map[string]any{"topic": "x", "depth": 3.0}, `"depth" must be one of 1, 2, not 3`},
		"boolean of a string": {map[string]any{"topic": "x", "verbose": "yes"}, `"verbose" must be a boolean, not a string`},
	} {
		t.Run(name, func(t *testing.T) {
			res := call(tc.args)
			assert.True(t, res.IsError, "refused as a tool error the model can read")
			assert.Contains(t, text(res), tc.want)
		})
	}

	// What the schema allows gets through, a number enum matches a JSON number,
	// the default applies, and an argument the schema does not name passes
	// through as it always has.
	res := call(map[string]any{"topic": "x", "depth": 2.0, "extra": "ignored"})
	assert.False(t, res.IsError, text(res))
	assert.Equal(t, "topic=x kind=", text(res))

	// A large whole-number enum entry still matches, and a null optional
	// argument gets its default exactly as an absent one does.
	res = call(map[string]any{"topic": "x", "big": 1000000.0, "kind": nil})
	assert.False(t, res.IsError, text(res))
	assert.Equal(t, "topic=x kind=", text(res))

	// The type names read as English.
	res = call(map[string]any{"topic": "x", "shape": map[string]any{}})
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), `"shape" must be a string, not an object`)
}

// A default is a number when its param is, whether the argument was left out
// or sent as null. A whole-number default is stored as an int, which used to
// fall through to a string — so `default = 5` did not equal 5.
func TestToolNumberDefaultArrivesAsANumber(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{{
		Name:        "limited",
		Description: "Uses a numeric default",
		Params:      []ParamDef{{Name: "limit", Type: "number", DefaultVal: int(5)}},
		Action:      parseExpr(t, `ctx.args.limit == 5 ? "number" : "not a number: ${jsonencode(ctx.args.limit)}"`),
	}}, nil)

	cs := connectInMemory(t, srv)

	for name, args := range map[string]map[string]any{
		"absent": {},
		"null":   {"limit": nil},
	} {
		res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "limited", Arguments: callArgs(args)})
		require.NoError(t, err, name)
		txt, ok := res.Content[0].(*sdkmcp.TextContent)
		require.True(t, ok, name)
		assert.Equal(t, "number", txt.Text, name)
	}
}

// Straight at the converters, naming the value shape rather than a scenario:
// this is the test that would have caught the panic.
func TestCtyConversionsRejectNullAndUnknown(t *testing.T) {
	for name, val := range map[string]cty.Value{
		"null":    cty.NullVal(cty.String),
		"unknown": cty.UnknownVal(cty.String),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ctyToCallToolResult(val)
			assert.ErrorContains(t, err, "tool action returned")

			_, err = ctyToResourceContents("test://x", "text/plain", val)
			assert.ErrorContains(t, err, "resource action returned")

			_, err = ctyToPromptMessages(val)
			assert.ErrorContains(t, err, "prompt action returned")
		})
	}
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
			Action:      parseExpr(t, `mcp::error("something went wrong")`),
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

// A default has to reach the action even when the client ignores the one
// published in the input schema, which is the only guarantee a config author
// can rely on.
func TestToolDefaultAppliedWhenArgumentOmitted(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{
			Name:        "search",
			Description: "Search",
			Params: []ParamDef{
				{Name: "query", Type: "string", Required: true},
				{Name: "limit", Type: "number", DefaultVal: float64(10)},
			},
			Action: parseExpr(t, `"${ctx.args.query}:${ctx.args.limit}"`),
		},
	}, nil)

	cs := connectInMemory(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: callArgs(map[string]any{"query": "widgets"}),
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "widgets:10", res.Content[0].(*sdkmcp.TextContent).Text)

	// An argument the client does send wins over the default.
	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: callArgs(map[string]any{"query": "widgets", "limit": float64(3)}),
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "widgets:3", res.Content[0].(*sdkmcp.TextContent).Text)
}

// enum and default are advertised to the model through the input schema; a
// param carrying them and a param carrying neither have to coexist.
func TestToolSchemaCarriesEnumAndDefault(t *testing.T) {
	srv := newTestServer(t, nil, []ToolDef{
		{
			Name:        "report",
			Description: "Report",
			Params: []ParamDef{
				{Name: "length", Type: "string", DefaultVal: "medium",
					Enum: []any{"short", "medium", "long"}},
				{Name: "plain", Type: "string"},
			},
			Action: parseExpr(t, `ctx.args.length`),
		},
	}, nil)

	cs := connectInMemory(t, srv)

	list, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	props := list.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)

	length := props["length"].(map[string]any)
	assert.Equal(t, "medium", length["default"])
	assert.Equal(t, []any{"short", "medium", "long"}, length["enum"])

	// A param with neither gets neither key, rather than a null one.
	plain := props["plain"].(map[string]any)
	assert.NotContains(t, plain, "default")
	assert.NotContains(t, plain, "enum")
}
