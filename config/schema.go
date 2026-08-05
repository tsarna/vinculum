package config

// Machine-readable description of the VCL block language, emitted by
// `vinculum schema`.
//
// The structural skeleton — what attributes exist, whether they are required,
// how blocks nest — is reflected from the same gohcl-tagged decode structs the
// parser uses, so it cannot describe a block the parser can't parse and cannot
// go stale when a field is added. Curated metadata (prose, value hints,
// cross-attribute constraints) is authored next to those structs as TypeSchema
// values and validated against the reflected structure.
//
// This file has three parts: the curated authoring types, the reflector, and
// the emitted document types plus the builder that merges the two.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/version"
	"github.com/zclconf/go-cty/cty"
)

// SchemaFormatVersion is the version of the `vinculum schema` output *format*
// (the shape of the emitted JSON document), independent of the Vinculum
// version that produced it. Bumped on any breaking structural change.
const SchemaFormatVersion = "1"

// ---------------------------------------------------------------------------
// Curated authoring types
// ---------------------------------------------------------------------------

// TypeSchema carries everything the `vinculum schema` command needs about one
// block body: a plain top-level block, one variant of a typed block, or a
// nested sub-block.
//
// Sample drives structural reflection; everything else is curated by hand and
// validated against the reflected structure so it cannot silently drift.
// TypeSchema values are declared next to the decode struct they describe.
type TypeSchema struct {
	// Sample is a zero value (normally a pointer to one) of the gohcl decode
	// struct for this body. Required for top-level blocks and typed variants;
	// ignored for nested sub-blocks, whose structure is reflected from the
	// parent's field type.
	Sample any

	// AlsoSamples lists further decode structs whose attributes and blocks
	// belong to the same body. Some bodies are decoded in more than one pass —
	// a sql client's dialect struct captures the rest with `,remain` and hands
	// it to a second struct holding the dialect-agnostic settings — and the
	// schema has to describe the union.
	AlsoSamples []any

	// Summary is a one-line description, used as a completion item's detail.
	Summary string

	// Doc is richer Markdown used for hover; may be multi-paragraph.
	Doc string

	// DocPage names the hand-written reference page for this type, relative to
	// doc/ — "client-mqtt.md", or "server-vws.md#client-vws" for a type
	// documented in a section of another page.
	//
	// It exists because a generated index has to link somewhere, and a link
	// target synthesized by convention is exactly the kind of thing that rots
	// silently: the page gets renamed and the index still points at it. A test
	// checks that every DocPage names a file that exists and, for a fragment,
	// a heading that is in it.
	//
	// Required for a variant of a typed block under --require-docs; those are
	// what the generated per-type indexes link to.
	DocPage string

	// Attrs holds curated per-attribute metadata, keyed by HCL attribute name.
	// Every key must name an attribute that reflection found.
	Attrs map[string]AttrMeta

	// FreeAttributes marks a body whose attribute names are chosen by the
	// config author rather than fixed by the parser (`const`, an fsm
	// `storage` block). Consumers should not flag an unknown attribute here.
	FreeAttributes bool

	// Blocks holds curated metadata for nested sub-blocks, keyed by HCL block
	// type name. Structure is normally reflected and this only adds docs, so
	// every key must name a sub-block reflection found — unless the entry
	// carries its own Sample, which declares a sub-block of a body the parent
	// parses by hand from a `,remain` body (see fsm's state/event/storage).
	Blocks map[string]TypeSchema

	// Repeatable and Required describe the header of a sub-block declared via
	// its own Sample. They are ignored for reflected sub-blocks, whose field
	// type already says whether the block is a slice, a pointer, or a value.
	Repeatable bool
	Required   bool

	// Variants declares the variant bodies of a typed block whose variants are
	// not driven by a registry (currently only `metric`). Registry-driven
	// blocks (client, server, trigger, condition, wire_format, editor) leave
	// this nil; their variants come from the registry.
	Variants map[string]TypeSchema

	// Constraints are advisory cross-attribute rules that reflection cannot
	// express. Every attribute they name must exist in the reflected body.
	Constraints []Constraint
}

// AttrMeta is curated metadata for a single HCL attribute.
type AttrMeta struct {
	// Summary is a one-line description, shown as a completion item's detail.
	Summary string

	// Doc is richer Markdown for hover.
	Doc string

	// Hint tells a completion provider what kind of value belongs after `=`.
	Hint Hint

	// Context names the shape of `ctx` this attribute's expression is
	// evaluated against, for attributes evaluated at event time. The shape
	// varies per *attribute*, not per block: a receiver's `action` sees
	// ctx.topic/ctx.msg/ctx.fields, while `on_connect` on the same client sees
	// none of them and `on_decode_error` sees the failure instead.
	//
	// Use a stable kebab-case name shared by every attribute with the same
	// shape (e.g. "message", "http-request", "connection"), and describe the
	// shape itself with RegisterContextSchema. Naming a shape that nothing
	// describes — or describing one nothing names — is a reported problem.
	Context string

	// ContextFields are fields this attribute's `ctx` carries in addition to
	// the ones its shape declares. Only an open shape — one whose OpenFields
	// is set — accepts them: `on_decode_error` has the same five fixed fields
	// everywhere, plus identity fields chosen by the receiver, so the mqtt
	// receiver adds `mqtt_topic` where the rabbitmq one adds `routing_key`.
	//
	// A name that the shape already declares, or that a universal field takes,
	// is a reported problem — it is exactly the collision the runtime resolves
	// by dropping the site's field.
	ContextFields []ContextField

	// Enum is the closed set of legal string values, when there is one.
	Enum []string

	// Default is the value the attribute takes when it is omitted, written as
	// it would be written in a config file: `30s`, `true`, `0`. A computed
	// default may be written as the pattern it follows
	// (`vinculum-<name>-<hostname>`).
	//
	// Leave it empty when there is no default worth stating — a required
	// attribute has none, and neither does an optional one whose absence means
	// "do nothing" rather than "do this instead". It is rendered as a literal,
	// so a default that can only be explained in a sentence ("the vinculum
	// topic, verbatim") belongs in Doc instead; a backticked sentence in a
	// Default column reads as a value and is not one.
	//
	// This is curated, like Summary, because the defaults are applied
	// imperatively while processing a block (`keepAlive := 30 * time.Second`)
	// rather than declared anywhere reflection can reach. Prefer stating it
	// here over burying it in Doc prose: a default is the second thing a reader
	// wants after what the attribute means, and a consumer can only put it in
	// its own column if it arrives as its own field.
	Default string

	// Deprecated, when non-empty, marks the attribute deprecated and explains
	// the replacement.
	Deprecated string
}

// WithContextFields returns a copy of meta carrying the given additional `ctx`
// fields. Use it to specialize a shared AttrMeta per site:
//
//	"on_decode_error": cfg.OnDecodeErrorAttr.WithContextFields(
//	    cfg.ContextField{Name: "routing_key", Type: "string", Summary: "…"},
//	),
func (meta AttrMeta) WithContextFields(fields ...ContextField) AttrMeta {
	meta.ContextFields = fields
	return meta
}

// WithDefault returns a copy of meta stating the given default. Use it where a
// shared AttrMeta describes an attribute whose default is genuinely the host's
// choice rather than the attribute's:
//
//	"max_delay": cfg.SomeSharedAttrs["max_delay"].WithDefault("60s"),
//
// Reach for it sparingly. A shared attribute that means different things per
// host is usually a bug wearing a schema, and the fix is to make the hosts
// agree rather than to document that they do not.
func (meta AttrMeta) WithDefault(value string) AttrMeta {
	meta.Default = value
	return meta
}

