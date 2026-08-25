package httpserver_test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	"go.uber.org/zap"
)

// buildServerOn builds a one-server config listening on addr, without starting.
func buildServerOn(t *testing.T, addr string) (*httpserver.HttpServer, *cfg.Config) {
	t.Helper()
	vcl := fmt.Sprintf(`
server "http" "main" {
    listen = %q

    handle "GET /" { action = "ok" }
}
`, addr)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%v", diags)
	return config.Servers["http"]["main"].(*httpserver.HttpServer), config
}

func TestStartReportsABindFailure(t *testing.T) {
	// Occupy the port, so the server's own bind cannot succeed.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { taken.Close() })

	srv, config := buildServerOn(t, taken.Addr().String())

	// ListenAndServe inside the goroutine returned nil here and logged the
	// failure, leaving a process that was up and serving nothing.
	err = srv.Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "address already in use")
	assert.Contains(t, err.Error(), `"main"`, "the error should name the server")

	// And the failure is visible to a probe, not only to the log.
	config.Health.SetBooted()
	failing := config.Health.Failing(context.Background(), cfg.ProbeReady, true)
	require.Len(t, failing, 1)
	assert.Equal(t, "server.main", failing[0].Component)
	assert.Equal(t, "http", failing[0].Type)
	assert.Equal(t, "not listening", failing[0].Reason)
}

func TestStartIsReadyOnceBound(t *testing.T) {
	srv, config := buildServerOn(t, "127.0.0.1:0")
	require.NoError(t, srv.Start())
	t.Cleanup(func() { srv.Server.Close() })

	config.Health.SetBooted()
	assert.Empty(t, config.Health.Failing(context.Background(), cfg.ProbeReady, true))
	assert.NoError(t, srv.Ready(context.Background()))
}

func TestAServerThatDeclinesDoesNotGateReadiness(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { taken.Close() })

	vcl := fmt.Sprintf(`
server "http" "main" {
    listen    = %q
    readiness = false

    handle "GET /" { action = "ok" }
}
`, taken.Addr().String())
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%v", diags)

	srv := config.Servers["http"]["main"].(*httpserver.HttpServer)
	require.Error(t, srv.Start(), "the bind still fails and is still reported to the caller")

	// But it no longer takes the process out of rotation, which is the whole
	// point of the attribute.
	config.Health.SetBooted()
	assert.Empty(t, config.Health.Failing(context.Background(), cfg.ProbeReady, true))
}
