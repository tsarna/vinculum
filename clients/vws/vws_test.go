package vws

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// TestReconnectIsABlock guards the shape of `reconnect`. It is documented as a
// block in doc/server-vws.md, and was tagged as an attribute, so the
// documented configuration did not parse at all.
func TestReconnectIsABlock(t *testing.T) {
	src := `
client "vws" "upstream" {
    url = "wss://example.com/ws"

    reconnect {
        initial_delay  = "1s"
        max_delay      = "30s"
        backoff_factor = 2.0
    }
}
`
	_, diags := cfg.NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "config should be valid: %s", diags.Error())
}

// TestReconnectRejectsAttributeForm is the other half: the attribute form the
// old tag implied was never usable, and should stay rejected.
func TestReconnectRejectsAttributeForm(t *testing.T) {
	src := `
client "vws" "upstream" {
    url       = "wss://example.com/ws"
    reconnect = "1s"
}
`
	_, diags := cfg.NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	assert.True(t, diags.HasErrors(), "reconnect is a block, not an attribute")
}
