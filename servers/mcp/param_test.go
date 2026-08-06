package mcp

import (
	"context"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// buildFromVCL parses a config and returns the MCP server it declares, so a
// test can check what actually survives the trip from HCL to the wire. The
// handler tests construct ParamDef values directly, which is what let `default`
// and `enum` be decoded and then dropped without any test noticing.
func buildFromVCL(t *testing.T, src string) *Server {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := config.NewConfig().WithSources([]byte(src)).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	srv, ok := cfg.Servers["mcp"]["params"].(*McpServer)
	require.True(t, ok, "expected an mcp server named params")
	return srv.server
}

func TestParamDefaultAndEnumSurviveConfigParsing(t *testing.T) {
	srv := buildFromVCL(t, `
server "mcp" "params" {
    tool "report" {
        description = "Produce a report"

        param "length" {
            type    = "string"
            default = "medium"
            enum    = ["short", "medium", "long"]
        }
        param "limit" {
            type    = "number"
            default = 10
        }

        action = "${ctx.args.length}/${ctx.args.limit}"
    }
}
`)

	cs := connectInMemory(t, srv)

	list, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, list.Tools, 1)
	props := list.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)

	length := props["length"].(map[string]any)
	assert.Equal(t, "medium", length["default"])
	assert.Equal(t, []any{"short", "medium", "long"}, length["enum"])

	assert.Equal(t, float64(10), props["limit"].(map[string]any)["default"])

	// And the defaults reach the action when the caller omits both.
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "report"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "medium/10", res.Content[0].(*sdkmcp.TextContent).Text)
}

// A default of the wrong type would publish an input schema that contradicts
// itself, so it is refused at config time rather than at call time.
func TestParamValueMustMatchDeclaredType(t *testing.T) {
	for name, src := range map[string]string{
		"default": "param \"n\" {\n type = \"number\"\n default = \"ten\"\n}",
		"enum":    "param \"s\" {\n type = \"string\"\n enum = [\"a\", 2]\n}",
	} {
		t.Run(name, func(t *testing.T) {
			logger, err := zap.NewDevelopment()
			require.NoError(t, err)

			_, diags := config.NewConfig().WithSources([]byte(`
server "mcp" "params" {
    tool "t" {
        description = "t"
        ` + src + `
        action = "x"
    }
}
`)).WithLogger(logger).Build()

			require.True(t, diags.HasErrors(), "expected a type mismatch to be reported")
			assert.Contains(t, diags.Error(), "does not match its type")
		})
	}
}
