package config_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tsarna/functy"
	// Registers the `sys` ambient provider (sys.testing) used by the fixture.
	_ "github.com/tsarna/vinculum/ambient"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// buildTestrunConfig builds the testdata/testrun config (a bus, a var, a router
// and a testing-gated capture subscription, plus .cty test blocks). testing
// selects `vinculum test` mode (sys.testing == true).
func buildTestrunConfig(t *testing.T, testing bool) *config.Config {
	t.Helper()
	cb := config.NewConfig().WithSources("testdata/testrun").WithLogger(zap.NewNop())
	if testing {
		cb = cb.WithTesting(true)
	}
	cfg, diags := cb.Build()
	require.False(t, diags.HasErrors(), diags.Error())
	return cfg
}

func outcomesByName(outcomes []functy.TestOutcome) map[string]functy.TestOutcome {
	m := make(map[string]functy.TestOutcome, len(outcomes))
	for _, o := range outcomes {
		m[o.Name] = o
	}
	return m
}

// TestRunTestsAgainstLiveRuntime boots the runtime and runs the .cty test blocks,
// covering sys.testing, eventually (async send→subscribe→var), never, and skip.
func TestRunTestsAgainstLiveRuntime(t *testing.T) {
	cfg := buildTestrunConfig(t, true)

	require.Equal(t, 4, cfg.FunctyTestCount())

	for _, s := range cfg.Startables {
		require.NoError(t, s.Start())
	}
	for _, ps := range cfg.PostStartables {
		require.NoError(t, ps.PostStart())
	}
	t.Cleanup(func() {
		for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
			_ = cfg.Stoppables[i].Stop()
		}
	})

	outcomes, err := cfg.RunTests(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 4)

	byName := outcomesByName(outcomes)
	require.True(t, byName["runs in testing mode"].Passed(), "sys.testing should be true under test")
	require.True(t, byName["router rewrites in/ to out/"].Passed(),
		"eventually should observe the routed message; err=%v", byName["router rewrites in/ to out/"].Err)
	require.True(t, byName["unmatched topics are not routed"].Passed(),
		"never should hold for an unrouted topic; err=%v", byName["unmatched topics are not routed"].Err)
	require.True(t, byName["work in progress"].Skipped, "skip() should skip the test")
}

// TestRunTestsFilter checks that a name filter selects a subset (and reports the
// rest as deselected via the count difference).
func TestRunTestsFilter(t *testing.T) {
	cfg := buildTestrunConfig(t, true)

	outcomes, err := cfg.RunTests(context.Background(), func(name string) bool {
		return name == "runs in testing mode"
	})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, "runs in testing mode", outcomes[0].Name)
	require.Equal(t, 3, cfg.FunctyTestCount()-len(outcomes), "other tests should be deselected")
}

// TestRunTestsWithoutTestingFlag verifies sys.testing is false when not built for
// testing, so the sys.testing assertion fails. Run under a filter (no runtime
// needed) so it stays fast.
func TestRunTestsWithoutTestingFlag(t *testing.T) {
	cfg := buildTestrunConfig(t, false)

	outcomes, err := cfg.RunTests(context.Background(), func(name string) bool {
		return name == "runs in testing mode"
	})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.True(t, outcomes[0].Failed(), "sys.testing should be false without WithTesting")
}

// TestRunTestsNoTests returns nil (no error) for a config with no test blocks.
func TestRunTestsNoTests(t *testing.T) {
	cfg, diags := config.NewConfig().WithSources([]byte(`bus "main" {}`)).WithLogger(zap.NewNop()).WithTesting(true).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	require.Equal(t, 0, cfg.FunctyTestCount())

	outcomes, err := cfg.RunTests(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, outcomes)
}