// ContextSchema describes one shape of `ctx`: what an expression evaluated at
// that kind of site can read. Shapes are named by AttrMeta.Context and
// registered with RegisterContextSchema.
//
// Unlike the rest of the structural layer there is no reflection source — a
// `ctx` is assembled imperatively by an hclutil.EvalContextBuilder chain — so
// Fields is written by hand. Keep it beside the code that builds the context so
// the two are read and edited together. The universal fields (auth, baggage,
// trace_id, span_id) are supplied by the generator and must not be repeated:
// every `ctx` goes through hclutil, which is what puts them there, and there is
// deliberately no way to describe a shape that lacks them. A site that would
// need one is a site that should be building its context through hclutil.
type ContextSchema struct {
	// Summary is a one-line description of when an expression sees this shape.
	Summary string

	// Doc is richer Markdown for hover.
	Doc string

	// Fields are the shape's own fields, in the order worth reading them.
	Fields []ContextField

	// OpenFields marks a shape whose Fields are a floor rather than the whole
	// list: every site carries them, and a site may carry more. Attributes
	// naming an open shape declare their additions with
	// AttrMeta.ContextFields; only an open shape may.
	//
	// `decode-error` is the case — five fields are fixed by
	// MakeDecodeErrorHook and the receiver adds its own transport identity —
	// and a consumer that treated the list as closed would report a correct
	// `ctx.routing_key` as an unknown field.
	OpenFields bool
}

// ContextField is one field of a `ctx` shape.
type ContextField struct {
	// Name is the attribute name, read as `ctx.<name>`.
	Name string

	// Type is the coarse value type, from the same vocabulary as an
	// attribute's: string, number, bool, list, map, or object — plus "dynamic"
	// for a value whose type depends on the message, and "capsule" for an
	// opaque handle passed to a function rather than read directly.
	Type string

	// Summary is a one-line description, shown as a completion item's detail.
	Summary string

	// Doc is richer Markdown for hover.
	Doc string

	// Optional marks a field that is absent from some evaluations of this
	// shape, e.g. ctx.fields on a message that carries no metadata.
	Optional bool
}

// Context field types beyond the attribute vocabulary.
const (
	// CtxTypeDynamic is a value whose type depends on the data, e.g. a
	// decoded message payload.
	CtxTypeDynamic = "dynamic"
	// CtxTypeObject is a value with named attributes of its own.
	CtxTypeObject = "object"
	// CtxTypeCapsule is an opaque handle, passed to a function rather than
	// read directly.
	CtxTypeCapsule = "capsule"
)

// contextSchemas holds every registered `ctx` shape, keyed by the name
// attributes reference it by.
var contextSchemas = map[string]ContextSchema{}

// RegisterContextSchema describes the `ctx` shape that AttrMeta.Context names.
// Call it from an init() in the package that builds the context.
func RegisterContextSchema(name string, cs ContextSchema) {
	contextSchemas[name] = cs
}

// universalContextFields are added to every `ctx` by
// hclutil.EvalContextBuilder.BuildEvalContext, whatever the site. They are
// described once here rather than repeated in every shape.
var universalContextFields = []ContextField{
	{
		Name:    "auth",
		Type:    CtxTypeObject,
		Summary: "The authenticated identity, or null.",
		Doc:     "Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.",
	},
	{
		Name:    "baggage",
		Type:    CtxTypeCapsule,
		Summary: "OpenTelemetry baggage riding with this context.",
		Doc:     "Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See doc/baggage.md.",
	},
	{
		Name:    "trace_id",
		Type:    attrTypeString,
		Summary: "Trace ID of the active span, or empty.",
		Doc:     "Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client \"otlp\"` configured.",
	},
	{
		Name:    "span_id",
		Type:    attrTypeString,
		Summary: "Span ID of the active span, or empty.",
	},
}

// Hint is a member of a closed vocabulary describing what kind of value an
// attribute takes, so a completion provider can offer the right candidates.
// The provider — not Vinculum — knows how to expand each hint.
type Hint string

const (
	// HintExpression is a generic expression, evaluated for its value against
	// the global namespace — const, var, metric, functions — with no `ctx`.
	// Usually that happens once at config load; a few attributes (a computed
	// metric's `value`) are polled at runtime but see the same namespace, and
	// say so in their own docs.
	HintExpression Hint = "expression"
	// HintActionExpression is an action: an expression
	// evaluated at event time rather than config time, largely for its side
	// effects — logging, sending messages, setting variables — with a
	// block-specific `ctx` in scope. In list form each element is evaluated in
	// order and the last value is the result.
	HintActionExpression Hint = "action-expression"
	// HintPredicateExpression is a predicate: evaluated at event time like an
	// action, with the same block-specific `ctx` in scope, but for its boolean
	// value rather than its side effects — an fsm transition `guard`, a
	// trigger's `skip_when`/`stop_when`, a server's `allow_send`. A completion
	// provider should offer `ctx.*` and comparisons here, not the
	// side-effecting functions an action wants.
	HintPredicateExpression Hint = "predicate-expression"
	// HintReactiveExpression is a reactive expression: it is re-evaluated for
	// its value automatically whenever any watchable it references changes — a
	// var, metric, condition, or fsm — rather than once at config time or in
	// response to an event. It has no `ctx`, and what it references is what
	// makes it fire, so a completion provider should offer watchable
	// references here. See config.ReactiveExpr.
	HintReactiveExpression Hint = "reactive-expression"
	// HintTransformPipeline is a list of transform functions applied in order
	// to each message before delivery — `add_topic_prefix("out/")`, `jq(...)`,
	// `if_topic_prefix(...)`, `stop()`, and so on.
	//
	// The transform functions are a DSL, not part of the general expression
	// language: they exist only in the eval context of attributes that expect
	// a transform list. A completion provider should offer those functions
	// here, and only those.
	HintTransformPipeline Hint = "transform-pipeline"
	// HintBusRef is a reference to a bus specifically — a slot that resolves
	// an event bus and nothing else. For the much more common slot that takes
	// anything able to receive messages, use HintSubscriberRef.
	HintBusRef Hint = "bus-ref"
	// HintSubscriberRef is a reference to anything that can receive messages:
	// any capsule whose value implements bus.Subscriber. That includes buses
	// (`bus.<name>`) but also FSMs, subscriber-implementing servers such as
	// `server "vws"` and `server "websocket"`, and clients that publish
	// outbound. A completion provider should offer all of them, not just
	// `bus.*`.
	HintSubscriberRef Hint = "subscriber-ref"
	// HintClientRef is a `client.<name>` reference.
	HintClientRef Hint = "client-ref"
	// HintServerRef is a `server.<name>` reference.
	HintServerRef Hint = "server-ref"
	// HintMetricRef is a `metric.<name>` reference.
	HintMetricRef Hint = "metric-ref"
	// HintTracingRef is a reference to a tracing backend: a `client "otlp"`
	// block. Narrower than HintClientRef, which would offer every client.
	HintTracingRef Hint = "tracing-ref"
	// HintMetricsRef is a reference to a metrics backend: a `server "metrics"`
	// or a `client "otlp"` block. Narrower than HintServerRef/HintClientRef,
	// and it spans both namespaces, which neither of those can express.
	HintMetricsRef Hint = "metrics-ref"
	// HintVarRef is a `var.<name>` reference.
	HintVarRef Hint = "var-ref"
	// HintTopicPattern is an MQTT-style topic pattern (`+`, `#`).
	HintTopicPattern Hint = "topic-pattern"
	// HintCronExpr is a cron expression.
	HintCronExpr Hint = "cron-expr"
	// HintDuration is a Go duration literal ("5s", "1h").
	HintDuration Hint = "duration"
	// HintURL is a URL.
	HintURL Hint = "url"
	// HintListenAddr is a `:port` or `host:port` listen address.
	HintListenAddr Hint = "listen-addr"
	// HintBool is `true` / `false`.
	HintBool Hint = "bool"
)

// ConstraintKind identifies a cross-attribute rule.
type ConstraintKind string

