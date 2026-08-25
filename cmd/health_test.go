package cmd

import (
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// The health blocks compose with `condition` and `trigger "watch"`, which
// register from packages that import config — so config's own tests cannot see
// them. These run through `vinculum check`, where every registration in the
// binary is present, which is the only place the composition is real.

func TestCheckComposesWithAConditionAndAWatchTrigger(t *testing.T) {
	stdout, _, err := runCheckCmd(t, map[string]string{
		"health.vcl": `
var "backlog" { value = 5000 }
var "stalled" { value = false }

# The condition holds the temporal behavior: a momentary spike is not an
# outage. The check says what the boolean means for serving traffic.
condition "timer" "backlog_ok" {
    input            = get(var.backlog) < 1000
    deactivate_after = "30s"
}

check "backlog" {
    input  = get(condition.backlog_ok)
    reason = "message backlog above threshold for 30s"
}

check "pipeline_progress" {
    probe  = "live"
    input  = !get(var.stalled, false)
    reason = "no pipeline output for 10 minutes"
}

# A per-check reaction, with no per-check hook attribute needed.
trigger "watch" "backlog_changed" {
    watch  = check.backlog
    action = log::warn("backlog check changed", {passing = ctx.new_value})
}

# ... and one over the aggregate.
trigger "watch" "readiness_changed" {
    watch  = sys.ready
    action = log::warn("readiness changed", {ready = ctx.new_value})
}

trigger "interval" "health_poll" {
    delay  = "10s"
    action = health::refresh(ctx)
}
`,
	})

	require.NoError(t, err)
	assert.Contains(t, stdout, "Configuration is valid")
}

func TestCheckTypoIsReportedByCheck(t *testing.T) {
	// A name that does not exist under the `check` root is reported against the
	// line that used it, exactly as `bus.mian` is — which is what registering
	// the root in blockNamespaceSchemas buys. Without it the reference would be
	// an unresolved root rather than a misspelled member.
	_, stderr, err := runCheckCmd(t, map[string]string{
		"health.vcl": `
check "database" { input = true }

trigger "watch" "t" {
    watch  = check.databse
    action = log::warn("changed")
}
`,
	})

	require.Error(t, err)
	assert.Contains(t, stderr, `does not have an attribute named "databse"`)
	assert.Contains(t, stderr, `watch  = check.databse`, "the offending line should be quoted")
}

func TestStandaloneHealthListener(t *testing.T) {
	// The listener needs no VCL at all: this config declares no server, which
	// is the deployment case it exists for.
	config, diags := config.NewConfig().
		WithSources([]byte(`check "broker" { input = false }`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%v", diags)

	healthListen, healthVerbose = "127.0.0.1:0", false
	t.Cleanup(func() { healthListen, healthVerbose = "", false })

	srv, err := startHealthListener(config, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, srv)
	t.Cleanup(func() { srv.Close() })

	addr := srv.Addr()

	// Boot has not completed, so every probe reports "starting" — which is
	// what makes a startupProbe pointed at /readyz work.
	code, body := httpGet(t, "http://"+addr+"/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "not ready\n", body)

	config.Health.SetBooted()

	code, _ = httpGet(t, "http://"+addr+"/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code, "the failing check keeps it unready")

	// A readiness failure must not restart the pod.
	code, body = httpGet(t, "http://"+addr+"/livez")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok\n", body)
	code, _ = httpGet(t, "http://"+addr+"/healthz")
	assert.Equal(t, http.StatusOK, code)

	// Off by default, so no port is opened that was not asked for.
	_, verbose := httpGet(t, "http://"+addr+"/readyz?verbose")
	assert.Equal(t, "not ready\n", verbose)
}

func TestStandaloneHealthListenerIsAbsentUnlessAsked(t *testing.T) {
	config, diags := config.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%v", diags)

	// A binary run from a terminal or a systemd unit opens no port it was not
	// asked to open; the image's default is an environment variable, not a
	// behavior baked into the runtime.
	healthListen = ""
	srv, err := startHealthListener(config, zap.NewNop())
	require.NoError(t, err)
	assert.Nil(t, srv)
}

func TestStandaloneHealthListenerReportsABindFailure(t *testing.T) {
	config, diags := config.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%v", diags)

	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { taken.Close() })

	healthListen = taken.Addr().String()
	t.Cleanup(func() { healthListen = "" })

	_, err = startHealthListener(config, zap.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--health-listen")
	assert.Contains(t, err.Error(), "VINCULUM_HEALTH_LISTEN=",
		"the error should say how to turn the image default off")
}

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}
