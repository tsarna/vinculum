package config

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// buildChecks builds a config from VCL source and puts it past the boot gate,
// which is where a probe would find it.
func buildChecks(t *testing.T, src string) *Config {
	t.Helper()
	logger := zap.NewNop()
	config, diags := NewConfig().WithSources([]byte(src)).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), "%v", diags)
	config.Health.SetBooted()
	return config
}

func buildChecksExpectingError(t *testing.T, src string) hcl.Diagnostics {
	t.Helper()
	_, diags := NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors(), "expected the config to be rejected")
	return diags
}

// unreadyReasons maps each failing component to its reason.
func unreadyReasons(config *Config, probe string) map[string]string {
	out := map[string]string{}
	for _, s := range config.Health.Failing(context.Background(), probe, true) {
		out[s.Component] = s.Reason
	}
	return out
}

func TestCheckInputInterpretation(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		reason string
		// want is the expected reason, or "" when the check should pass.
		want string
	}{
		{name: "true passes", input: "true"},
		{
			name:  "false fails with the default reason",
			input: "false",
			want:  "check failed",
		},
		{
			name:   "false fails with the declared reason",
			input:  "false",
			reason: "database is unreachable",
			want:   "database is unreachable",
		},
		{
			// A returned string is always a complaint: a healthy check has
			// nothing to say.
			name:  "a string is the reason",
			input: `"replication lag is 40s"`,
			want:  "replication lag is 40s",
		},
		{
			name:   "an empty string falls back to the declared reason",
			input:  `""`,
			reason: "something is wrong",
			want:   "something is wrong",
		},
		{
			name:  "null fails rather than passing by accident",
			input: "null",
			want:  "check returned null",
		},
		{
			name:  "an object may state both",
			input: `{ready = false, reason = "queue depth 12000"}`,
			want:  "queue depth 12000",
		},
		{
			name:  "an object may state readiness alone",
			input: `{ready = true}`,
		},
		{
			name:   "an object without a reason falls back",
			input:  `{ready = false}`,
			reason: "declared reason",
			want:   "declared reason",
		},
		{
			name:  "an unusable value is reported rather than coerced",
			input: "42",
			want:  "check produced number, which is not a pass or a reason",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "check \"c\" {\n  input = " + tc.input + "\n"
			if tc.reason != "" {
				src += "  reason = \"" + tc.reason + "\"\n"
			}
			src += "}\n"

			config := buildChecks(t, src)
			reasons := unreadyReasons(config, ProbeReady)

			if tc.want == "" {
				assert.Empty(t, reasons, "check should have passed")
				return
			}
			require.Contains(t, reasons, "check.c")
			assert.Equal(t, tc.want, reasons["check.c"])
		})
	}
}

func TestCheckEvaluationErrorIsTheAnswer(t *testing.T) {
	// The expression is the user's, so its failure is the check's answer: a
	// check whose input cannot be evaluated is exactly a check that is failing.
	config := buildChecks(t, `
check "c" {
    input = length("not a collection")
}
`)

	reasons := unreadyReasons(config, ProbeReady)
	require.Contains(t, reasons, "check.c")
	assert.NotEmpty(t, reasons["check.c"])
}

func TestCheckIsEvaluatedOnEachProbeNotAtBuildTime(t *testing.T) {
	config := buildChecks(t, `
var "healthy" { value = true }

check "c" {
    input = get(var.healthy)
}
`)

	assert.Empty(t, unreadyReasons(config, ProbeReady))

	v, err := types.GetVariableFromCapsule(config.CtyVarMap["healthy"])
	require.NoError(t, err)
	_, err = v.Set(context.Background(), []cty.Value{cty.False})
	require.NoError(t, err)

	// The check reads the variable now, not the value it held at build time.
	assert.Contains(t, unreadyReasons(config, ProbeReady), "check.c")
}

func TestCheckSeesTheProbersContext(t *testing.T) {
	// The ctx is derived from whoever asked rather than fabricated, so the
	// universal fields every eval context carries are in scope.
	config := buildChecks(t, `
check "c" {
    input = ctx.baggage != null && ctx.trace_id != null
}
`)

	assert.Empty(t, unreadyReasons(config, ProbeReady))
}