const (
	// ConstraintMutuallyExclusive means at most one of the attributes may be set.
	ConstraintMutuallyExclusive ConstraintKind = "mutually_exclusive"
	// ConstraintAtLeastOneOf means at least one of the attributes must be set.
	ConstraintAtLeastOneOf ConstraintKind = "at_least_one_of"
	// ConstraintRequiredTogether means the attributes must all be set, or none.
	ConstraintRequiredTogether ConstraintKind = "required_together"
	// ConstraintRequires means the first attribute implies all the others.
	ConstraintRequires ConstraintKind = "requires"
)

// Constraint is an advisory cross-attribute rule. Editors surface constraints
// as documentation or diagnostics; the Go decode/validate path remains the
// authority.
type Constraint struct {
	Kind       ConstraintKind `json:"kind"`
	Attributes []string       `json:"attributes"`
	Message    string         `json:"message"`
}

// WithMessage returns a copy of the constraint with a custom message,
// replacing the generated default.
func (c Constraint) WithMessage(msg string) Constraint {
	c.Message = msg
	return c
}

// MutuallyExclusive declares that at most one of the named attributes may be set.
func MutuallyExclusive(attrs ...string) Constraint {
	return Constraint{
		Kind:       ConstraintMutuallyExclusive,
		Attributes: attrs,
		Message:    fmt.Sprintf("Specify at most one of %s.", joinAttrs(attrs, "or")),
	}
}

// AtLeastOneOf declares that at least one of the named attributes must be set.
func AtLeastOneOf(attrs ...string) Constraint {
	return Constraint{
		Kind:       ConstraintAtLeastOneOf,
		Attributes: attrs,
		Message:    fmt.Sprintf("Specify at least one of %s.", joinAttrs(attrs, "or")),
	}
}

// RequiredTogether declares that the named attributes must all be set, or none.
func RequiredTogether(attrs ...string) Constraint {
	return Constraint{
		Kind:       ConstraintRequiredTogether,
		Attributes: attrs,
		Message:    fmt.Sprintf("%s must be specified together.", joinAttrs(attrs, "and")),
	}
}

// Requires declares that setting attr implies setting all of requires.
func Requires(attr string, requires ...string) Constraint {
	return Constraint{
		Kind:       ConstraintRequires,
		Attributes: append([]string{attr}, requires...),
		Message:    fmt.Sprintf("%s requires %s.", attr, joinAttrs(requires, "and")),
	}
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// RegisterOption customizes a Register*Type call. Supplying a schema is
// optional and additive: a type registered without one still appears in the
// output, flagged "undocumented", so coverage gaps are visible rather than
// invisible.
type RegisterOption func(*registerOptions)

type registerOptions struct {
	schema   *TypeSchema
	variants map[string]TypeSchema
}

func applyRegisterOptions(opts []RegisterOption) registerOptions {
	var o registerOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithSchema attaches the machine-readable schema of the type being
// registered. It is called from inside the defining package's init(), where
// the (usually unexported) decode struct is in scope:
//
//	cfg.RegisterClientType("http", process, cfg.WithSchema(httpClientSchema))
func WithSchema(ts TypeSchema) RegisterOption {
	return func(o *registerOptions) {
		o.schema = &ts
	}
}

// WithVariantSchemas attaches schemas for a set of type names registered
// together. It exists for RegisterConditionalTriggerType, whose factory cannot
// be invoked without a *Config and so cannot reveal its type names any other
// way.
func WithVariantSchemas(schemas map[string]TypeSchema) RegisterOption {
	return func(o *registerOptions) {
		o.variants = schemas
	}
}

// SchemaProvider is an optional interface a BlockHandler may implement to
// contribute its block's schema. Handlers that don't are emitted
// "undocumented", which lets the schema land block by block.
type SchemaProvider interface {
	Schema() TypeSchema
}

var (
	// typeSchemas holds curated schemas for registry-driven typed blocks,
	// keyed by block type ("client") then variant key ("http").
	typeSchemas = map[string]map[string]TypeSchema{}

	// conditionalTypes records variant keys whose availability depends on
	// config state, so they can be flagged in the output.
	conditionalTypes = map[string]map[string]bool{}

	// topLevelBlockSchemas holds schemas for top-level blocks with no
	// BlockHandler to carry them (`function`, `jq`, `editor`), registered via
	// RegisterBlockSchema.
	topLevelBlockSchemas = map[string]TypeSchema{}

	// sharedBlockSchemas holds curated metadata for sub-block structs shared
	// across many parents (tls, auth, reconnect, baggage), keyed by Go type.
	sharedBlockSchemas = map[reflect.Type]TypeSchema{}
)

// registerTypeSchema records the schema supplied for one variant of a typed
// block, if any.
func registerTypeSchema(blockType, typeName string, opts []RegisterOption) {
	o := applyRegisterOptions(opts)
	if o.schema == nil {
		return
	}
	setTypeSchema(blockType, typeName, *o.schema)
}

// registerConditionalTypeSchemas records the schemas of a set of conditional
// types registered together, flagging each as conditional.
func registerConditionalTypeSchemas(blockType string, opts []RegisterOption) {
	o := applyRegisterOptions(opts)
	for typeName, ts := range o.variants {
		setTypeSchema(blockType, typeName, ts)
		if conditionalTypes[blockType] == nil {
			conditionalTypes[blockType] = map[string]bool{}
		}
		conditionalTypes[blockType][typeName] = true
	}
}

func setTypeSchema(blockType, typeName string, ts TypeSchema) {
	if typeSchemas[blockType] == nil {
		typeSchemas[blockType] = map[string]TypeSchema{}
	}
	typeSchemas[blockType][typeName] = ts
}

// RegisterBlockSchema records the schema of a top-level block that has no
// BlockHandler to carry it — the blocks extracted early in Build() such as
// `function` and `jq`. Blocks with a handler implement SchemaProvider instead.
func RegisterBlockSchema(blockType string, ts TypeSchema) {
	topLevelBlockSchemas[blockType] = ts
}

// RegisterSharedBlockSchema records curated metadata for a sub-block struct
// used by many parent blocks (e.g. cfg.TLSConfig), so it is documented once
// rather than in every host. sample is a zero value of the struct, normally a
// pointer to one. A parent that curates the same sub-block wins over this.
func RegisterSharedBlockSchema(sample any, ts TypeSchema) {
	ty := reflect.TypeOf(sample)
	for ty != nil && ty.Kind() == reflect.Ptr {
		ty = ty.Elem()
	}
	if ty == nil || ty.Kind() != reflect.Struct {
		panic(fmt.Sprintf("RegisterSharedBlockSchema: sample must be a struct or pointer to one, got %T", sample))
	}
	sharedBlockSchemas[ty] = ts
}

// joinAttrs renders a list of attribute names as prose: "a", "a and b",
// "a, b, or c".
func joinAttrs(attrs []string, conj string) string {
	switch len(attrs) {
	case 0:
		return ""
	case 1:
		return attrs[0]
	case 2:
		return attrs[0] + " " + conj + " " + attrs[1]
	default:
		return strings.Join(attrs[:len(attrs)-1], ", ") + ", " + conj + " " + attrs[len(attrs)-1]
	}
}

// ---------------------------------------------------------------------------
// Structural reflection
// ---------------------------------------------------------------------------

// Coarse attribute value types emitted in the schema document. They are hints
// for editors, not contracts: most VCL attributes are hcl.Expression and accept
// any expression evaluating to the right kind of value.
const (
	attrTypeExpression = "expression"
	attrTypeString     = "string"
	attrTypeBool       = "bool"
	attrTypeNumber     = "number"
	attrTypeList       = "list"
	attrTypeMap        = "map"
)

// reflectedBody is the structural skeleton of one block body, derived from the
// gohcl struct tags the parser already decodes. Attributes and blocks are kept
// in field-declaration order, which reads better in docs and completions than
// the alphabetical order gohcl.ImpliedBodySchema uses internally.
type reflectedBody struct {
	Attrs  []reflectedAttr
	Blocks []reflectedBlock
}

// reflectedAttr is one HCL attribute discovered by reflection.
type reflectedAttr struct {
	Name     string
	Required bool
	Type     string
	GoType   reflect.Type
}

// reflectedBlock is one nested HCL block discovered by reflection.
type reflectedBlock struct {
	Name       string
	Labels     []string
	Unnamed    []string // labels whose tag gave no name, in declaration order
	Repeatable bool     // field was a slice
	Required   bool     // field was a plain struct (not a pointer or slice)
	GoType     reflect.Type
	Body       *reflectedBody
}

// attr returns the named attribute, or nil.
func (b *reflectedBody) attr(name string) *reflectedAttr {
	for i := range b.Attrs {
		if b.Attrs[i].Name == name {
			return &b.Attrs[i]
		}
	}
	return nil
}

// block returns the named nested block, or nil.
func (b *reflectedBody) block(name string) *reflectedBlock {
	for i := range b.Blocks {
		if b.Blocks[i].Name == name {
			return &b.Blocks[i]
		}
	}
	return nil
}

var (
	hclExpressionType = reflect.TypeOf((*hcl.Expression)(nil)).Elem()
	hclBodyType       = reflect.TypeOf((*hcl.Body)(nil)).Elem()
	ctyValueType      = reflect.TypeOf(cty.Value{})
	ctyTypeType       = reflect.TypeOf(cty.Type{})
)

// reflectSample derives the structural skeleton of the body decoded into the
// given sample value, which must be a struct or a pointer to one.
func reflectSample(sample any) (*reflectedBody, error) {
	if sample == nil {
		return nil, fmt.Errorf("no sample value")
	}
	ty := reflect.TypeOf(sample)
	for ty.Kind() == reflect.Ptr {
		ty = ty.Elem()
	}
	if ty.Kind() != reflect.Struct {
		return nil, fmt.Errorf("sample must be a struct or pointer to one, got %s", reflect.TypeOf(sample))
	}
	return reflectBodyType(ty, nil)
}

// reflectBodyType walks the hcl-tagged fields of a struct type. stack holds the
// struct types currently being walked, so a recursive block definition is
// reported rather than looped on forever.
func reflectBodyType(ty reflect.Type, stack []reflect.Type) (*reflectedBody, error) {
	for _, seen := range stack {
		if seen == ty {
			return nil, fmt.Errorf("recursive block type %s", ty)
		}
	}
	stack = append(stack, ty)

	body := &reflectedBody{}
	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		tag := field.Tag.Get("hcl")
		if tag == "" {
			// Untagged fields (including embedded structs) are invisible to
			// gohcl, so they are invisible here too.
			continue
		}
		name, kind, _ := strings.Cut(tag, ",")
		if kind == "" {
			kind = "attr"
		}

		switch kind {
		case "attr", "optional":
			body.Attrs = append(body.Attrs, reflectedAttr{
				Name: name,
				// Required is the author's intent: an attribute whose tag does
				// not say `,optional`. This deliberately differs from
				// gohcl.ImpliedBodySchema, which additionally reports
				// hcl.Expression and pointer fields as optional because it
				// signals their absence with a null rather than an error —
				// leaving the block's own processor to enforce the
				// requirement and report it.
				Required: kind == "attr",
				Type:     coarseAttrType(field.Type),
				GoType:   field.Type,
			})

		case "block":
			fty := field.Type
			repeatable := false
			if fty.Kind() == reflect.Slice {
				repeatable = true
				fty = fty.Elem()
			}
			optional := repeatable
			if fty.Kind() == reflect.Ptr {
				optional = true
				fty = fty.Elem()
			}
			if fty.Kind() != reflect.Struct {
				return nil, fmt.Errorf("block field %s.%s must be a struct, got %s", ty.Name(), field.Name, field.Type)
			}
			nested, err := reflectBodyType(fty, stack)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", ty.Name(), field.Name, err)
			}
			labels, unnamed := blockLabels(fty)
			body.Blocks = append(body.Blocks, reflectedBlock{
				Name:       name,
				Labels:     labels,
				Unnamed:    unnamed,
				Repeatable: repeatable,
				Required:   !optional,
				GoType:     fty,
				Body:       nested,
			})

		case "label", "remain", "body", "def_range", "type_range",
			"label_range", "attr_range", "attr_name_range", "attr_value_range":
			// Structural plumbing, not part of the authored config surface.

		default:
			return nil, fmt.Errorf("invalid hcl field tag kind %q on %s.%s", kind, ty.Name(), field.Name)
		}
	}
	return body, nil
}

