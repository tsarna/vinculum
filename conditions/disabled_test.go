package conditions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
)

// TestConditionDisabled covers `disabled` on a condition block. Every other
// block that creates a runtime component accepts it; a condition used to
// reject it outright, because its handler passed the whole body straight to
// the subtype without an envelope of its own.
func TestConditionDisabled(t *testing.T) {
	// Every subtype gets the attribute, since it comes from the shared
	// envelope rather than any one subtype's decode struct.
	for _, src := range []string{
		`condition "timer" "t" { disabled = true }`,
		`condition "threshold" "t" { disabled = true }`,
		`condition "counter" "t" { disabled = true }`,
		`condition "flipflop" "t" { disabled = true }`,
	} {
		c := buildConfig(t, []byte(src))
		assert.NotContains(t, c.CtyConditionMap, "t", "%s", src)
	}

	// A disabled block's own body is not validated against its subtype, so a
	// half-finished condition can be parked without deleting it — the same as
	// a disabled client or trigger.
	c := buildConfig(t, []byte(`
condition "threshold" "parked" {
    disabled = true
    input    = 1
}
`))
	assert.NotContains(t, c.CtyConditionMap, "parked",
		"a disabled threshold with no on_above/off_below pair should still build")

	// Nothing is registered, so a reference to a disabled condition fails
	// rather than silently reading false — matching a disabled fsm.
	_, diags := cfg.NewConfig().
		WithSources([]byte(`
condition "timer" "gone" { disabled = true }
var "v" { value = get(condition.gone) }
`)).
		WithLogger(testLogger(t)).
		Build()
	require.True(t, diags.HasErrors(), "referencing a disabled condition should fail")
}

// TestConditionEnabledStillBuilds is the other half: the envelope must not
// swallow the subtype's own body.
func TestConditionEnabledStillBuilds(t *testing.T) {
	c := buildConfig(t, []byte(`
condition "timer" "live" {
    disabled       = false
    activate_after = "1s"
}
`))
	assert.Contains(t, c.CtyConditionMap, "live")
}
