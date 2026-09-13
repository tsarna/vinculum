package mcp

import (
	"context"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/functions"
	"github.com/yosida95/uritemplate/v3"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// parseExpr parses a literal HCL expression for use in tests.
func parseExpr(t *testing.T, src string) hcl.Expression {
	t.Helper()
	expr, diags := hclsyntax.ParseExpression([]byte(src), "test", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors(), diags.Error())
	return expr
}

// emptyEvalCtx returns an HCL eval context for testing with the standard
// functions that production config normally provides to the MCP server.
func emptyEvalCtx() *hcl.EvalContext {
	funcs := functions.GetStandardLibraryFunctions()
	for name, fn := range functions.GetMcpFunctions() {
		funcs[name] = fn
	}
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{},
		Functions: funcs,
	}
}

// newTestServer creates an MCP server with the given resources for testing.
func newTestServer(t *testing.T, resources []ResourceDef, tools []ToolDef, prompts []PromptDef) *Server {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	srv, err := New(ServerConfig{
		Name:          "test",
		ServerName:    "Test Server",
		ParentEvalCtx: emptyEvalCtx(),
		Logger:        logger,
		Resources:     resources,
		Tools:         tools,
		Prompts:       prompts,
	})
	require.NoError(t, err)
	return srv
}

// connectInMemory creates a connected server/client pair using in-memory transports.
// Returns the ClientSession; caller is responsible for Close().
func connectInMemory(t *testing.T, srv *Server) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := sdkmcp.NewInMemoryTransports()

	ss, err := srv.SDKServer().Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { ss.Close() })

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })

	return cs
}

func TestResourceStaticText(t *testing.T) {
	srv := newTestServer(t, []ResourceDef{
		{
			URI:    "status://current",
			Name:   "Status",
			Action: parseExpr(t, `"OK"`),
		},
	}, nil, nil)

	cs := connectInMemory(t, srv)

	ctx := context.Background()
	res, err := cs.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: "status://current"})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "OK", res.Contents[0].Text)
	assert.Equal(t, "status://current", res.Contents[0].URI)
}

// `{+path}` is RFC 6570 reserved expansion: the capture may contain slashes,
// which is what lets one template address a whole tree of topics. A plain
// {path} would not match "client/mqtt" at all, so a URL space built on it rests
// on this and nothing else checks it.
func TestResourceTemplateCapturesASlashSeparatedPath(t *testing.T) {
	tmpl, err := ParseResourceTemplate("vcl://topic/{+path}")
	require.NoError(t, err)
	require.NotNil(t, tmpl, "a URI with a placeholder is a template")

	srv := newTestServer(t, []ResourceDef{
		{
			URI:      "vcl://topic/{+path}",
			Name:     "Topic",
			Template: tmpl,
			Action:   parseExpr(t, `ctx.args.path`),
		},
	}, nil, nil)

	cs := connectInMemory(t, srv)

	for uri, want := range map[string]string{
		"vcl://topic/subscription":       "subscription",
		"vcl://topic/client/mqtt":        "client/mqtt",
		"vcl://topic/server/http/handle": "server/http/handle",
	} {
		res, err := cs.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: uri})
		require.NoError(t, err, uri)
		require.Len(t, res.Contents, 1, uri)
		assert.Equal(t, want, res.Contents[0].Text, uri)
	}
}

// As for a tool: a typed null would take the string branch and panic. The null
// is typed for the reason TestToolNullResultIsReportedNotPanicked gives.
func TestResourceNullResultIsReportedNotPanicked(t *testing.T) {
	srv := newTestServer(t, []ResourceDef{
		{URI: "nothing://x", Name: "Nothing", Action: parseExpr(t, `true ? null : ""`)},
	}, nil, nil)

	cs := connectInMemory(t, srv)

	_, err := cs.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: "nothing://x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned null")
}

func TestResourceListsResources(t *testing.T) {
	srv := newTestServer(t, []ResourceDef{
		{URI: "a://x", Name: "A", Action: parseExpr(t, `"a"`)},
		{URI: "b://y", Name: "B", Action: parseExpr(t, `"b"`)},
	}, nil, nil)

	cs := connectInMemory(t, srv)

	ctx := context.Background()
	list, err := cs.ListResources(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, list.Resources, 2)
}

func TestResourceTemplateURIVars(t *testing.T) {
	tmpl, err := uritemplate.New("info://{section}")
	require.NoError(t, err)

	srv := newTestServer(t, []ResourceDef{
		{
			URI:      "info://{section}",
			Name:     "Info",
			Action:   parseExpr(t, `"section: ${ctx.args.section}"`),
			Template: tmpl,
		},
	}, nil, nil)

	cs := connectInMemory(t, srv)

	ctx := context.Background()
	res, err := cs.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: "info://foobar"})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "section: foobar", res.Contents[0].Text)
}

func TestResourceCtxAttributes(t *testing.T) {
	srv := newTestServer(t, []ResourceDef{
		{
			URI:    "meta://info",
			Name:   "Meta",
			Action: parseExpr(t, `"server=${ctx.server_name} uri=${ctx.uri}"`),
		},
	}, nil, nil)

	cs := connectInMemory(t, srv)

	ctx := context.Background()
	res, err := cs.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: "meta://info"})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "server=test uri=meta://info", res.Contents[0].Text)
}

// There is no implicit encoding: an object has to be passed through
// jsonencode() rather than returned as-is. Documented, so pinned.
func TestResourceRejectsUnencodedStructuredValue(t *testing.T) {
	srv := newTestServer(t, []ResourceDef{
		{URI: "cfg://raw", Name: "Raw", Action: parseExpr(t, `{a = 1}`)},
		{URI: "cfg://json", Name: "JSON", Action: parseExpr(t, `jsonencode({a = 1})`)},
	}, nil, nil)

	cs := connectInMemory(t, srv)
	ctx := context.Background()

	_, err := cs.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: "cfg://raw"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")

	res, err := cs.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: "cfg://json"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, res.Contents[0].Text)
}