// labelsOrEmpty returns labels, or an empty slice, so `labels` marshals as
// `[]` rather than `null` for a label-less block.
func labelsOrEmpty(labels []string) []string {
	if labels == nil {
		return []string{}
	}
	return labels
}

// blockLabels returns the label names of a nested block's struct type, in
// declaration order, along with the names of any labels whose tag left the
// name empty.
//
// gohcl permits `hcl:",label"` and takes the label name straight from the tag,
// so an unnamed one makes HCL's own diagnostic read "Missing  for match; All
// match blocks must have 1 labels ()". Falling back to the field name keeps
// the schema readable, but the fallback is a guess at a name the author never
// wrote — so it is reported rather than applied silently.
func blockLabels(ty reflect.Type) (labels, unnamed []string) {
	for i := 0; i < ty.NumField(); i++ {
		tag := ty.Field(i).Tag.Get("hcl")
		if tag == "" {
			continue
		}
		name, kind, _ := strings.Cut(tag, ",")
		if kind == "label" {
			if name == "" {
				name = strings.ToLower(ty.Field(i).Name)
				unnamed = append(unnamed, name)
			}
			labels = append(labels, name)
		}
	}
	return labels, unnamed
}

// coarseAttrType maps a Go field type to the coarse value type emitted in the
// schema document.
func coarseAttrType(ty reflect.Type) string {
	switch ty {
	case hclExpressionType, hclBodyType, ctyValueType, ctyTypeType:
		return attrTypeExpression
	}
	if ty.Implements(hclExpressionType) {
		return attrTypeExpression
	}
	for ty.Kind() == reflect.Ptr {
		ty = ty.Elem()
	}
	switch ty.Kind() {
	case reflect.String:
		return attrTypeString
	case reflect.Bool:
		return attrTypeBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return attrTypeNumber
	case reflect.Slice, reflect.Array:
		return attrTypeList
	case reflect.Map:
		return attrTypeMap
	default:
		return attrTypeExpression
	}
}

// ---------------------------------------------------------------------------
// Emitted document
// ---------------------------------------------------------------------------

// SchemaDocument is the root of the `vinculum schema` JSON output.
type SchemaDocument struct {
	// SchemaVersion is the version of the output format itself.
	SchemaVersion string `json:"schemaVersion"`
	// VinculumVersion is the version of the binary that produced the document.
	VinculumVersion string `json:"vinculumVersion"`
	// Contexts maps a `ctx` shape name to its description. An attribute's
	// `context` field names one of these.
	Contexts map[string]*SchemaContext `json:"contexts"`
	// Plugins lists the registry entries plugins contributed to this document,
	// e.g. "client.acme". Absent when no plugins were loaded, so its presence
	// is what tells a consumer the document describes more than a stock binary.
	// The generator does not load plugins itself; the caller sets this.
	Plugins []string `json:"plugins,omitempty"`
	// Blocks maps top-level block type name to its description.
	Blocks map[string]*SchemaBlock `json:"blocks"`
}

