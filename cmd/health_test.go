package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
