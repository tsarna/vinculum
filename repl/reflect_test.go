package repl

import (
	"strings"
	"testing"

	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// These tests are the end-to-end proof of the functy extern feature.
//
// rich-cty-types provides get/set/count/... in Go. Each takes an optional *leading*
// context, sniffed out of the first argument at call time. cty can only make a
// function's *trailing* parameters optional, so that context — and every named
// trailing argument with it — is swallowed into one anonymous variadic, and from cty
// metadata alone the whole family reflects as the useless `get(thing, ...args)`.
//
// rich-cty-types therefore ships externs.cty declaring what they really are;
// vinculum registers it (functions/external.go); and help() prefers it over the cty
// fallback. These tests assert that the true signature is what a user sees.

func TestHelpRendersExternSignature(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	got := evalString(t, h, `help("get")`).AsString()

	// The signature cty cannot express: an optional LEADING parameter, a named
	// optional trailing one, and a variadic tail.
	if !strings.HasPrefix(got, "get(ctx?: ctx, thing, fallback?, *args) -> any") {
		t.Fatalf("help(\"get\") did not render the extern signature:\n%s", got)
	}
	// Per-parameter docs, which cty erased along with the parameters.
	for _, want := range []string{"Parameters:", "ctx?", "fallback?", "the thing to read"} {
		if !strings.Contains(got, want) {
			t.Errorf("help(\"get\") is missing %q:\n%s", want, got)
		}
	}
	// The cty fallback would have rendered this instead. If it appears, the eval
	// context won the lookup and the extern did nothing.
	if strings.Contains(got, "get(thing, ...args)") {
		t.Fatalf("the cty metadata shadowed the extern:\n%s", got)
	}
}

// The four functions whose VarParam exists *solely* to make room for the optional
// leading ctx — their Impl discards the rest — take no trailing arguments at all.
// The extern is the only place that can be said.
func TestHelpRendersExternsWithNoTrailingArgs(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	for name, want := range map[string]string{
		"count": "count(ctx?: ctx, thing) -> number",
		"clear": "clear(ctx?: ctx, thing) -> null",
		"reset": "reset(ctx?: ctx, thing) -> null",
		"state": "state(ctx?: ctx, thing) -> string",
	} {
		got := evalString(t, h, `help("`+name+`")`).AsString()
		if !strings.HasPrefix(got, want) {
			t.Errorf("help(%q) =\n%s\nwant it to start with\n%s", name, got, want)
		}
	}
}

// call() is the one member of the family whose context is REQUIRED, and whose
// variadic tail is a genuine variadic.
func TestHelpRendersRequiredCtx(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	got := evalString(t, h, `help("call")`).AsString()
	if !strings.HasPrefix(got, "call(ctx: ctx, thing, *args) -> any") {
		t.Fatalf("help(\"call\") should show a required ctx:\n%s", got)
	}
}

// The other half of the brief: a function whose cty metadata CAN describe it fully
// gets no extern, and must still render completely — from that metadata alone.
func TestHelpRendersCtyMetadataWhenNoExtern(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	got := evalString(t, h, `help("length")`).AsString()

	// Rendered from the cty spec: the parameter is DynamicPseudoType, so it carries
	// no type annotation — which is honest, and all cty has to say.
	if !strings.HasPrefix(got, "length(v)") {
		t.Fatalf("help(\"length\") did not render from cty metadata:\n%s", got)
	}
	// The Spec.Description and the Parameter.Description, which are the *only*
	// documentation length() has: it carries no extern.
	if !strings.Contains(got, "How many elements a value holds") {
		t.Errorf("help(\"length\") is missing its description:\n%s", got)
	}
	if !strings.Contains(got, "Lengthable capsule") {
		t.Errorf("help(\"length\") is missing its parameter documentation:\n%s", got)
	}
	// It has no extern, so it must not have acquired a variadic.
	if strings.Contains(got, "*args") {
		t.Errorf("length() should have no variadic:\n%s", got)
	}
}

// doc() reads cty metadata, not the extern — so the Description on each Go spec is
// what makes it non-empty. Without it, doc("get") would report a function that
// exists but is undocumented, while help("get") showed a full block.
func TestDocReadsCtyDescriptions(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	for _, name := range []string{"get", "set", "count", "call", "length", "tostring"} {
		got := evalString(t, h, `doc("`+name+`")`)
		if got.IsNull() || got.AsString() == "" {
			t.Errorf("doc(%q) is empty: the cty Spec.Description is missing", name)
		}
	}
}

// The externs must be visible with no user .cty sources at all — that is the case
// functyState.compile used to bail out of early, leaving a nil Result.
func TestHelpWorksWithNoCtySources(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`) // VCL only; not a line of functy
	got := evalString(t, h, `help("get")`).AsString()
	if !strings.Contains(got, "ctx?: ctx") {
		t.Fatalf("externs are invisible when the user has no .cty files:\n%s", got)
	}
}

// A user .cty function that collides with a registered extern is an error: the
// extern documents a function the host provides, so a user function of that name
// would make it a lie. functy reports this by name at Build, rather than leaving it
// to surface later and vaguely as a reserved-name clash.
func TestUserFunctionCollidingWithAnExternIsRejected(t *testing.T) {
	_, diags := config.NewConfig().
		WithSources("../config/testdata/functyexterncollision").
		WithLogger(zap.NewNop()).
		Build()

	if !diags.HasErrors() {
		t.Fatal("expected a collision error for a user function named get()")
	}
	if !strings.Contains(diags.Error(), "get") {
		t.Fatalf("collision diagnostic does not name the function:\n%s", diags.Error())
	}
}

// help() with no argument lists what is callable. The externs name functions that
// ARE in the eval context, so they appear either way — but the listing must not have
// lost them.
func TestHelpWithNoArgumentListsFunctions(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	got := evalString(t, h, `help()`).AsString()
	for _, want := range []string{"get", "length", "help"} {
		if !strings.Contains(got, want) {
			t.Errorf("help() listing is missing %q", want)
		}
	}
}
