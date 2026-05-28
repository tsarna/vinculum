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

// transformPluginTestMu serializes access to the package-global
// transformPlugins slice across these tests. Each test takes a snapshot
// of the slice on entry and restores it on cleanup so registrations
// don't leak between tests.
var transformPluginTestMu sync.Mutex

// withCleanTransformPlugins snapshots the global transformPlugins slice
// and restores it on test cleanup. Tests that mutate the registry must
// call this before doing so.
func withCleanTransformPlugins(t *testing.T) {
	t.Helper()
	transformPluginTestMu.Lock()
	snapshot := append([]transformPluginEntry(nil), transformPlugins...)
	t.Cleanup(func() {
		transformPlugins = snapshot
		transformPluginTestMu.Unlock()
	})
}

// makeIdentityTransform returns a cty function that produces a
// MessageTransformCapsule value. The function body is not exercised by
// the collision check — only its registered name matters.
func makeIdentityTransform() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(MessageTransformCapsuleType),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			return NewMessageTransformCapsule(nil), nil
		},
	})
}

func TestTransformPlugin_CollidesWithBuiltin(t *testing.T) {
	withCleanTransformPlugins(t)

	// "jq" is a built-in transform name.
	RegisterTransformPlugin("collide_with_builtin", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{
			"jq": makeIdentityTransform(),
		}
	})

	_, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(), "expected collision diagnostic")
	assert.Contains(t, allDiagText(diags), "collide_with_builtin")
	assert.Contains(t, allDiagText(diags), `"jq"`)
}

func TestTransformPlugin_CollidesBetweenPlugins(t *testing.T) {
	withCleanTransformPlugins(t)

	RegisterTransformPlugin("plug_a", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{
			"plug_a_b_shared": makeIdentityTransform(),
		}
	})
	RegisterTransformPlugin("plug_b", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{
			"plug_a_b_shared": makeIdentityTransform(),
		}
	})

	_, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(), "expected cross-plugin collision diagnostic")
	combined := allDiagText(diags)
	assert.Contains(t, combined, "plug_a")
	assert.Contains(t, combined, "plug_b")
	assert.Contains(t, combined, `"plug_a_b_shared"`)
}

func TestTransformPlugin_UniqueNameMerged(t *testing.T) {
	withCleanTransformPlugins(t)

	RegisterTransformPlugin("unique_plug", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{
			"unique_plug_xform": makeIdentityTransform(),
		}
	})

	// A subscription using the plugin-registered transform should parse
	// cleanly. This exercises the merge path inside getTransformExprEvalCtx.
	src := []byte(`
bus "main" { queue_size = 16 }
subscription "s" {
    target = bus.main
    topics = ["foo"]
    transforms = [unique_plug_xform()]
    action = "noop"
}
`)
	_, diags := NewConfig().
		WithSources(src).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
}

// allDiagText concatenates every diagnostic's Summary and Detail so the
// caller can assert.Contains across all of them, not just the first.
// hcl.Diagnostics.Error() truncates after the first diagnostic.
func allDiagText(diags interface{ Errs() []error }) string {
	var s string
	for _, e := range diags.Errs() {
		s += e.Error() + "\n"
	}
	return s
}
