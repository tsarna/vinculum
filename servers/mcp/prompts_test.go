package mcp

import (
	"context"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptUserMessage(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{
			Name:        "greet",
			Description: "A greeting prompt",
			Params: []ParamDef{
				{Name: "name", Type: "string", Required: true},
			},
			Action: parseExpr(t, `mcp_usermessage("Hello, ${ctx.args.name}!")`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{
		Name:      "greet",
		Arguments: map[string]string{"name": "Alice"},
	})
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, sdkmcp.Role("user"), res.Messages[0].Role)
	txt, ok := res.Messages[0].Content.(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Hello, Alice!", txt.Text)
}

func TestPromptAssistantMessage(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{
			Name:   "assistant_example",
			Action: parseExpr(t, `mcp_assistantmessage("I can help with that.")`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{Name: "assistant_example"})
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, sdkmcp.Role("assistant"), res.Messages[0].Role)
}

func TestPromptListsPrompts(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{Name: "p1", Action: parseExpr(t, `mcp_usermessage("one")`)},
		{Name: "p2", Action: parseExpr(t, `mcp_usermessage("two")`)},
	})

	cs := connectInMemory(t, srv)

	list, err := cs.ListPrompts(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, list.Prompts, 2)
}

func TestPromptContextAttributes(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{
			Name:   "meta",
			Action: parseExpr(t, `mcp_usermessage("server=${ctx.server_name} prompt=${ctx.prompt_name}")`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{Name: "meta"})
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	txt, ok := res.Messages[0].Content.(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "server=test prompt=meta", txt.Text)
}

func TestPromptDescriptionPropagated(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{
			Name:        "described",
			Description: "A well-described prompt",
			Action:      parseExpr(t, `mcp_usermessage("hi")`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{Name: "described"})
	require.NoError(t, err)
	assert.Equal(t, "A well-described prompt", res.Description)
}
