package config

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

// functionPluginTestMu serializes access to the package-global functionPlugins
// slice across these tests. Each test takes a snapshot of the slice on entry
// and restores it on cleanup so registrations don't leak between tests.
var functionPluginTestMu sync.Mutex

// withCleanFunctionPlugins snapshots the global functionPlugins slice and
// restores it on test cleanup. Tests that mutate the registry must call this
// before doing so.
func withCleanFunctionPlugins(t *testing.T) {
	t.Helper()
	functionPluginTestMu.Lock()
	snapshot := append([]functionPluginEntry(nil), functionPlugins...)
	t.Cleanup(func() {
		functionPlugins = snapshot
		functionPluginTestMu.Unlock()
	})
}

// makeConstantFunc returns a zero-argument function returning a fixed string.
// Only its registered name matters to the collision check; the distinct return
// value is what lets a test tell two registrations of one name apart.
func makeConstantFunc(result string) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.String),
		Impl: func(_ []cty.Value, _ cty.Type) (cty.Value, error) {
			return cty.StringVal(result), nil
		},
	})
}

func TestFunctionPlugin_CollidesBetweenPlugins(t *testing.T) {
	withCleanFunctionPlugins(t)

	RegisterFunctionPlugin("func_plug_a", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{"shared_fn": makeConstantFunc("a")}
	})
	RegisterFunctionPlugin("func_plug_b", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{"shared_fn": makeConstantFunc("b")}
	})

	_, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(), "expected cross-plugin collision diagnostic")
	combined := allDiagText(diags)
	assert.Contains(t, combined, "func_plug_a")
	assert.Contains(t, combined, "func_plug_b")
	assert.Contains(t, combined, `"shared_fn"`)
}

func TestFunctionPlugin_CollisionIsReportedEvenWhenTheFeatureIsOff(t *testing.T) {
	withCleanFunctionPlugins(t)

	// The shape the in-tree `basename` collision had: one plugin contributes the
	// name unconditionally, the other only behind a feature flag. Nothing in a
	// run without the flag can call the gated copy — but the binary is still
	// ambiguous, and the run that would notice is not the run that needs
	// telling, so the diagnostic does not wait for the flag.
	RegisterFunctionPlugin("always_plug", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{"gated_fn": makeConstantFunc("always")}
	})
	RegisterFunctionPlugin("gated_plug", func(c *Config) map[string]function.Function {
		if c.GetFeature("readfiles") == "" {
			return nil
		}
		return map[string]function.Function{"gated_fn": makeConstantFunc("gated")}
	})

	_, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(),
		"a collision behind a disabled feature should still be reported")
	combined := allDiagText(diags)
	assert.Contains(t, combined, "always_plug")
	assert.Contains(t, combined, "gated_plug")
	assert.Contains(t, combined, `"gated_fn"`)
}

func TestFunctionPlugin_GatedNameAloneIsNotACollision(t *testing.T) {
	withCleanFunctionPlugins(t)

	// The probe must not turn a single feature-gated plugin into a collision
	// with itself, nor leak its functions into a run that did not enable it.
	RegisterFunctionPlugin("lonely_gated_plug", func(c *Config) map[string]function.Function {
		if c.GetFeature("readfiles") == "" {
			return nil
		}
		return map[string]function.Function{"lonely_gated_fn": makeConstantFunc("gated")}
	})

	config, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	assert.NotContains(t, config.Functions, "lonely_gated_fn",
		"a gated function must not reach a run that did not enable its feature")

	withFeature, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		WithFeature("readfiles", t.TempDir()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	assert.Contains(t, withFeature.Functions, "lonely_gated_fn")
}

func TestFunctionPlugin_UniqueNamesMerged(t *testing.T) {
	withCleanFunctionPlugins(t)

	RegisterFunctionPlugin("unique_func_plug", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{"unique_fn": makeConstantFunc("ok")}
	})

	config, diags := NewConfig().
		WithSources([]byte(`const { got = unique_fn() }`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	assert.Contains(t, config.Functions, "unique_fn")
}