// SchemaBlock describes one top-level block type. It has two shapes: a plain
// block inlines a single Body, while a typed block (one whose first label
// selects a variant, e.g. `client "http"`) carries a map of variant bodies
// instead.
type SchemaBlock struct {
	// Labels are the block's label names, from blockSchema.
	Labels []string
	// VariantLabel names the label that selects the variant; empty for plain
	// blocks. Always the first label in v1.
	VariantLabel string
	// Summary and Doc describe the block type as a whole.
	Summary string
	Doc     string
	// DocPage is the hand-written reference page for the block type.
	DocPage string
	// Undocumented is true when no curated schema was registered for the block.
	Undocumented bool
	// Body is the single body of a plain block; nil for typed blocks.
	Body *SchemaBody
	// Variants maps variant key to body; nil for plain blocks.
	Variants map[string]*SchemaBody
}

// SchemaBody describes the contents of one block body: a plain block, one
// variant of a typed block, or a nested sub-block.
type SchemaBody struct {
	Summary string `json:"summary,omitempty"`
	Doc     string `json:"doc,omitempty"`
	// DocPage is the hand-written reference page for this type, relative to
	// doc/. A consumer generating an index links to it.
	DocPage string `json:"docPage,omitempty"`
	// Undocumented is true when no curated schema was registered for this body.
	Undocumented bool `json:"undocumented,omitempty"`
	// Conditional is true for a variant whose availability depends on config
	// state (see RegisterConditionalTriggerType).
	Conditional bool `json:"conditional,omitempty"`
	// FreeAttributes is true when attribute names here are chosen by the
	// config author rather than fixed by the parser, so an unknown name is
	// not an error.
	FreeAttributes bool                          `json:"freeAttributes,omitempty"`
	Attributes     []*SchemaAttr                 `json:"attributes"`
	Blocks         map[string]*SchemaNestedBlock `json:"blocks"`
	Constraints    []Constraint                  `json:"constraints"`
}