func TestCheckProbeSelectsWhichProbeItGates(t *testing.T) {
	config := buildChecks(t, `
check "traffic" {
    input = false
}

check "wedged" {
    probe  = "live"
    input  = false
    reason = "no pipeline output for 10 minutes"
}
`)

	ready := unreadyReasons(config, ProbeReady)
	live := unreadyReasons(config, ProbeLive)

	// A check belongs to exactly one probe: a readiness failure must never
	// restart the process, and a liveness failure is not merely "send traffic
	// elsewhere".
	assert.Contains(t, ready, "check.traffic")
	assert.NotContains(t, ready, "check.wedged")
	assert.Contains(t, live, "check.wedged")
	assert.NotContains(t, live, "check.traffic")
	assert.Equal(t, "no pipeline output for 10 minutes", live["check.wedged"])
}

func TestCheckTimeoutIsReported(t *testing.T) {
	// sleep() is not a VCL function, so the timeout is exercised through the
	// registered contributor directly — the block's job is to pass the parsed
	// duration through, and that is what this asserts.
	config := buildChecks(t, `
check "c" {
    input   = true
    timeout = "250ms"
}
`)

	require.Len(t, config.Health.contributors, 1)
	assert.Equal(t, 250*time.Millisecond, config.Health.contributors[0].timeout)
	assert.Equal(t, ProbeReady, config.Health.contributors[0].probe)
	assert.Equal(t, "check.c", config.Health.contributors[0].component())
}

func TestCheckDisabledCreatesNothing(t *testing.T) {
	config := buildChecks(t, `
check "c" {
    input    = false
    disabled = true
}
`)

	assert.Empty(t, config.Health.RegisteredComponents())
	assert.NotContains(t, config.CtyCheckMap, "c")
	assert.Empty(t, unreadyReasons(config, ProbeReady))
}

func TestCheckDuplicateNameIsReported(t *testing.T) {
	diags := buildChecksExpectingError(t, `
check "c" { input = true }
check "c" { input = false }
`)
	assert.Contains(t, diags.Error(), "Duplicate check")
}

func TestCheckInvalidProbeIsReported(t *testing.T) {
	diags := buildChecksExpectingError(t, `
check "c" {
    input = true
    probe = "startup"
}
`)
	assert.Contains(t, diags.Error(), "Invalid probe")
	assert.Contains(t, diags.Error(), `got "startup"`)
}

func TestCheckRequiresInput(t *testing.T) {
	diags := buildChecksExpectingError(t, `check "c" { reason = "nothing to test" }`)
	assert.Contains(t, strings.ToLower(diags.Error()), "input")
}

func TestCheckNameIsGettableAndWatchable(t *testing.T) {
	config := buildChecks(t, `
var "healthy" { value = true }

check "database" {
    input  = get(var.healthy)
    reason = "database is unreachable"
}
`)

	val, ok := config.CtyCheckMap["database"]
	require.True(t, ok, "a check must publish check.<name>")

	check, err := GetCheckFromCapsule(val)
	require.NoError(t, err)

	// Nothing has probed yet, and unknown is treated as ready — the same rule
	// the aggregator applies to a component that reports nothing.
	got, err := check.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.True))

	w := &countingWatcher{}
	watchable, err := richcty.WatchableFromCtyValue(val)
	require.NoError(t, err)
	watchable.Watch(w)

	// The first probe establishes the baseline rather than synthesizing an edge.
	config.Health.Failing(context.Background(), ProbeReady, true)
	assert.Empty(t, w.values())

	v, err := types.GetVariableFromCapsule(config.CtyVarMap["healthy"])
	require.NoError(t, err)
	_, err = v.Set(context.Background(), []cty.Value{cty.False})
	require.NoError(t, err)

	config.Health.Failing(context.Background(), ProbeReady, true)
	require.Len(t, w.values(), 1)
	assert.True(t, w.values()[0].RawEquals(cty.False))

	got, err = check.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.False))

	// Still failing: a watcher woken with the value it already had is noise.
	config.Health.Failing(context.Background(), ProbeReady, true)
	assert.Len(t, w.values(), 1)
}

// Composition with `condition` and `trigger "watch"` cannot be exercised here:
// those types register from packages that import config, so neither is in this
// test binary's registry. They are covered end to end in cmd/health_test.go,
// where the whole binary's registrations are present.
