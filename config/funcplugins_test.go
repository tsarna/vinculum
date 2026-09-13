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

func TestFunctionFeatures_FromTheProbe(t *testing.T) {
	withCleanFunctionPlugins(t)

	RegisterFunctionPlugin("features_plug", func(c *Config) map[string]function.Function {
		out := map[string]function.Function{"always_fn": makeConstantFunc("always")}
		if c.GetFeature("readfiles") != "" {
			out["reads_fn"] = makeConstantFunc("reads")
			// Asked only once readfiles is on, which the all-features probe
			// has to see anyway.
			if c.GetFeature("writefiles") != "" {
				out["writes_fn"] = makeConstantFunc("writes")
			}
		}
		return out
	})

	// A running config that enabled readfiles still reports what the function
	// needs: the answer is how to get the function, not whether this run has it.
	config, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		WithFeature("readfiles", t.TempDir()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	assert.Nil(t, config.FunctionFeatures("always_fn"))
	assert.Equal(t, []string{"readfiles"}, config.FunctionFeatures("reads_fn"))
	assert.Equal(t, []string{"readfiles", "writefiles"}, config.FunctionFeatures("writes_fn"),
		"a function behind two features needs both")
	assert.Nil(t, config.FunctionFeatures("no_such_fn"))

	doc, ok := config.FuncDoc("reads_fn")
	require.True(t, ok)
	assert.Equal(t, []string{"readfiles"}, doc.Features)

	// The answer is a copy: changing it does not change the next one.
	doc.Features[0] = "mutated"
	assert.Equal(t, []string{"readfiles"}, config.FunctionFeatures("reads_fn"))
}

// Build refuses --write-path without --file-path, which the probe cannot see,
// so a function behind writefiles is documented as needing both.
func TestFunctionFeatures_ImpliedByBuild(t *testing.T) {
	withCleanFunctionPlugins(t)

	RegisterFunctionPlugin("implied_plug", func(c *Config) map[string]function.Function {
		if c.GetFeature("writefiles") == "" {
			return nil
		}
		return map[string]function.Function{"writes_only_fn": makeConstantFunc("w")}
	})

	config, diags := NewConfig().WithSources([]byte("")).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
	assert.Equal(t, []string{"readfiles", "writefiles"}, config.FunctionFeatures("writes_only_fn"))
}

// A config's own function is not a plugin's, whatever its name: without the
// flag the plugin's function does not exist, and the config's does.
func TestFuncDoc_UserFunctionSharingAGatedNameHasNoFeatures(t *testing.T) {
	withCleanFunctionPlugins(t)

	RegisterFunctionPlugin("shadowed_plug", func(c *Config) map[string]function.Function {
		if c.GetFeature("allowkill") == "" {
			return nil
		}
		return map[string]function.Function{"shadowed_fn": makeConstantFunc("plugin")}
	})

	config, diags := NewConfig().
		WithSources([]byte(`function "shadowed_fn" {
  params = [a]
  result = a
}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	doc, ok := config.FuncDoc("shadowed_fn")
	require.True(t, ok)
	assert.Nil(t, doc.Features)
	assert.Equal(t, []string{"allowkill"}, config.FunctionFeatures("shadowed_fn"),
		"the binary-level answer is unchanged; only this config's function is exempt")
}

func TestWithEveryFeature_RegistersGatedFunctions(t *testing.T) {
	withCleanFunctionPlugins(t)

	RegisterFunctionPlugin("every_feature_plug", func(c *Config) map[string]function.Function {
		if c.GetFeature("allowkill") == "" {
			return nil
		}
		return map[string]function.Function{"gated_every_fn": makeConstantFunc("gated")}
	})

	config, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		WithEveryFeature().
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	doc, ok := config.FuncDoc("gated_every_fn")
	require.True(t, ok, "a config built WithEveryFeature documents gated functions")
	assert.Equal(t, []string{"allowkill"}, doc.Features)
	assert.Empty(t, config.EnabledFeatureNames(), "no feature is actually enabled")
	assert.Empty(t, config.BaseDir)
}