// SchemaAttr describes one HCL attribute.
type SchemaAttr struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	// Type is the coarse value type: expression, string, bool, number, list,
	// or map.
	Type    string `json:"type"`
	Summary string `json:"summary,omitempty"`
	Doc     string `json:"doc,omitempty"`
	Hint    Hint   `json:"hint,omitempty"`
	// Context names the `ctx` shape this attribute's expression sees; look it
	// up in the document's top-level `contexts`.
	Context string `json:"context,omitempty"`
	// ContextFields are fields this site adds to that shape, present only for
	// an open shape. Read them as appended to the shape's own fields.
	ContextFields []*SchemaContextField `json:"contextFields,omitempty"`
	Enum          []string              `json:"enum,omitempty"`
	// Default is the value used when the attribute is omitted, written as it
	// would be written in a config file. Absent when there is no default worth
	// stating rather than when the zero value applies.
	Default    string `json:"default,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
}

// SchemaContext describes one `ctx` shape, named by an attribute's `context`.
type SchemaContext struct {
	Summary string `json:"summary,omitempty"`
	Doc     string `json:"doc,omitempty"`
	// Fields are the shape's own fields first, then the universal ones every
	// `ctx` carries.
	Fields []*SchemaContextField `json:"fields"`
	// OpenFields is true when Fields is a floor rather than the whole list: a
	// site may carry more, named by its attribute's `contextFields`. Treat an
	// unlisted field as unknown-but-possible rather than as an error.
	OpenFields bool `json:"openFields,omitempty"`
}

// SchemaContextField describes one field readable as `ctx.<name>`.
type SchemaContextField struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Summary string `json:"summary,omitempty"`
	Doc     string `json:"doc,omitempty"`
	// Optional is true when the field is absent from some evaluations.
	Optional bool `json:"optional,omitempty"`
	// Universal is true for a field every `ctx` carries whatever the site, so
	// a consumer can group or fold them away.
	Universal bool `json:"universal,omitempty"`
}

// SchemaNestedBlock describes a sub-block: its header plus its body.
type SchemaNestedBlock struct {
	Labels []string `json:"labels"`
	// Repeatable is true when the block may appear any number of times.
	Repeatable bool `json:"repeatable"`
	// Required is true when exactly one instance must be present.
	Required bool `json:"required"`
	SchemaBody
}

// newSchemaBody returns a body with its collections initialized, so the
// emitted JSON always carries `attributes`, `blocks`, and `constraints`.
func newSchemaBody() *SchemaBody {
	return &SchemaBody{
		Attributes:  []*SchemaAttr{},
		Blocks:      map[string]*SchemaNestedBlock{},
		Constraints: []Constraint{},
	}
}

// plainBlockJSON is the emitted shape of a block with a single body.
type plainBlockJSON struct {
	Labels       []string `json:"labels"`
	Summary      string   `json:"summary,omitempty"`
	Doc          string   `json:"doc,omitempty"`
	DocPage      string   `json:"docPage,omitempty"`
	Undocumented bool     `json:"undocumented,omitempty"`
	SchemaBody
}

// typedBlockJSON is the emitted shape of a block whose first label selects a
// variant.
type typedBlockJSON struct {
	Labels       []string               `json:"labels"`
	VariantLabel string                 `json:"variantLabel"`
	Summary      string                 `json:"summary,omitempty"`
	Doc          string                 `json:"doc,omitempty"`
	DocPage      string                 `json:"docPage,omitempty"`
	Undocumented bool                   `json:"undocumented,omitempty"`
	Variants     map[string]*SchemaBody `json:"variants"`
}

// MarshalJSON emits the plain or typed shape depending on whether the block
// has a variant dimension.
func (b SchemaBlock) MarshalJSON() ([]byte, error) {
	if b.VariantLabel != "" {
		variants := b.Variants
		if variants == nil {
			variants = map[string]*SchemaBody{}
		}
		return json.Marshal(typedBlockJSON{
			Labels:       b.labels(),
			VariantLabel: b.VariantLabel,
			Summary:      b.Summary,
			Doc:          b.Doc,
			DocPage:      b.DocPage,
			Undocumented: b.Undocumented,
			Variants:     variants,
		})
	}

	body := b.Body
	if body == nil {
		body = newSchemaBody()
	}
	// A plain block's description may be curated at either level — the block
	// carries it, or the single body it inlines does. The block-level fields
	// shadow the embedded body's in the emitted JSON, so fold them together.
	return json.Marshal(plainBlockJSON{
		Labels:       b.labels(),
		Summary:      firstNonEmptyString(b.Summary, body.Summary),
		Doc:          firstNonEmptyString(b.Doc, body.Doc),
		DocPage:      firstNonEmptyString(b.DocPage, body.DocPage),
		Undocumented: b.Undocumented || body.Undocumented,
		SchemaBody:   *body,
	})
}

// labels returns the block's labels, never nil, so `labels` marshals as `[]`
// for label-less blocks like `const` rather than `null`.
func (b SchemaBlock) labels() []string {
	if b.Labels == nil {
		return []string{}
	}
	return b.Labels
}

// firstNonEmptyString returns the first non-empty string, or "".
func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Building: reflected structure + curated metadata
// ---------------------------------------------------------------------------

// SchemaGenOptions controls schema generation.
type SchemaGenOptions struct {
	// Strict makes curation problems (metadata referencing an attribute or
	// block that does not exist) fatal for the caller. Problems are always
	// collected; Strict only decides whether they fail the command.
	Strict bool
	// RequireDocs additionally reports every block, variant, nested block, and
	// attribute that carries no Summary.
	RequireDocs bool
}

// schemaBuilder merges reflected structure with curated metadata, collecting
// any curation problems it finds along the way.
type schemaBuilder struct {
	opts     SchemaGenOptions
	problems []error
}

// problemf records a curation problem.
func (b *schemaBuilder) problemf(format string, args ...any) {
	b.problems = append(b.problems, fmt.Errorf(format, args...))
}

// reportUnnamedLabels records the labels blockLabels had to name for itself.
// The name it guesses is only ever documentation, but the missing tag name is
// not: HCL uses it verbatim in its own "missing label" diagnostic.
func (b *schemaBuilder) reportUnnamedLabels(path string, unnamed []string) {
	for _, label := range unnamed {
		b.problemf("%s: label %q has no name in its hcl tag; write `hcl:%q` so HCL's own diagnostics can name it", path, label, label+",label")
	}
}

// bodyFromSample reflects the sample in ts and merges ts's curated metadata
// into the result. A missing or unreflectable sample yields an empty body and
// a recorded problem.
func (b *schemaBuilder) bodyFromSample(path string, ts TypeSchema) *SchemaBody {
	rb, err := reflectSample(ts.Sample)
	if err != nil {
		b.problemf("%s: %v", path, err)
		return newSchemaBody()
	}
	for _, also := range ts.AlsoSamples {
		extra, err := reflectSample(also)
		if err != nil {
			b.problemf("%s: %v", path, err)
			continue
		}
		rb = mergeReflectedBodies(rb, extra)
	}
	return b.mergeBody(path, rb, ts)
}

// mergeReflectedBodies returns the union of two reflected bodies, keeping
// base's entry when both declare the same attribute or block.
func mergeReflectedBodies(base, extra *reflectedBody) *reflectedBody {
	merged := &reflectedBody{
		Attrs:  append([]reflectedAttr(nil), base.Attrs...),
		Blocks: append([]reflectedBlock(nil), base.Blocks...),
	}
	for _, attr := range extra.Attrs {
		if merged.attr(attr.Name) == nil {
			merged.Attrs = append(merged.Attrs, attr)
		}
	}
	for _, blk := range extra.Blocks {
		if merged.block(blk.Name) == nil {
			merged.Blocks = append(merged.Blocks, blk)
		}
	}
	return merged
}

// mergeBody combines a reflected body with the curated metadata describing it.
// The reflected structure is authoritative: curation that names something the
// structure does not contain is dropped and reported.
func (b *schemaBuilder) mergeBody(path string, rb *reflectedBody, ts TypeSchema) *SchemaBody {
	body := newSchemaBody()
	body.Summary = ts.Summary
	body.Doc = ts.Doc
	body.DocPage = ts.DocPage
	body.FreeAttributes = ts.FreeAttributes

	if b.opts.RequireDocs && ts.Summary == "" {
		b.problemf("%s: missing summary", path)
	}

	for _, ra := range rb.Attrs {
		meta := ts.Attrs[ra.Name]
		if b.opts.RequireDocs && meta.Summary == "" {
			b.problemf("%s.%s: missing summary", path, ra.Name)
		}
		// A required attribute cannot be omitted, so a default for it is
		// either a lie or a sign the attribute is not really required.
		if meta.Default != "" && ra.Required {
			b.problemf("%s.%s: required attributes have no default, but one is documented (%q)",
				path, ra.Name, meta.Default)
		}
		body.Attributes = append(body.Attributes, &SchemaAttr{
			Name:          ra.Name,
			Required:      ra.Required,
			Type:          ra.Type,
			Summary:       meta.Summary,
			Doc:           meta.Doc,
			Hint:          meta.Hint,
			Context:       meta.Context,
			ContextFields: schemaContextFields(meta.ContextFields),
			Enum:          meta.Enum,
			Default:       meta.Default,
			Deprecated:    meta.Deprecated,
		})
	}
	for _, name := range sortedKeys(ts.Attrs) {
		if rb.attr(name) == nil {
			b.problemf("%s: documented attribute %q does not exist", path, name)
		}
	}

	for _, rblk := range rb.Blocks {
		nestedTS := ts.Blocks[rblk.Name]
		// The parent struct already says how this sub-block may appear, so
		// curation restating it is either redundant or a disagreement the
		// reflected structure would silently win. Say so either way.
		if nestedTS.Repeatable || nestedTS.Required {
			b.problemf("%s.%s: repeatable/required come from the parent struct and cannot be curated", path, rblk.Name)
		}
		b.reportUnnamedLabels(path+"."+rblk.Name, rblk.Unnamed)
		if shared, ok := sharedBlockSchemas[rblk.GoType]; ok {
			// A sub-block struct shared by many parents (tls, auth, ...) is
			// documented once; anything the parent says about it wins.
			nestedTS = nestedTS.withDefaultsFrom(shared)
		}
		nestedBody := b.mergeBody(path+"."+rblk.Name, rblk.Body, nestedTS)
		body.Blocks[rblk.Name] = &SchemaNestedBlock{
			Labels:     labelsOrEmpty(rblk.Labels),
			Repeatable: rblk.Repeatable,
			Required:   rblk.Required,
			SchemaBody: *nestedBody,
		}
	}
	for _, name := range sortedKeys(ts.Blocks) {
		if rb.block(name) != nil {
			continue
		}
		// A curated entry with its own Sample declares a sub-block the parent
		// struct cannot contain, because the parent parses it by hand out of a
		// `,remain` body. Anything else is stale curation.
		declared := ts.Blocks[name]
		if declared.Sample == nil {
			b.problemf("%s: documented block %q does not exist", path, name)
			continue
		}
		declaredRB, err := reflectSample(declared.Sample)
		if err != nil {
			b.problemf("%s.%s: %v", path, name, err)
			continue
		}
		labels, unnamed := blockLabels(reflect.Indirect(reflect.ValueOf(declared.Sample)).Type())
		b.reportUnnamedLabels(path+"."+name, unnamed)
		body.Blocks[name] = &SchemaNestedBlock{
			Labels:     labelsOrEmpty(labels),
			Repeatable: declared.Repeatable,
			Required:   declared.Required,
			SchemaBody: *b.mergeBody(path+"."+name, declaredRB, declared),
		}
	}

	for _, c := range ts.Constraints {
		for _, name := range c.Attributes {
			if rb.attr(name) == nil {
				b.problemf("%s: constraint %s references unknown attribute %q", path, c.Kind, name)
			}
		}
		body.Constraints = append(body.Constraints, c)
	}

	return body
}

// withDefaultsFrom layers ts over base, filling anything ts leaves empty. It
// lets a shared sub-block definition supply the baseline documentation while a
// particular parent overrides or extends it.
func (ts TypeSchema) withDefaultsFrom(base TypeSchema) TypeSchema {
	out := ts
	out.Summary = firstNonEmptyString(ts.Summary, base.Summary)
	out.Doc = firstNonEmptyString(ts.Doc, base.Doc)

	if len(base.Attrs) > 0 {
		attrs := make(map[string]AttrMeta, len(base.Attrs)+len(ts.Attrs))
		for name, meta := range base.Attrs {
			attrs[name] = meta
		}
		for name, meta := range ts.Attrs {
			attrs[name] = meta
		}
		out.Attrs = attrs
	}

	if len(base.Blocks) > 0 {
		blocks := make(map[string]TypeSchema, len(base.Blocks)+len(ts.Blocks))
		for name, nested := range base.Blocks {
			blocks[name] = nested
		}
		for name, nested := range ts.Blocks {
			blocks[name] = nested.withDefaultsFrom(base.Blocks[name])
		}
		out.Blocks = blocks
	}

	if len(out.Constraints) == 0 {
		out.Constraints = base.Constraints
	}
	return out
}

// ---------------------------------------------------------------------------
// Generating the whole document
// ---------------------------------------------------------------------------

// GenerateSchema describes the entire VCL block language: every top-level
// block from blockSchema, every variant of a typed block from the registry
// that drives its parsing, and the curated metadata registered alongside them.
//
// It reflects the in-tree registries as populated by init(), so it describes a
// stock binary; plugins are not loaded. Curation problems are always returned;
// opts.Strict decides whether the caller treats them as fatal.
func GenerateSchema(opts SchemaGenOptions) (*SchemaDocument, []error) {
	b := &schemaBuilder{opts: opts}
	doc := &SchemaDocument{
		SchemaVersion:   SchemaFormatVersion,
		VinculumVersion: version.Version,
		Blocks:          make(map[string]*SchemaBlock, len(blockSchema)),
	}

	handlers := GetBlockHandlers()
	for _, header := range blockSchema {
		doc.Blocks[header.Type] = b.topLevelBlock(header, handlers)
	}
	doc.Contexts = b.contexts(doc)
	return doc, b.problems
}

// contexts describes every `ctx` shape the document's attributes name.
//
// The shapes have no reflection source, so what can be checked is the closure
// between the two halves: a name with no shape leaves a consumer holding a
// label it cannot expand, and a shape no attribute names is curation for a
// site that no longer exists.
func (b *schemaBuilder) contexts(doc *SchemaDocument) map[string]*SchemaContext {
	named := map[string][]contextRef{} // shape name -> attributes naming it
	for _, blockType := range sortedKeys(doc.Blocks) {
		block := doc.Blocks[blockType]
		if block.Body != nil {
			collectContextNames(blockType, block.Body, named)
		}
		for _, variant := range sortedKeys(block.Variants) {
			collectContextNames(blockType+" "+variant, block.Variants[variant], named)
		}
	}

	out := make(map[string]*SchemaContext, len(named))
	for _, name := range sortedKeys(named) {
		cs, ok := contextSchemas[name]
		if !ok {
			b.problemf("context %q is named by %s but no shape is registered for it",
				name, named[name][0].path)
			continue
		}
		out[name] = b.contextShape(name, cs)
		for _, ref := range named[name] {
			b.checkContextFields(name, out[name], ref)
		}
	}
	for _, name := range sortedKeys(contextSchemas) {
		if _, ok := named[name]; !ok {
			b.problemf("context %q is described but no attribute names it", name)
		}
	}
	return out
}

func (b *schemaBuilder) contextShape(name string, cs ContextSchema) *SchemaContext {
	if b.opts.RequireDocs && cs.Summary == "" {
		b.problemf("context %s: missing summary", name)
	}
	out := &SchemaContext{
		Summary:    cs.Summary,
		Doc:        cs.Doc,
		OpenFields: cs.OpenFields,
		Fields:     make([]*SchemaContextField, 0, len(cs.Fields)+len(universalContextFields)),
	}
	seen := map[string]bool{}
	for _, f := range cs.Fields {
		if b.opts.RequireDocs && f.Summary == "" {
			b.problemf("context %s.%s: missing summary", name, f.Name)
		}
		if seen[f.Name] {
			b.problemf("context %s: duplicate field %q", name, f.Name)
			continue
		}
		seen[f.Name] = true
		out.Fields = append(out.Fields, &SchemaContextField{
			Name:     f.Name,
			Type:     f.Type,
			Summary:  f.Summary,
			Doc:      f.Doc,
			Optional: f.Optional,
		})
	}
	for _, f := range universalContextFields {
		if seen[f.Name] {
			b.problemf("context %s: %q is a universal field and must not be redeclared", name, f.Name)
			continue
		}
		out.Fields = append(out.Fields, &SchemaContextField{
			Name:      f.Name,
			Type:      f.Type,
			Summary:   f.Summary,
			Doc:       f.Doc,
			Universal: true,
		})
	}
	return out
}

// contextRef is one attribute naming a `ctx` shape, and where it was found.
type contextRef struct {
	path string
	attr *SchemaAttr
}

// collectContextNames records every context name an attribute in body refers
// to, at any depth, along with where it was found.
func collectContextNames(path string, body *SchemaBody, into map[string][]contextRef) {
	for _, attr := range body.Attributes {
		if attr.Context != "" {
			into[attr.Context] = append(into[attr.Context], contextRef{path + "." + attr.Name, attr})
		}
	}
	for _, name := range sortedKeys(body.Blocks) {
		collectContextNames(path+"."+name, &body.Blocks[name].SchemaBody, into)
	}
}

// checkContextFields validates one site's additions to an open shape. The
// collisions it reports are the ones the runtime resolves by dropping the
// site's field, so the field would be documented and then never appear.
func (b *schemaBuilder) checkContextFields(name string, shape *SchemaContext, ref contextRef) {
	if len(ref.attr.ContextFields) == 0 {
		return
	}
	if !shape.OpenFields {
		b.problemf("%s: adds fields to context %q, whose field list is closed", ref.path, name)
		ref.attr.ContextFields = nil
		return
	}
	declared := make(map[string]bool, len(shape.Fields))
	for _, f := range shape.Fields {
		declared[f.Name] = true
	}
	added := map[string]bool{}
	kept := ref.attr.ContextFields[:0]
	for _, f := range ref.attr.ContextFields {
		switch {
		case declared[f.Name]:
			b.problemf("%s: context field %q is already a field of %q, so the site's value is dropped at runtime",
				ref.path, f.Name, name)
			continue
		case added[f.Name]:
			b.problemf("%s: duplicate context field %q", ref.path, f.Name)
			continue
		case b.opts.RequireDocs && f.Summary == "":
			b.problemf("%s: context field %q: missing summary", ref.path, f.Name)
		}
		added[f.Name] = true
		kept = append(kept, f)
	}
	ref.attr.ContextFields = kept
}

// schemaContextFields converts curated context fields to their emitted form.
func schemaContextFields(fields []ContextField) []*SchemaContextField {
	if len(fields) == 0 {
		return nil
	}
	out := make([]*SchemaContextField, 0, len(fields))
	for _, f := range fields {
		out = append(out, &SchemaContextField{
			Name:     f.Name,
			Type:     f.Type,
			Summary:  f.Summary,
			Doc:      f.Doc,
			Optional: f.Optional,
		})
	}
	return out
}

// topLevelBlock describes one entry of blockSchema. A block is typed — its
// first label selects a variant — exactly when that label is named "type".
func (b *schemaBuilder) topLevelBlock(header hcl.BlockHeaderSchema, handlers map[string]BlockHandler) *SchemaBlock {
	blockType := header.Type
	ts, documented := topLevelSchema(blockType, handlers)

	blk := &SchemaBlock{
		Labels:  header.LabelNames,
		Summary: ts.Summary,
		Doc:     ts.Doc,
		DocPage: ts.DocPage,
	}

	if len(header.LabelNames) > 0 && header.LabelNames[0] == "type" {
		blk.VariantLabel = header.LabelNames[0]
		blk.Undocumented = !documented
		// A typed block has no body of its own, so anything body-shaped in its
		// block-level schema would be silently dropped. Say so instead.
		if ts.Sample != nil || len(ts.Attrs) > 0 || len(ts.Blocks) > 0 || len(ts.Constraints) > 0 {
			b.problemf("%s: block-level schema of a typed block cannot describe a body; move it to the variants", blockType)
		}
		blk.Variants = b.variants(blockType, ts)
		if !documented && b.opts.RequireDocs {
			b.problemf("%s: no block schema registered", blockType)
		}
		return blk
	}

	if !documented {
		blk.Undocumented = true
		blk.Body = newSchemaBody()
		if b.opts.RequireDocs {
			b.problemf("%s: no block schema registered", blockType)
		}
		return blk
	}
	blk.Body = b.bodyFromSample(blockType, ts)
	return blk
}

// topLevelSchema finds the curated schema for a top-level block: from its
// BlockHandler when it implements SchemaProvider, otherwise from an explicit
// RegisterBlockSchema (for blocks extracted before handlers run).
func topLevelSchema(blockType string, handlers map[string]BlockHandler) (TypeSchema, bool) {
	if handler, ok := handlers[blockType]; ok {
		if provider, ok := handler.(SchemaProvider); ok {
			return provider.Schema(), true
		}
	}
	if ts, ok := topLevelBlockSchemas[blockType]; ok {
		return ts, true
	}
	return TypeSchema{}, false
}

// variants describes every variant of a typed block. The variant set comes
// from the registry that drives parsing — or, for a typed block with no
// registry behind it (`metric`), from the block's own curated Variants.
func (b *schemaBuilder) variants(blockType string, blockTS TypeSchema) map[string]*SchemaBody {
	curated := typeSchemas[blockType]
	names := registeredTypeNames(blockType)
	if names == nil {
		curated = blockTS.Variants
		names = sortedKeys(blockTS.Variants)
	}

	// Attributes the block handler decodes from every block of this type
	// before dispatching to the variant's own processor. They are part of each
	// variant's authored surface, so they are merged into every variant body.
	var common []*SchemaAttr
	if envelope, ok := envelopeSchemas[blockType]; ok {
		common = b.mergeBody(blockType, mustReflect(envelope.Sample), envelope).Attributes
	}

	variants := make(map[string]*SchemaBody, len(names))
	for _, name := range names {
		path := blockType + " " + name
		ts, ok := curated[name]

		var body *SchemaBody
		if ok {
			body = b.bodyFromSample(path, ts)
		} else {
			// No schema registered: the variant's own attributes are unknown,
			// but the block's common ones are still true of it.
			body = newSchemaBody()
			body.Undocumented = true
			if b.opts.RequireDocs {
				b.problemf("%s: no schema registered", path)
			}
		}
		body.Attributes = appendCommonAttrs(body.Attributes, common)
		body.Conditional = conditionalTypes[blockType][name]
		// A variant is what a generated per-type index links to, so it is the
		// one place a reference page is required rather than optional.
		if b.opts.RequireDocs && ok && ts.DocPage == "" {
			b.problemf("%s: no DocPage; a generated index has nothing to link to", path)
		}
		variants[name] = body
	}
	return variants
}

// appendCommonAttrs adds the block-level attributes a variant does not itself
// declare, after the variant's own.
func appendCommonAttrs(attrs, common []*SchemaAttr) []*SchemaAttr {
	for _, c := range common {
		declared := false
		for _, a := range attrs {
			if a.Name == c.Name {
				declared = true
				break
			}
		}
		if !declared {
			attrs = append(attrs, c)
		}
	}
	return attrs
}

// mustReflect reflects a sample that the config package itself supplies, where
// failure is a programming error rather than bad curation.
func mustReflect(sample any) *reflectedBody {
	body, err := reflectSample(sample)
	if err != nil {
		panic(fmt.Sprintf("schema: %v", err))
	}
	return body
}

// envelopeSchemas describes the attributes each typed block's handler decodes
// before dispatching to the type-specific processor.
//
// Only the attributes are emitted — spliced into every variant — so each
// envelope's own Summary is for maintainers reading this file rather than for
// consumers of the document.
var envelopeSchemas = map[string]TypeSchema{
	"client": {
		Sample:  &ClientDefinition{},
		Summary: "Attributes every client block accepts.",
		Attrs:   map[string]AttrMeta{"disabled": DisabledAttr},
	},
	"server": {
		Sample:  &ServerDefinition{},
		Summary: "Attributes every server block accepts.",
		Attrs:   map[string]AttrMeta{"disabled": DisabledAttr},
	},
	"trigger": {
		Sample:  &TriggerDefinition{},
		Summary: "Attributes every trigger block accepts.",
		Attrs: map[string]AttrMeta{
			"disabled": DisabledAttr,
			"tracing":  TracingAttr,
		},
	},
	"condition": {
		Sample:  &ConditionDefinition{},
		Summary: "Attributes every condition block accepts.",
		Attrs:   map[string]AttrMeta{"disabled": DisabledAttr},
	},
	"editor": {
		Sample:  &editorOuterBody{},
		Summary: "Attributes every editor block accepts.",
		Attrs:   EditorParamAttrs,
	},
}

// Attribute metadata for attributes that recur across many blocks with
// identical meaning. Curating them once keeps the wording consistent and keeps
// each block's own schema to what is actually specific to it. Blocks fold them
// in with MergeAttrs.
var (
	// DisabledAttr documents `disabled`.
	DisabledAttr = AttrMeta{
		Summary: "Skip this block entirely.",
		Doc:     "The block is parsed and validated, but nothing is created from it.",
		Hint:    HintBool,
	}

	// TracingAttr documents `tracing`, which selects a tracing backend.
	TracingAttr = AttrMeta{
		Summary: "Where to report traces.",
		Doc:     "A `client \"otlp\"` block. Auto-wires to the default tracing backend when omitted.",
		Hint:    HintTracingRef,
	}

	// MetricsAttr documents `metrics`, which selects a metrics backend.
	MetricsAttr = AttrMeta{
		Summary: "Where to report metrics.",
		Doc:     "A `server \"metrics\"` or `client \"otlp\"` block. Auto-wires to the default metrics backend when omitted.",
		Hint:    HintMetricsRef,
	}

	// WireFormatAttr documents `wire_format`, which selects how message
	// payloads are encoded and decoded on the wire.
	WireFormatAttr = AttrMeta{
		Summary: "How to encode and decode message payloads.",
		Doc: "A `wire_format` block, or the name of a built-in format. Under `auto`, " +
			"strings and bytes pass through and everything else is JSON-encoded; " +
			"decoding auto-detects JSON and falls back to a string.",
		Hint:    HintExpression,
		Default: "auto",
	}

	// OnConnectAttr documents `on_connect`, which every client with a
	// meaningful connection lifecycle accepts.
	OnConnectAttr = AttrMeta{
		Summary: "Evaluated after the connection is established and ready.",
		Doc:     "Runs synchronously: no messages are produced or consumed until it returns. There is no message in flight, so no message variables are in scope.",
		Hint:    HintActionExpression,
		Context: "connection",
	}

	// OnDisconnectAttr documents `on_disconnect`, the counterpart to
	// OnConnectAttr.
	OnDisconnectAttr = AttrMeta{
		Summary: "Evaluated when the connection is lost or closed.",
		Doc:     "Always runs before any reconnection attempt, and on a graceful shutdown before the connection is torn down. Every `on_connect` after the first is preceded by one.",
		Hint:    HintActionExpression,
		Context: "connection",
	}

	// OnDecodeErrorAttr documents `on_decode_error`, which receivers accept to
	// handle a payload their wire format cannot decode.
	OnDecodeErrorAttr = AttrMeta{
		Summary: "Evaluated when an inbound message cannot be decoded.",
		Doc:     "The message is dropped rather than delivered. Use this to publish to a dead-letter destination or record the failure.",
		Hint:    HintActionExpression,
		Context: "decode-error",
	}
)

