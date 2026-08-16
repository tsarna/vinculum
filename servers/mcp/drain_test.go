package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

func freeDrainPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// A standalone MCP server owns a listener, so it drains. The http.Server used
// to be a local in Start(), which made closing it impossible.
func TestStandaloneMcpServerDrains(t *testing.T) {
	port := freeDrainPort(t)
	vcl := fmt.Sprintf(`
server "mcp" "m" {
    listen           = "127.0.0.1:%d"
    shutdown_timeout = "2s"

    tool "echo" {
        description = "echo"
        action      = "ok"
    }
}
`, port)

	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	require.Len(t, c.Drainables, 1, "a standalone MCP server should register a Drainable")

	srv := c.Servers["mcp"]["m"].(*McpServer)
	assert.Equal(t, 2*time.Second, srv.server.shutdownTimeout, "shutdown_timeout should reach the server")

	require.NoError(t, srv.Start())
	t.Cleanup(func() { _ = srv.Drain(context.Background()) })

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 10*time.Millisecond, "MCP server should come up")

	require.NoError(t, srv.Drain(context.Background()))

	_, err := http.Get("http://" + addr + "/")
	assert.Error(t, err, "the MCP listener must be closed once Drain returns")
}

// A mounted MCP server has no listener of its own; the server "http" block
// hosting it drains, and this one must not claim a phase slot it cannot honour.
func TestMountedMcpServerRegistersNoDrainable(t *testing.T) {
	vcl := `
server "mcp" "m" {
    tool "echo" {
        description = "echo"
        action      = "ok"
    }
}

server "http" "main" {
    listen = "127.0.0.1:0"

    handle "/mcp" {
        handler = server.m
    }
}
`
	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	require.Len(t, c.Drainables, 1, "only the http server owns a listener here")

	srv := c.Servers["mcp"]["m"].(*McpServer)
	require.NoError(t, srv.Drain(context.Background()), "draining a mounted server is a no-op")
}
