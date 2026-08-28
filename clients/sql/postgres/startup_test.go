package postgres

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlengine "github.com/tsarna/vinculum/clients/sql"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// deadPort returns an address nothing is listening on, so a dial is refused
// rather than hanging.
func deadPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

func clientOn(t *testing.T, port int) *sqlengine.SQLClient {
	t.Helper()
	vcl := fmt.Sprintf(`
client "postgres" "db" {
    dsn = "postgres://u:p@127.0.0.1:%d/app?sslmode=disable&connect_timeout=1"
}
`, port)
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	c, ok := config.Clients["postgres"]["db"].(*sqlengine.SQLClient)
	require.True(t, ok)
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

// A server that is down at boot is a recoverable state, not a dead client.
// Start used to close the pool and leave the handle nil, throwing away the
// reconnection database/sql gives for free — so the client stayed broken for
// the life of the process even after the database came back.
func TestStartKeepsThePoolWhenTheServerIsDown(t *testing.T) {
	c := clientOn(t, deadPort(t))

	err := c.Start()
	require.Error(t, err, "the failure is still reported to the boot loop")
	assert.Contains(t, err.Error(), `"db"`, "the error should name the client")
	assert.False(t, cfg.IsTerminal(err),
		"a refused connection is transient by nature; retrying is exactly what fixes it")

	// The pool survived, so Ready reports the live state rather than the
	// permanent "not started" a nil handle would give.
	readyErr := c.Ready(context.Background())
	require.Error(t, readyErr)
	assert.NotContains(t, readyErr.Error(), "not started",
		"a retained pool means the probe pings for real instead of answering from a nil handle")
}

// A malformed DSN is *not* terminal here, and the reason is the driver, not a
// judgement: pgx defers parsing to the first dial, so sql.Open accepts anything
// and the failure arrives as a connect error like any other. Pinned because the
// opposite is the intuitive guess, and because a pgx that started validating
// eagerly would change which branch this takes.
func TestAMalformedDSNIsReportedByTheDialNotByOpen(t *testing.T) {
	vcl := `
client "postgres" "db" {
    dsn = "::::not a dsn at all::::"
}
`
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	c := config.Clients["postgres"]["db"].(*sqlengine.SQLClient)
	t.Cleanup(func() { _ = c.Stop() })

	err := c.Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect")
	assert.False(t, cfg.IsTerminal(err))
}

// A query issued while the server is away must fail with the driver's own
// error, not with a nil-handle message: the pool is there, it just cannot
// connect yet, and that distinction is what the reason string on /readyz
// carries.
func TestQueryWhileDownReportsTheDialFailure(t *testing.T) {
	c := clientOn(t, deadPort(t))
	require.Error(t, c.Start())

	_, err := c.Get(context.Background(), []cty.Value{cty.StringVal("SELECT 1")})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not started")
}
