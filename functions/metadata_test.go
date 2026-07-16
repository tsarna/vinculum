package functions

import (
	"testing"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// ownFunctions are the cty functions vinculum itself defines (as opposed to the cty
// standard library, whose metadata is upstream, or the sibling cty packages, which carry
// their own metadata and externs). Every one must document itself: a description on the
// function and on each parameter, so help()/doc() and any non-functy cty host see full
// information.
var ownFunctions = []string{
	"log::debug", "log::info", "log::warn", "log::error", "log::msg",
	"diff", "patch",
	"send", "send::go", "send::json",
	"llm::wrap", "sql::must",
	"wire::serialize", "wire::serialize_str", "wire::deserialize",
	"http::get", "http::post", "http::put", "http::delete", "http::head",
	"http::options", "http::patch", "http::request", "http::must",
	"http::response", "http::redirect", "http::error",
	"http::add_header", "http::remove_header", "http::set_cookie", "http::basic_auth",
	"mcp::image", "mcp::error", "mcp::user_message", "mcp::assistant_message",
}

// TestOwnFunctionsAreDocumented guards the metadata of vinculum's own functions against
// regression: a new function, or a new parameter, that ships without documentation.
func TestOwnFunctionsAreDocumented(t *testing.T) {
	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	if diags.HasErrors() {
		t.Fatalf("build config: %s", diags)
	}
	funcs := config.EvalCtx().Functions

	for _, name := range ownFunctions {
		fn, ok := funcs[name]
		if !ok {
			t.Errorf("%s() is not registered", name)
			continue
		}
		if fn.Description() == "" {
			t.Errorf("%s() has no cty Description", name)
		}
		for _, p := range fn.Params() {
			if p.Description == "" {
				t.Errorf("%s() parameter %q has no Description", name, p.Name)
			}
		}
		if vp := fn.VarParam(); vp != nil && vp.Description == "" {
			t.Errorf("%s() variadic parameter %q has no Description", name, vp.Name)
		}
	}
}

// The functions whose return type is genuinely dynamic — a structural diff, a patched
// value, a deserialized value, a pass-through result — are documented as such in prose,
// since no signature can state a type that varies with the input. Every *other* own
// function's return must be visible in reflected metadata: a static return hidden behind a
// dynamic parameter (poisoned to dynamic by cty) is a metadata bug, fixed by opting the
// parameter into AllowDynamicType. This guards that the fix stays in place.
func TestOwnFunctionsWithStaticReturnsExposeThem(t *testing.T) {
	genuinelyDynamic := map[string]bool{
		"diff": true, "patch": true, "wire::deserialize": true, "sql::must": true,
	}

	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	if diags.HasErrors() {
		t.Fatalf("build config: %s", diags)
	}
	funcs := config.EvalCtx().Functions

	for _, name := range ownFunctions {
		if genuinelyDynamic[name] {
			continue
		}
		fn, ok := funcs[name]
		if !ok {
			continue // reported by the other test
		}
		argTypes := make([]cty.Type, len(fn.Params()))
		for i, p := range fn.Params() {
			argTypes[i] = p.Type
		}
		rt, err := fn.ReturnType(argTypes)
		if err != nil {
			t.Errorf("%s ReturnType: %v", name, err)
			continue
		}
		if rt == cty.DynamicPseudoType || rt == cty.NilType {
			t.Errorf("%s() return type is hidden (dynamic) in reflected metadata; a dynamic "+
				"parameter poisoned the static return — give that parameter AllowDynamicType", name)
		}
	}
}
