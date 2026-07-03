package config

import (
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"
	bytescty "github.com/tsarna/bytes-cty-type"
	"github.com/tsarna/functy"
	richcty "github.com/tsarna/rich-cty-types"
	timecty "github.com/tsarna/time-cty-funcs"
	urlcty "github.com/tsarna/url-cty-funcs"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// functyTypeRegistration is a host type contributed for use in .cty annotations,
// either identity-enforced (ty set) or open/predicate-backed (pred set).
type functyTypeRegistration struct {
	name string
	ty   cty.Type              // identity type (open == false)
	pred func(cty.Value) error // predicate (open == true)
	open bool
}

// registeredFunctyTypes holds types contributed via RegisterFunctyType /
// RegisterFunctyOpenType. It exists so leaf packages that import config (clients,
// functions, triggers, conditions) — and cannot be imported back by config
// without an import cycle — can still make their capsule/object types nameable in
// .cty annotations by self-registering in init(). The same hook is the mechanism
// a .vinit plugin would use.
var registeredFunctyTypes []functyTypeRegistration

// RegisterFunctyType registers a closed (identity-enforced) named type for use in
// .cty type annotations: a raw capsule type, or a rich object's Object type when
// that object has a *fixed* attribute set (a single static cty.Type). Prefer this
// over the open form — closed types are usable in more positions. Call from a
// package's init(); applied when the functy parser is built during Build().
func RegisterFunctyType(name string, ty cty.Type) {
	registeredFunctyTypes = append(registeredFunctyTypes, functyTypeRegistration{name: name, ty: ty})
}

// RegisterFunctyOpenType registers an open, predicate-backed named type: the
// value is passed through untouched when pred returns nil (attributes preserved),
// else rejected. Use only when no single fixed cty.Type exists — a rich object
// whose attribute set varies per instance, or a heterogeneous interface-dispatch
// type spanning many capsule types.
func RegisterFunctyOpenType(name string, pred func(cty.Value) error) {
	registeredFunctyTypes = append(registeredFunctyTypes, functyTypeRegistration{name: name, pred: pred, open: true})
}

// functyState holds the parsed .cty artifacts kept on Config for reuse across
// build phases: the parsed Result (functions, and top-level var/const decls
// folded into Vinculum's pools), the raw sources (for rendering functy errors
// with source context), and the configured parser (whose TypeResolver is shared
// with the VCL `var` `type` attribute).
type functyState struct {
	result  *functy.Result
	sources []functy.Source
	parser  *functy.Parser
	// files maps each .cty filename to its source bytes, for rendering functy
	// throws with source context at runtime. Built once during compile() (the
	// sources are immutable after Build), so ActionError need not rebuild it.
	files map[string]*hcl.File
}

// collectFunctySources gathers functy (.cty) sources from the same heterogeneous
// source set passed to WithSources, delegating directory/embed.FS walks to
// functy.ParseSources (recursive, skipping dot-dirs, filtering on ".cty").
//
// Two source kinds are filtered out here rather than forwarded:
//   - Bare []byte: in Vinculum's source set a []byte is .vcl content supplied by
//     a test fixture, never functy source (functy.ParseSources would treat it as
//     one anonymous <bytes> source). This mirrors how the .vinit pass skips
//     []byte (parse.go's acceptBytes handling).
//   - An explicit non-.cty *file* path (e.g. `check config.vcl`):
//     functy.ParseSources only applies the ".cty" extension filter during
//     directory walks — an explicitly named file is read regardless of
//     extension — so a `.vcl` file passed by path would otherwise be parsed as
//     functy. Directories are forwarded as-is (functy walks and filters them).
func collectFunctySources(sources []any) ([]functy.Source, hcl.Diagnostics) {
	filtered := make([]any, 0, len(sources))
	add := func(s any) {
		if path, ok := s.(string); ok && !keepFunctyPath(path) {
			return
		}
		filtered = append(filtered, s)
	}
	for _, s := range sources {
		switch v := s.(type) {
		case []byte:
			continue
		case []string:
			for _, p := range v {
				add(p)
			}
		default:
			add(s)
		}
	}
	return functy.ParseSources(filtered...)
}

// keepFunctyPath reports whether a string path should be handed to
// functy.ParseSources: directories (functy walks and filters them for .cty) and
// .cty files. A non-.cty file, or a path that cannot be stat'd (the .vcl/.vinit
// passes report those), is skipped.
func keepFunctyPath(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return true
	}
	return strings.HasSuffix(path, functy.Extension)
}

