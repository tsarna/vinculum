package metricsserver

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

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// A standalone metrics server owns a listener, so it drains. Until it did, the
// scrape endpoint stayed up while the rest of the runtime was torn down.
func TestStandaloneMetricsServerDrains(t *testing.T) {
	port := freePort(t)
	vcl := fmt.Sprintf(`
server "metrics" "m" {
    listen           = "127.0.0.1:%d"
    shutdown_timeout = "2s"
}
`, port)

	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	require.Len(t, c.Drainables, 1, "a standalone metrics server should register a Drainable")

	srv := c.Servers["metrics"]["m"].(*MetricsServer)
	assert.Equal(t, 2*time.Second, srv.shutdownTimeout, "shutdown_timeout should reach the server")

	require.NoError(t, srv.Start())
	t.Cleanup(func() { _ = srv.Drain(context.Background()) })

	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	require.Eventually(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond, "metrics endpoint should come up")

	require.NoError(t, srv.Drain(context.Background()))

	_, err := http.Get(url)
	assert.Error(t, err, "the metrics listener must be closed once Drain returns")
}

// A mounted metrics server has no listener of its own; the server "http" block
// hosting it drains, and this one must not claim a phase slot it cannot honour.
func TestMountedMetricsServerRegistersNoDrainable(t *testing.T) {
	vcl := `
server "metrics" "m" {}

server "http" "main" {
    listen = "127.0.0.1:0"

    handle "/metrics" {
        handler = server.m
    }
}
`
	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	require.Len(t, c.Drainables, 1, "only the http server owns a listener here")

	srv := c.Servers["metrics"]["m"].(*MetricsServer)
	require.NoError(t, srv.Drain(context.Background()), "draining a mounted server is a no-op")
}
