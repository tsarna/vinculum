package config

import (
	"os"
	"reflect"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// TestFunctyFunctionsCallableFromVCL loads a directory mixing a .cty file (which
// declares functions, one calling another and one with a host-registered capsule
// param type) and a .vcl file that calls a functy function from a const
// expression. It proves .cty functions join the shared user-function namespace,
// resolve each other via late binding, and that host type registration took
// effect.
func TestFunctyFunctionsCallableFromVCL(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().
		WithSources("testdata/functy").
		WithLogger(logger).
		Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	// The functy greet() (which itself calls the functy prefix()) evaluated
	// inside a VCL const.
	got, ok := config.Constants["greeting"]
	require.True(t, ok, "greeting const should be present")
	assert.Equal(t, cty.StringVal("hi world"), got)

	// The functy functions are in the shared function namespace.
	_, ok = config.Functions["greet"]
	assert.True(t, ok, "greet should be registered")
	_, ok = config.Functions["on_bus"]
	assert.True(t, ok, "on_bus (bus-typed param) should be registered")
	_, ok = config.Functions["wait_for"]
	assert.True(t, ok, "wait_for (duration-typed param) should be registered")
	_, ok = config.Functions["notify"]
	assert.True(t, ok, "notify (subscriber-typed param) should be registered")
	_, ok = config.Functions["handle"]
	assert.True(t, ok, "handle (http_request/http_response params) should be registered")
	_, ok = config.Functions["inspect"]
	assert.True(t, ok, "inspect (http_client_response/baggage/metric params) should be registered")
}

// testRegCapsuleType is a throwaway capsule type registered via the hook to prove
// leaf-package/plugin type contributions reach the functy parser.
var testRegCapsuleType = cty.CapsuleWithOps("test_reg", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{})

// TestRegisterFunctyType verifies a type contributed via RegisterFunctyType is
// nameable in a .cty annotation.
func TestRegisterFunctyType(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	saved := registeredFunctyTypes
	t.Cleanup(func() { registeredFunctyTypes = saved })
	RegisterFunctyType("test_reg", testRegCapsuleType)

	config, diags := NewConfig().
		WithSources("testdata/functyregistered").
		WithLogger(logger).
		Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	_, ok := config.Functions["uses_registered"]
	assert.True(t, ok, "uses_registered (test_reg-typed param) should be registered")
}

// TestFormatFunctySourceUsesRegisteredTypes proves the guarantee behind
// `fmt --plugin-path`: a type contributed via RegisterFunctyType — exactly what a
// plugin's VinculumPluginInit does — lets FormatFunctySource parse and reformat a
// .cty file whose parameter is annotated with that type. Without the registration
// the same source is an "unknown type" and is returned unchanged (the invariant
// that fmt never rewrites a file it cannot fully parse).
func TestFormatFunctySourceUsesRegisteredTypes(t *testing.T) {
	src, err := os.ReadFile("testdata/functyregistered/reg.cty")
	require.NoError(t, err)

	// Unregistered: unknown type, source returned unchanged.
	out, diags := FormatFunctySource(src, "reg.cty")
	assert.True(t, diags.HasErrors(), "test_reg should be unknown before registration")
	assert.Equal(t, src, out, "unparseable source must be returned unchanged")

	// Registered (as a plugin would): the file now parses and formats cleanly.
	saved := registeredFunctyTypes
	t.Cleanup(func() { registeredFunctyTypes = saved })
	RegisterFunctyType("test_reg", testRegCapsuleType)

	out, diags = FormatFunctySource(src, "reg.cty")
	assert.False(t, diags.HasErrors(), "registered test_reg should let the file parse: %s", diags)
	assert.NotEmpty(t, out)
}

// TestFunctyIgnoresExplicitNonCtyFile guards against collectFunctySources handing
// an explicitly named non-.cty file to functy: functy.ParseSources only applies
// the extension filter during directory walks, so an explicit .vcl file path
// would otherwise be parsed as functy and fail.
func TestFunctyIgnoresExplicitNonCtyFile(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	// A self-contained .vcl passed by explicit file path. Before the fix this
	// failed with a functy parse error ("Top-level functy declarations must be
	// functions") because functy read the explicitly named file regardless of
	// its extension.
	_, diags := NewConfig().
		WithSources("testdata/variable.vcl").
		WithLogger(logger).
		Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

// TestFunctyConstFolding verifies functy top-level `const` declarations fold into
// the shared const pool: a functy const may reference a VCL const and vice versa
// (cross-surface, order-independent), and a typed functy const is enforced.
func TestFunctyConstFolding(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().
		WithSources("testdata/functyconst").
		WithLogger(logger).
		Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	assert.True(t, config.Constants["vcl_base"].RawEquals(cty.NumberIntVal(21)))
	assert.True(t, config.Constants["doubled"].RawEquals(cty.NumberIntVal(42)), "functy const referencing VCL const")
	assert.True(t, config.Constants["from_functy"].RawEquals(cty.NumberIntVal(43)), "VCL const referencing functy const")
	assert.True(t, config.Constants["greeting"].RawEquals(cty.StringVal("hi")))
}

// TestFunctyVarFolding verifies functy top-level `var` declarations fold into the
// var pool: they become mutable var.<name> values (settable, typed), initialized
// from a VCL const, and visible to Process-phase consumers (asserts).
func TestFunctyVarFolding(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().
		WithSources("testdata/functyvar").
		WithLogger(logger).
		Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	// var.counter was set to 42 by the assert; var.label stayed "start".
	require.Contains(t, config.CtyVarMap, "counter")
	require.Contains(t, config.CtyVarMap, "label")
}

// TestFunctyNamespacedConstScoping verifies namespaced functy consts are scoped to
// their namespace instead of folded flat: two namespaces each declare `const
// greeting` without colliding, each body resolves its own, a namespaced body still
// sees the global functy-const and VCL-const surface (own+global), and a namespaced
// const is not exposed to VCL.
func TestFunctyNamespacedConstScoping(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().
		WithSources("testdata/functynsconst").
		WithLogger(logger).
		Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	// Each namespace's greet() resolves its own greeting — no collision.
	callStr := func(name string, args ...cty.Value) cty.Value {
		fn, ok := config.Functions[name]
		require.True(t, ok, "%s should be registered", name)
		v, err := fn.Call(args)
		require.NoError(t, err, "calling %s", name)
		return v
	}
	assert.Equal(t, cty.StringVal("hello world"), callStr("foo::greet", cty.StringVal("world")))
	assert.Equal(t, cty.StringVal("goodbye world"), callStr("bar::greet", cty.StringVal("world")))

	// own+global: foo's greeting + global functy const suffix ("!") + VCL const
	// vcl_tag ("#").
	assert.Equal(t, cty.StringVal("hello!#"), callStr("foo::tagged"))

	// The global functy const and VCL const are on the shared surface; the
	// namespaced const is not.
	assert.True(t, config.Constants["suffix"].RawEquals(cty.StringVal("!")), "global functy const on shared surface")
	assert.True(t, config.Constants["vcl_tag"].RawEquals(cty.StringVal("#")))
	_, exposed := config.Constants["greeting"]
	assert.False(t, exposed, "namespaced const must not be exposed to VCL")
}

// TestFunctyNamespacedVarRejected verifies a top-level functy `var` in a namespace
// is rejected: vinculum vars are global, so there is no namespace to scope one to.
func TestFunctyNamespacedVarRejected(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	_, diags := NewConfig().
		WithSources("testdata/functynsvar").
		WithLogger(logger).
		Build()
	require.True(t, diags.HasErrors(), "expected a namespaced-var error")

	found := false
	for _, d := range diags {
		if d.Severity == hcl.DiagError && d.Summary == "Namespaced var is not supported" {
			found = true
		}
	}
	assert.True(t, found, "expected a Namespaced var diagnostic, got: %v", diags)
}

// TestFunctyBuiltinCollision verifies a .cty function whose name collides with a
// registered builtin (send) is rejected by GetFunctions' duplicate check.
func TestFunctyBuiltinCollision(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	_, diags := NewConfig().
		WithSources("testdata/functycollision").
		WithLogger(logger).
		Build()
	require.True(t, diags.HasErrors(), "expected a duplicate-function error")

	found := false
	for _, d := range diags {
		if d.Severity == hcl.DiagError && d.Summary == "Duplicate function" {
			found = true
		}
	}
	assert.True(t, found, "expected a Duplicate function diagnostic, got: %v", diags)
}
