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
			Action: parseExpr(t, `mcp::user_message("Hello, ${ctx.args.name}!")`),
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
			Action: parseExpr(t, `mcp::assistant_message("I can help with that.")`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{Name: "assistant_example"})
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, sdkmcp.Role("assistant"), res.Messages[0].Role)
}

// A bare string is the prompt whose only message is the obvious one. Requiring
// mcp::user_message() around it would add a call that says what the shape
// already says — and the block's own documentation has always promised this.
func TestPromptStringIsAUserMessage(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{
			Name:   "plain",
			Params: []ParamDef{{Name: "topic", Type: "string", Required: true}},
			Action: parseExpr(t, `"Tell me about ${ctx.args.topic}."`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{
		Name:      "plain",
		Arguments: map[string]string{"topic": "subscriptions"},
	})
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, sdkmcp.Role("user"), res.Messages[0].Role)
	txt, ok := res.Messages[0].Content.(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Tell me about subscriptions.", txt.Text)
}

// A typed null reports what it is. The capsule-typed ones are the cases that
// crashed: GetMCPResult called EncapsulatedValue on a null capsule, at the top
// level and, separately, on an element of a list.
func TestPromptNullResultIsReportedNotPanicked(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"string":       {`true ? null : ""`, "returned null"},
		"capsule":      {`true ? null : mcp::user_message("x")`, "returned null"},
		"list element": {`[mcp::user_message("a"), true ? null : mcp::user_message("b")]`, "element 1 is null"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t, nil, nil, []PromptDef{
				{Name: "nothing", Action: parseExpr(t, tc.src)},
			})

			cs := connectInMemory(t, srv)

			_, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{Name: "nothing"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)

			_, err = cs.ListPrompts(context.Background(), nil)
			assert.NoError(t, err, "the server is still there to answer")
		})
	}
}

func TestPromptListsPrompts(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{Name: "p1", Action: parseExpr(t, `mcp::user_message("one")`)},
		{Name: "p2", Action: parseExpr(t, `mcp::user_message("two")`)},
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
			Action: parseExpr(t, `mcp::user_message("server=${ctx.server_name} prompt=${ctx.prompt_name}")`),
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
			Action:      parseExpr(t, `mcp::user_message("hi")`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{Name: "described"})
	require.NoError(t, err)
	assert.Equal(t, "A well-described prompt", res.Description)
}

// Prompt arguments are strings on the wire, so a default is stringified to
// match — an action must not have to handle one argument arriving as a number
// only because the caller omitted it.
func TestPromptDefaultAppliedAsString(t *testing.T) {
	srv := newTestServer(t, nil, nil, []PromptDef{
		{
			Name: "summarize",
			Params: []ParamDef{
				{Name: "text", Type: "string", Required: true},
				{Name: "sentences", Type: "number", DefaultVal: float64(3)},
			},
			Action: parseExpr(t, `mcp::user_message("${ctx.args.sentences}: ${ctx.args.text}")`),
		},
	})

	cs := connectInMemory(t, srv)

	res, err := cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{
		Name:      "summarize",
		Arguments: map[string]string{"text": "hello"},
	})
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, "3: hello", res.Messages[0].Content.(*sdkmcp.TextContent).Text)

	// A supplied argument still wins.
	res, err = cs.GetPrompt(context.Background(), &sdkmcp.GetPromptParams{
		Name:      "summarize",
		Arguments: map[string]string{"text": "hello", "sentences": "7"},
	})
	require.NoError(t, err)
	assert.Equal(t, "7: hello", res.Messages[0].Content.(*sdkmcp.TextContent).Text)
}