// MergeAttrs combines attribute metadata maps into a new one, with later maps
// winning on conflict. Use it to fold shared attributes into a block's own:
//
//	Attrs: cfg.MergeAttrs(cfg.SubscriberSourceAttrs, map[string]cfg.AttrMeta{…})
func MergeAttrs(maps ...map[string]AttrMeta) map[string]AttrMeta {
	merged := map[string]AttrMeta{}
	for _, m := range maps {
		for name, meta := range m {
			merged[name] = meta
		}
	}
	return merged
}

// registeredTypeNames returns the variant keys of a registry-driven typed
// block, or nil when the block has no registry behind it.
func registeredTypeNames(blockType string) []string {
	switch blockType {
	case "client":
		return sortedKeys(clientRegistry)
	case "server":
		return sortedKeys(serverRegistry)
	case "condition":
		return sortedKeys(conditionRegistry)
	case "wire_format":
		return sortedKeys(wireFormatRegistry)
	case "editor":
		return sortedKeys(editorRegistry)
	case "trigger":
		// Conditional trigger types are described as the full superset: the
		// schema says what can exist, not what a given config enables.
		names := sortedKeys(triggerRegistry)
		for _, name := range sortedKeys(conditionalTypes["trigger"]) {
			if _, ok := triggerRegistry[name]; !ok {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		return names
	}
	return nil
}

// sortedKeys returns a map's keys in sorted order, so problems are reported
// deterministically.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
