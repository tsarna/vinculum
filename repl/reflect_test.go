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

// bytes-cty-type's three functions are the other shape an extern exists for: an
// overload set. Each takes a string *or* a bytes value, and cty has no union type;
// base64decode goes further and picks its *return* type from how many arguments it was
// given, which a single cty signature cannot say at all. Each is declared as one form
// per arm, and help() must render the whole set.
func TestHelpRendersBytesOverloadSets(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	for name, want := range map[string][]string{
		"bytes": {
			"bytes(s: string, content_type?: string) -> bytes",
			"bytes(b: bytes, content_type?: string) -> bytes",
		},
		"base64encode": {
			"base64encode(s: string) -> string",
			"base64encode(b: bytes) -> string",
		},
		// The one whose return type moves.
		"base64decode": {
			"base64decode(s: string) -> string",
			"base64decode(s: string, content_type: string) -> bytes",
		},
	} {
		got := evalString(t, h, `help("`+name+`")`).AsString()
		for _, form := range want {
			if !strings.Contains(got, form) {
				t.Errorf("help(%q) is missing the form %q:\n%s", name, form, got)
			}
		}
		// The cty fallback renders the fake variadic. If it shows up, the extern lost.
		if strings.Contains(got, "...content_type") {
			t.Errorf("the cty metadata shadowed the extern for %s():\n%s", name, got)
		}
	}
}

// barcode() is the third shape: a single function with an *optional object* argument.
// cty can only make it optional by making it variadic, and cannot describe its shape at
// all, so it reflects as `barcode(type, data, ...options)` with the four options
// invisible. The extern names them, and help() must render the object type — with its
// optional() markers — inline in the signature, not cty's flattened "object".
func TestHelpRendersBarcodeOptionalObject(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	got := evalString(t, h, `help("barcode")`).AsString()

	if !strings.HasPrefix(got, "barcode(type: string, data: string, options?: object({") {
		t.Fatalf("help(\"barcode\") did not render the extern signature:\n%s", got)
	}
	// The object's attributes, each marked optional — the whole point of the extern.
	for _, want := range []string{
		"scale = optional(number)",
		"error_correction = optional(string)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("help(\"barcode\") signature is missing %q:\n%s", want, got)
		}
	}
	// The cty fallback would render the variadic. If it shows, the extern lost.
	if strings.Contains(got, "...options") {
		t.Fatalf("the cty metadata shadowed the extern:\n%s", got)
	}
}

// The geo functions are the case that needs the geopoint open type and, for six of them,
// an extern to expose a return shape cty hides. Every geo point argument is `dynamic` in
// cty (a point carries arbitrary extras), and a dynamic argument poisons cty's return
// type to dynamic — so geo::inverse's whole payload is invisible from metadata alone. The
// externs restore both: geopoint-typed inputs and the real return shape, including inside
// list(geopoint), which exercises functy's nested-opaque type resolution. The names are
// namespaced (geo::, sky::), so help() also proves namespaced lookup reaches an extern.
func TestHelpRendersGeoWithGeopointAndReturnShapes(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	for name, want := range map[string]string{
		// A fixed-shape function whose return cty hid behind dynamic params.
		"geo::inverse": "geo::inverse(point_a: geopoint, point_b: geopoint) -> object({ back_bearing = number, bearing = number, distance = number })",
		// list(geopoint) — the nested-opaque case.
		"geo::area": "geo::area(polygon: list(geopoint)) -> number",
		// A sky:: function: optional trailing time, structural return.
		"sky::sun_position": "sky::sun_position(point: geopoint, t?: time) -> object({ altitude = number, azimuth = number })",
	} {
		got := evalString(t, h, `help("`+name+`")`).AsString()
		if !strings.HasPrefix(got, want) {
			t.Errorf("help(%q) =\n%s\nwant it to start with\n%s", name, got, want)
		}
	}

	// geo::point is an overload set returning geopoint.
	gp := evalString(t, h, `help("geo::point")`).AsString()
	for _, form := range []string{"geo::point(coords: string) -> geopoint", "geo::point(lat, lon, base) -> geopoint"} {
		if !strings.Contains(gp, form) {
			t.Errorf("help(\"geo::point\") missing form %q:\n%s", form, gp)
		}
	}
}

// sqid pairs both extern reasons in one package: sqid()'s id is a number-or-list union
// (declared as one form per arm), and both functions take an optional options object that
// cty renders shapeless behind a variadic. help() must show the union forms and the full
// object shape.
func TestHelpRendersSqidUnionAndOptions(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	opts := "options?: object({ alphabet = optional(string), blocklist = optional(list(string)), min_length = optional(number) })"

	sq := evalString(t, h, `help("sqid")`).AsString()
	for _, form := range []string{
		"sqid(id: number, " + opts + ") -> string",
		"sqid(ids: list(number), " + opts + ") -> string",
	} {
		if !strings.Contains(sq, form) {
			t.Errorf("help(\"sqid\") missing form:\n%s\ngot:\n%s", form, sq)
		}
	}

	un := evalString(t, h, `help("unsqid")`).AsString()
	if !strings.HasPrefix(un, "unsqid(s: string, "+opts+") -> list(number)") {
		t.Errorf("help(\"unsqid\") did not render the options object:\n%s", un)
	}
}

// The url functions are namespaced (url::), and three of them lose their return type to
// the dynamic-argument poisoning (their base/ref/params are unions cty renders `dynamic`).
// Their externs restore the return; the three with concrete string params keep their
// complete cty metadata. url::encode is stdlib's urlencode, aliased for symmetry with
// url::decode.
func TestHelpRendersURLNamespace(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)
	for name, want := range map[string]string{
		// Extern restores the return hidden by a dynamic param, and keeps the param docs.
		"url::join":      "url::join(base, ref) -> url",
		"url::join_path": "url::join_path(base, *elem: string) -> url",
		// cty-complete, no extern.
		"url::parse":  "url::parse(rawURL: string) -> url",
		"url::decode": "url::decode(str: string) -> string",
		// The stdlib alias.
		"url::encode": "url::encode(str: string) -> string",
	} {
		got := evalString(t, h, `help("`+name+`")`).AsString()
		if !strings.HasPrefix(got, want) {
			t.Errorf("help(%q) =\n%s\nwant it to start with\n%s", name, got, want)
		}
	}

	// query_encode is an overload set over the two map value types.
	qe := evalString(t, h, `help("url::query_encode")`).AsString()
	for _, form := range []string{
		"url::query_encode(params: map(string)) -> string",
		"url::query_encode(params: map(list(string))) -> string",
	} {
		if !strings.Contains(qe, form) {
			t.Errorf("help(\"url::query_encode\") missing form %q:\n%s", form, qe)
		}
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
