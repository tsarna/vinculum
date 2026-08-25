package functions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// buildHealth builds a config past its boot gate, and returns it with a ctx
// value shaped as an action expression would see one.
func buildHealth(t *testing.T, src string) (*cfg.Config, cty.Value) {
	t.Helper()
	config, diags := cfg.NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%v", diags)
	config.Health.SetBooted()

	evalCtx, err := hclutil.NewEvalContext(context.Background()).BuildEvalContext(config.EvalCtx())
	require.NoError(t, err)
	return config, evalCtx.Variables["ctx"]
}

func call(t *testing.T, config *cfg.Config, name string, args ...cty.Value) cty.Value {
	t.Helper()
	fn, ok := config.EvalCtx().Functions[name]
	require.True(t, ok, "%s is not registered", name)
	got, err := fn.Call(args)
	require.NoError(t, err)
	return got
}

func TestHealthReadyAndLiveAreBooleans(t *testing.T) {
	config, ctx := buildHealth(t, `
check "traffic" { input = true }

check "wedged" {
    probe = "live"
    input = true
}
`)

	assert.True(t, call(t, config, "health::ready", ctx).True())
	assert.True(t, call(t, config, "health::live", ctx).True())
}

func TestHealthReadyIsFalseWhenAReadinessCheckFails(t *testing.T) {
	config, ctx := buildHealth(t, `
check "traffic" {
    input  = false
    reason = "upstream is down"
}

check "wedged" {
    probe = "live"
    input = true
}
`)

	// A readiness failure takes the process out of rotation and leaves
	// liveness alone: a dependency outage must never restart every replica.
	assert.False(t, call(t, config, "health::ready", ctx).True())
	assert.True(t, call(t, config, "health::live", ctx).True())
}

func TestHealthFailingReportsOnlyTheProblems(t *testing.T) {
	config, ctx := buildHealth(t, `
check "up" { input = true }

check "down" {
    input  = false
    reason = "upstream is down"
}
`)

	status := call(t, config, "health::status", ctx).AsValueSlice()
	failing := call(t, config, "health::failing", ctx).AsValueSlice()

	// status carries every contributor, including the built-in process entry;
	// failing is the same report filtered, so the two cannot disagree.
	assert.Len(t, status, 3)
	require.Len(t, failing, 1)
	assert.Equal(t, "check.down", failing[0].GetAttr("component").AsString())
	assert.Equal(t, "upstream is down", failing[0].GetAttr("reason").AsString())
	assert.False(t, failing[0].GetAttr("ready").True())
}

func TestHealthDetailFunctionsTakeAProbe(t *testing.T) {
	config, ctx := buildHealth(t, `
check "traffic" {
    input  = false
    reason = "upstream is down"
}

check "wedged" {
    probe  = "live"
    input  = false
    reason = "pipeline stalled"
}
`)

	live := cty.StringVal("live")

	readyFailing := call(t, config, "health::failing", ctx).AsValueSlice()
	liveFailing := call(t, config, "health::failing", ctx, live).AsValueSlice()

	require.Len(t, readyFailing, 1)
	assert.Equal(t, "check.traffic", readyFailing[0].GetAttr("component").AsString())
	require.Len(t, liveFailing, 1)
	assert.Equal(t, "check.wedged", liveFailing[0].GetAttr("component").AsString())

	// An explicit "ready" means the same as omitting it.
	assert.Equal(t,
		call(t, config, "health::failing", ctx).AsValueSlice(),
		call(t, config, "health::failing", ctx, cty.StringVal("ready")).AsValueSlice())
}

func TestHealthRejectsAnUnknownProbe(t *testing.T) {
	config, ctx := buildHealth(t, `check "c" { input = true }`)

	fn := config.EvalCtx().Functions["health::status"]
	_, err := fn.Call([]cty.Value{ctx, cty.StringVal("startup")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `got "startup"`)
}

func TestHealthRefreshBypassesTheCache(t *testing.T) {
	config, ctx := buildHealth(t, `
var "healthy" { value = true }

check "c" { input = get(var.healthy) }
`)

	require.True(t, call(t, config, "health::ready", ctx).True())

	v, err := richcty.WatchableFromCtyValue(config.CtyVarMap["healthy"])
	require.NoError(t, err)
	settable, ok := v.(richcty.Settable)
	require.True(t, ok)
	_, err = settable.Set(context.Background(), []cty.Value{cty.False})
	require.NoError(t, err)

	// The cached report is younger than the TTL, so the ordinary accessor does
	// not notice. refresh() is the configuration instructing itself and is not
	// rate limited.
	assert.True(t, call(t, config, "health::ready", ctx).True())
	assert.False(t, call(t, config, "health::refresh", ctx).True())
	assert.False(t, call(t, config, "health::ready", ctx).True())
}

func TestHealthRequiresAContext(t *testing.T) {
	// The required leading ctx is what makes a health call in a load-time
	// expression fail loudly rather than quietly answer "everything is ready"
	// from a registry nothing has populated yet.
	_, diags := cfg.NewConfig().
		WithSources([]byte(`const { ok = health::ready() }`)).
		WithLogger(zap.NewNop()).
		Build()

	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "health::ready")
}
