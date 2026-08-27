package repl

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runReady drives :ready and returns what it wrote to each stream.
func runReady(t *testing.T, h *host, args ...string) (out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	h.readyTo(&o, args, &e)
	return o.String(), e.String()
}

// readyTestHost builds a session with one passing check, one failing one, and a
// third on the liveness probe — enough to tell the two probes apart.
func readyTestHost(t *testing.T) *host {
	t.Helper()
	h := newTestHost(t, `
check "database" { input = true }
check "upstream" { input = { ready = false, reason = "no route to host" } }

check "wedged" {
    input = true
    probe = "live"
}
`)
	// Past the boot gate, which otherwise answers for every contributor.
	h.cfg.Health.SetBooted()
	return h
}

// :ready must appear in :help's listing, which the engine builds from Summary.
// Without one it is undiscoverable.
func TestReadyIsAMetaCommandWithASummary(t *testing.T) {
	h := readyTestHost(t)

	for _, m := range h.metaCommands() {
		if m.Names[0] == ":ready" {
			assert.NotEmpty(t, m.Summary, ":help lists commands by their Summary")
			assert.NotNil(t, m.Run)
			return
		}
	}
	t.Fatal(":ready is not registered")
}

func TestReadyReportsWhatIsFailingAndWhy(t *testing.T) {
	out, errOut := runReady(t, readyTestHost(t))
	require.Empty(t, errOut)

	assert.Contains(t, out, "[+]process ok")
	assert.Contains(t, out, "[+]check.database ok")
	assert.Contains(t, out, "[-]check.upstream failed: no route to host")
	assert.Contains(t, out, "readyz check failed")

	// The same body /readyz?verbose serves, rendered by the same code — so the
	// two cannot drift, and each entry carries how long it has held its state.
	assert.Contains(t, out, "(for ")
}

func TestReadyLiveShowsOnlyTheLivenessProbe(t *testing.T) {
	out, errOut := runReady(t, readyTestHost(t), "live")
	require.Empty(t, errOut)

	assert.Contains(t, out, "[+]check.wedged ok")
	assert.Contains(t, out, "livez check passed")

	// A dependency being down must not read as "this process is wedged" — the
	// whole reason the two probes are separate.
	assert.NotContains(t, out, "check.upstream")
	assert.NotContains(t, out, "check.database")
}

func TestReadyRejectsAnUnknownProbe(t *testing.T) {
	out, errOut := runReady(t, readyTestHost(t), "bogus")

	assert.Empty(t, out)
	assert.Contains(t, errOut, `unknown probe "bogus"`)
	// Naming the alternatives, since the vocabulary is closed and short.
	assert.Contains(t, errOut, "ready")
	assert.Contains(t, errOut, "live")
}

func TestReadyRejectsExtraArguments(t *testing.T) {
	out, errOut := runReady(t, readyTestHost(t), "ready", "live")

	assert.Empty(t, out)
	assert.Contains(t, errOut, "usage: :ready")
}

func TestReadyAcceptsAnExplicitReady(t *testing.T) {
	explicit, errOut := runReady(t, readyTestHost(t), "ready")
	require.Empty(t, errOut)
	assert.Contains(t, explicit, "readyz check failed")
}

// alwaysFailing is a contributor that is never serving.
type alwaysFailing struct{}

func (alwaysFailing) Ready(context.Context) error { return errors.New("broker went away") }

func TestReadyBypassesTheCache(t *testing.T) {
	h := readyTestHost(t)

	// Establish a cached report, then change what is behind it. Inside the TTL
	// an HTTP probe would still be served the stale answer; the command forces
	// an evaluation, because a person who just typed it means now.
	first, _ := runReady(t, h)
	require.Contains(t, first, "[+]check.database ok")
	require.NotContains(t, first, "client.broker")

	h.cfg.Health.RegisterReady("client", "mqtt", "broker", alwaysFailing{})

	second, _ := runReady(t, h)
	assert.Contains(t, second, "[-]client.broker failed: broker went away",
		"a contributor added after the first report must show without waiting out the TTL")
}
