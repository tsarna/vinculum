package functions

import (
	_ "embed"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

//go:embed testdata/cond_funcs.vcl
var condFuncsVCL []byte

//go:embed testdata/try_funcs.vcl
var tryFuncsVCL []byte

//go:embed testdata/switch_funcs.vcl
var switchFuncsVCL []byte

// testCallCounter tracks how many times a side-effecting test function was
// invoked. Used by the single-eval proof test for try().
var testCallCounter atomic.Int64

func init() {
	cfg.RegisterFunctionPlugin("_test_counter", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"_test_bump_counter": function.New(&function.Spec{
				Type: function.StaticReturnType(cty.String),
				Impl: func(_ []cty.Value, _ cty.Type) (cty.Value, error) {
					testCallCounter.Add(1)
					return cty.StringVal("bumped"), nil
				},
			}),
		}
	})
}

func buildVCL(t *testing.T, src []byte) (*cfg.Config, error) {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(logger).Build()
	if diags.HasErrors() {
		return nil, diags
	}
	return c, nil
}

func TestCond(t *testing.T) {
	_, err := buildVCL(t, condFuncsVCL)
	require.NoError(t, err, "cond_funcs.vcl assertions should all pass")
}

func TestCondArityErrors(t *testing.T) {
	cases := map[string]string{
		"even count": `const { x = cond(true, "b") }`,
		"too few":    `const { x = cond(true) }`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := buildVCL(t, []byte(src))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cond requires an odd number")
		})
	}
}

func TestCondNullCondition(t *testing.T) {
	src := `const { x = cond(null, "b", "c") }`
	_, err := buildVCL(t, []byte(src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
}

func TestTry(t *testing.T) {
	_, err := buildVCL(t, tryFuncsVCL)
	require.NoError(t, err, "try_funcs.vcl assertions should all pass")
}

func TestTryZeroArgs(t *testing.T) {
	src := `const { x = try() }`
	_, err := buildVCL(t, []byte(src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one")
}

func TestTryAllFailAggregatesDiagnostics(t *testing.T) {
	src := `const { x = try(error("alpha"), error("beta")) }`
	_, err := buildVCL(t, []byte(src))
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "alpha")
	assert.Contains(t, msg, "beta")
}

// TestTrySingleEval verifies that our try() evaluates the selected expression
// exactly once. The upstream hcl/v2/ext/tryfunc.TryFunc evaluates twice
// (once for Type inference, once for Impl), so the counter would read 2 there.
func TestTrySingleEval(t *testing.T) {
	testCallCounter.Store(0)
	src := `const { x = try(_test_bump_counter(), "fallback") }`
	_, err := buildVCL(t, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, int64(1), testCallCounter.Load(),
		"side-effecting try() argument must be evaluated exactly once")
}

// TestTryFallbackLazy confirms that later arguments are not evaluated when an
// earlier one succeeds (the counter appears only in the fallback slot).
func TestTryFallbackLazy(t *testing.T) {
	testCallCounter.Store(0)
	src := `const { x = try("first", _test_bump_counter()) }`
	_, err := buildVCL(t, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, int64(0), testCallCounter.Load(),
		"unused try() arguments must not be evaluated")
}

// TestCondLazyViaCounter backs up the error()-based laziness proofs in the VCL
// fixture with a Go-side counter check.
func TestCondLazyViaCounter(t *testing.T) {
	testCallCounter.Store(0)
	src := `const {
		a = cond(true, "ok", _test_bump_counter())
		b = cond(false, _test_bump_counter(), "ok")
	}`
	_, err := buildVCL(t, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, int64(0), testCallCounter.Load(),
		"cond() must not evaluate branches it doesn't select")
}

func TestSwitch(t *testing.T) {
	_, err := buildVCL(t, switchFuncsVCL)
	require.NoError(t, err, "switch_funcs.vcl assertions should all pass")
}

func TestSwitchArityErrors(t *testing.T) {
	cases := map[string]string{
		"zero args": `const { x = switch() }`,
		"one arg":   `const { x = switch(1) }`,
		"two args":  `const { x = switch(1, "a") }`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := buildVCL(t, []byte(src))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "switch requires at least 3")
		})
	}
}

// No match and no default: the function errors at call time.
func TestSwitchNoMatchNoDefault(t *testing.T) {
	src := `const { x = switch(99, 1, "one", 2, "two") }`
	_, err := buildVCL(t, []byte(src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no case matched")
}

// TestSwitchOnEvaluatedOnce verifies that the `on` expression — which the
// implementation reads exactly once — actually runs once even when several
// case-value comparisons are performed.
func TestSwitchOnEvaluatedOnce(t *testing.T) {
	testCallCounter.Store(0)
	src := `const { x = switch(_test_bump_counter(), "a", "no", "b", "no", "bumped", "yes", "default") }`
	c, err := buildVCL(t, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, int64(1), testCallCounter.Load(),
		"switch() must evaluate `on` exactly once")
	_ = c
}

// TestSwitchSkipsLaterCaseValues confirms that case-values past the matching
// arm are not evaluated.
func TestSwitchSkipsLaterCaseValues(t *testing.T) {
	testCallCounter.Store(0)
	src := `const { x = switch("hit", "hit", "ok", _test_bump_counter(), "never", "default") }`
	_, err := buildVCL(t, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, int64(0), testCallCounter.Load(),
		"switch() must not evaluate case values after a match")
}

// Sanity check: non-bool string condition that isn't convertible errors.
func TestCondNonBoolCondition(t *testing.T) {
	src := `const { x = cond("not a bool", "b", "c") }`
	_, err := buildVCL(t, []byte(src))
	require.Error(t, err)
	// The exact message comes from convert.Convert; just confirm we errored
	// out with something about bool.
	assert.True(t, strings.Contains(err.Error(), "bool") || strings.Contains(err.Error(), "convert"),
		"expected a conversion error, got: %v", err)
}
