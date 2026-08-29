package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	metricsserver "github.com/tsarna/vinculum/servers/metrics"
	"go.uber.org/zap"
)

// The measurement BACKEND-AUTOWIRE-ORDER was written from, as a test. A bus
// that omits `metrics` auto-wires the default backend, and used to find one
// only when the backend block happened to be written above it: the same two
// blocks in the other order produced an uninstrumented bus, with nothing to
// tell the author that where they wrote them was the difference.
//
// Asserted on a scrape rather than on the plumbing — the bus builds its
// instruments from whatever MeterProvider it was handed, so a metric family
// reaching the registry is the whole chain: sort, resolve, wire, record.

func busSentMetric(t *testing.T, src string) bool {
	t.Helper()

	cfg, diags := config.NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %s", diags.Error())
	t.Cleanup(func() {
		for _, b := range cfg.Buses {
			b.Stop() //nolint:errcheck
		}
	})

	server, ok := cfg.Servers["metrics"]["prom"].(*metricsserver.MetricsServer)
	require.True(t, ok, "the fixture must declare server \"metrics\" \"prom\"")

	// A counter reaches the exporter once it has recorded something.
	require.NoError(t, cfg.Buses["main"].PublishSync(context.Background(), "t", "payload"))

	families, err := server.GetRegistry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "messaging_client_sent_messages") {
			return true
		}
	}
	return false
}

func TestBusDeclaredBeforeTheMetricsBackendIsStillInstrumented(t *testing.T) {
	assert.True(t, busSentMetric(t, `
bus "main" {}
server "metrics" "prom" {}
`), "a bus declared above the backend reported nothing")
}

func TestBusDeclaredAfterTheMetricsBackendIsInstrumented(t *testing.T) {
	assert.True(t, busSentMetric(t, `
server "metrics" "prom" {}
bus "main" {}
`))
}

func TestBusNamingTheMetricsBackendIsInstrumented(t *testing.T) {
	assert.True(t, busSentMetric(t, `
bus "main" { metrics = server.prom }
server "metrics" "prom" {}
`), "an explicit reference orders the blocks and always did")
}

// The blanket rule gives every server and client block a dependency on every
// backend, which for a backend would include itself. These are the shapes that
// would then sort into "Circular dependency between blocks" rather than a
// configuration — both backends present, in either order, with an ordinary
// server and client alongside taking the blanket rule for real.
func TestBackendBlocksDoNotFormACycle(t *testing.T) {
	for _, src := range []string{`
server "metrics" "prom" {}
client "otlp" "t" {
    endpoint     = "http://127.0.0.1:4318"
    service_name = "test"
}
server "http" "web" { listen = "127.0.0.1:0" }
client "http" "api" {}
`, `
client "otlp" "t" {
    endpoint     = "http://127.0.0.1:4318"
    service_name = "test"
}
server "metrics" "prom" {}
`, `
server "metrics" "prom" { tracing = client.t }
client "otlp" "t" {
    endpoint     = "http://127.0.0.1:4318"
    service_name = "test"
}
`} {
		assert.Empty(t, buildRefCheck(t, src), "configuration should build: %s", src)
	}
}
