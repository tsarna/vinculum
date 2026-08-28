package metricsserver

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// A metrics server that cannot bind is the case most likely to go unnoticed:
// nothing scrapes it on the way up, and it contributes to no probe of its own.
// Terminal is what makes it impossible to miss.
func TestStartReportsABindFailureAsTerminal(t *testing.T) {
	// Occupy the port, so the server's own bind cannot succeed.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { taken.Close() })

	vcl := fmt.Sprintf(`
server "metrics" "m" {
    listen = %q
}
`, taken.Addr().String())

	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	err = c.Servers["metrics"]["m"].(*MetricsServer).Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "address already in use")
	assert.Contains(t, err.Error(), `"m"`, "the error should name the server")
	assert.True(t, cfg.IsTerminal(err))
}

// A mounted server owns no listener, so there is nothing to fail at.
func TestMountedMetricsServerStartsWithoutBinding(t *testing.T) {
	vcl := `
server "metrics" "m" {}
`
	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	assert.NoError(t, c.Servers["metrics"]["m"].(*MetricsServer).Start())
}
