package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// TestNoFunctionNameCollisions is the anti-drift guard for the function
// registry. The cmd package blank-imports every subsystem (cmd/plugins.go), so
// this is the only place the whole shipped set of function plugins is linked
// together — package config's own tests see none of them.
//
// Two plugins contributing one name is reported wherever a config is built, so
// without this test the failure still surfaces; it surfaces as every test that
// happens to build a config failing at once, naming nothing in particular. That
// is how `basename`, `dirname`, `abspath` and `pathexpand` — registered by both
// `stdlib` and `filesystem` — presented when the check was first switched on.
//
// The check inside Build() probes every feature, so a name that only collides
// behind --file-path or --allow-kill is caught here too, without this test
// having a list of feature flags to keep in step.
func TestNoFunctionNameCollisions(t *testing.T) {
	cfg, diags := config.NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		Build()
	if cfg != nil {
		for _, b := range cfg.Buses {
			b.Stop() //nolint:errcheck
		}
	}

	assert.False(t, diags.HasErrors(),
		"an empty config must build cleanly; a collision reads "+
			"\"Function name collides between plugins\" and names both plugins: %v", diags)
}