// newFunctyParser builds a functy parser configured with Vinculum's named types
// so .cty type annotations can reference the capsule and rich-object types that
// VCL expressions expose.
//
// Prefer closed (identity) types; a closed type is usable in more positions than
// an open one. The deciding factor is whether the value has a single fixed
// cty.Type:
//   - Closed → RegisterType (identity). Anything with a fixed type: a raw capsule
//     (bus/server/variable/time/duration/baggage/metric/wire_format), or a rich
//     object with a *fixed* attribute set (url/bytes and the http* request/
//     response objects, whose Object types are static cty.Object values).
//   - Open → RegisterOpenType (value passed through untouched, attributes
//     preserved). Only where no single fixed type exists: a rich object whose
//     attribute set *varies per instance* (ctx — handler-dependent fields;
//     sql_client — one attribute per declared query), or a heterogeneous
//     interface-dispatch type spanning many capsule types (client — any
//     config.Client; subscriber — any bus.Subscriber).
func newFunctyParser() *functy.Parser {
	p := functy.NewParser().
		// Top-level var/const declarations are folded into Vinculum's own const
		// and var pools (see foldFunctyConsts / foldFunctyVars).
		AllowTopLevelConst(true).
		AllowTopLevelVar(true).
		// Raw capsule types (identity).
		RegisterType("bus", EventBusCapsuleType).
		RegisterType("server", ServerCapsuleType).
		RegisterType("variable", types.VariableCapsuleType).
		RegisterType("time", timecty.TimeCapsuleType).
		RegisterType("duration", timecty.DurationCapsuleType).
		RegisterType("baggage", types.BaggageCapsuleType).
		RegisterType("metric", types.MetricCapsuleType).
		RegisterType("wire_format", WireFormatCapsuleType).
		// Rich object types (identity on the object wrapping the capsule).
		RegisterType("url", urlcty.URLObjectType).
		RegisterType("bytes", bytescty.BytesObjectType).
		RegisterType("http_request", types.HTTPRequestObjectType).
		RegisterType("http_response", types.HTTPResponseObjectType).
		RegisterType("http_client_response", types.HTTPClientResponseObjectType).
		// Open, predicate-backed types (non-destructive pass-through).
		RegisterOpenType("ctx", richcty.IsContextObject).
		RegisterOpenType("subscriber", IsSubscriber).
		RegisterOpenType("client", IsClient)

	// Apply types contributed by leaf packages (and future plugins) via
	// RegisterFunctyType / RegisterFunctyOpenType. These cannot be referenced
	// directly here because their packages import config.
	for _, r := range registeredFunctyTypes {
		if r.open {
			p.RegisterOpenType(r.name, r.pred)
		} else {
			p.RegisterType(r.name, r.ty)
		}
	}

	return p
}

// newFunctyState builds the functy parser configured with Vinculum's named types.
// It is created during Build() even when there are no .cty sources, so the type
// resolver is available to VCL `var` type constraints; result/sources are
// populated later by compile.
func newFunctyState() *functyState {
	return &functyState{parser: newFunctyParser()}
}

// resolver is the type resolver shared between .cty parsing and the VCL `var`
// `type` attribute, so both accept identical type syntax and the same
// host-registered named types.
func (s *functyState) resolver() *functy.TypeResolver {
	return s.parser.Types()
}

// compile parses the collected .cty sources with the state's configured parser
// (so all files share one namespace and type environment) and compiles the
// resulting function declarations into cty functions. Each compiled function
// captures evalCtxFn for late binding, exactly like procedure and user functions,
// enabling recursion, mutual recursion, and reference to const/var/ambient values
// finalized after compilation. The parsed Result (functions plus top-level
// var/const decls, folded into Vinculum's pools later) is retained on the state.
// The function map is nil when there are no .cty sources.
func (s *functyState) compile(sources []functy.Source, evalCtxFn func() *hcl.EvalContext) (map[string]function.Function, hcl.Diagnostics) {
	s.sources = sources
	if len(sources) == 0 {
		return nil, nil
	}

	// Cache the filename→bytes map once for runtime functy-error rendering.
	s.files = make(map[string]*hcl.File, len(sources))
	for _, src := range sources {
		s.files[src.Filename] = &hcl.File{Bytes: src.Bytes}
	}

	result, diags := s.parser.ParseAll(sources)
	s.result = result
	if diags.HasErrors() {
		return nil, diags
	}

	funcs, compileDiags := result.Compile(evalCtxFn)
	return funcs, diags.Extend(compileDiags)
}
