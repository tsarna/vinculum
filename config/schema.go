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

	// Summary is a one-line description, used as a completion item's detail.
	Summary string

	// Doc is richer Markdown used for hover; may be multi-paragraph.
	Doc string

	// Attrs holds curated per-attribute metadata, keyed by HCL attribute name.
	// Every key must name an attribute that reflection found.
	Attrs map[string]AttrMeta

	// Blocks holds curated metadata for nested sub-blocks, keyed by HCL block
	// type name. Structure is still reflected; this only adds docs. Every key
	// must name a sub-block that reflection found.
	Blocks map[string]TypeSchema

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
	// v1 emits the name only; enumerating each shape's fields is a later
	// revision. Use a stable kebab-case name shared by every attribute with
	// the same shape (e.g. "message", "http-request", "connection").
	Context string

	// Enum is the closed set of legal string values, when there is one.
	Enum []string

	// Deprecated, when non-empty, marks the attribute deprecated and explains
	// the replacement.
	Deprecated string
}

// Hint is a member of a closed vocabulary describing what kind of value an
// attribute takes, so a completion provider can offer the right candidates.
// The provider — not Vinculum — knows how to expand each hint.
type Hint string

const (
	// HintExpression is a generic expression, evaluated once at config load
	// time for its value.
	HintExpression Hint = "expression"
	// HintActionExpression is an action: an expression
	// evaluated at event time rather than config time, largely for its side
	// effects — logging, sending messages, setting variables — with a
	// block-specific `ctx` in scope. In list form each element is evaluated in
	// order and the last value is the result.
	HintActionExpression Hint = "action-expression"
	// HintBusRef is a `bus.<name>` reference.
	HintBusRef Hint = "bus-ref"
	// HintClientRef is a `client.<name>` reference.
	HintClientRef Hint = "client-ref"
	// HintServerRef is a `server.<name>` reference.
	HintServerRef Hint = "server-ref"
	// HintMetricRef is a `metric.<name>` reference.
	HintMetricRef Hint = "metric-ref"
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
	Repeatable bool // field was a slice
	Required   bool // field was a plain struct (not a pointer or slice)
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
				// Required is the author's intent — an attribute with no
				// `,optional` and a non-pointer field. This deliberately
				// differs from gohcl.ImpliedBodySchema for hcl.Expression
				// fields, which gohcl always reports as optional because it
				// signals absence with a null value rather than an error.
				Required: kind == "attr" && field.Type.Kind() != reflect.Ptr,
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
			body.Blocks = append(body.Blocks, reflectedBlock{
				Name:       name,
				Labels:     blockLabels(fty),
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

// blockLabels returns the label names of a nested block's struct type, in
// declaration order.
func blockLabels(ty reflect.Type) []string {
	var labels []string
	for i := 0; i < ty.NumField(); i++ {
		tag := ty.Field(i).Tag.Get("hcl")
		if tag == "" {
			continue
		}
		name, kind, _ := strings.Cut(tag, ",")
		if kind == "label" {
			labels = append(labels, name)
		}
	}
	return labels
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
	// Undocumented is true when no curated schema was registered for this body.
	Undocumented bool `json:"undocumented,omitempty"`
	// Conditional is true for a variant whose availability depends on config
	// state (see RegisterConditionalTriggerType).
	Conditional bool                          `json:"conditional,omitempty"`
	Attributes  []*SchemaAttr                 `json:"attributes"`
	Blocks      map[string]*SchemaNestedBlock `json:"blocks"`
	Constraints []Constraint                  `json:"constraints"`
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
	// Context names the `ctx` shape this attribute's expression sees. Shapes
	// are enumerated in a later revision of the format.
	Context    string   `json:"context,omitempty"`
	Enum       []string `json:"enum,omitempty"`
	Deprecated string   `json:"deprecated,omitempty"`
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

// bodyFromSample reflects the sample in ts and merges ts's curated metadata
// into the result. A missing or unreflectable sample yields an empty body and
// a recorded problem.
func (b *schemaBuilder) bodyFromSample(path string, ts TypeSchema) *SchemaBody {
	rb, err := reflectSample(ts.Sample)
	if err != nil {
		b.problemf("%s: %v", path, err)
		return newSchemaBody()
	}
	return b.mergeBody(path, rb, ts)
}

// mergeBody combines a reflected body with the curated metadata describing it.
// The reflected structure is authoritative: curation that names something the
// structure does not contain is dropped and reported.
func (b *schemaBuilder) mergeBody(path string, rb *reflectedBody, ts TypeSchema) *SchemaBody {
	body := newSchemaBody()
	body.Summary = ts.Summary
	body.Doc = ts.Doc

	if b.opts.RequireDocs && ts.Summary == "" {
		b.problemf("%s: missing summary", path)
	}

	for _, ra := range rb.Attrs {
		meta := ts.Attrs[ra.Name]
		if b.opts.RequireDocs && meta.Summary == "" {
			b.problemf("%s.%s: missing summary", path, ra.Name)
		}
		body.Attributes = append(body.Attributes, &SchemaAttr{
			Name:       ra.Name,
			Required:   ra.Required,
			Type:       ra.Type,
			Summary:    meta.Summary,
			Doc:        meta.Doc,
			Hint:       meta.Hint,
			Context:    meta.Context,
			Enum:       meta.Enum,
			Deprecated: meta.Deprecated,
		})
	}
	for _, name := range sortedKeys(ts.Attrs) {
		if rb.attr(name) == nil {
			b.problemf("%s: documented attribute %q does not exist", path, name)
		}
	}

	for _, rblk := range rb.Blocks {
		nestedTS := ts.Blocks[rblk.Name]
		nestedBody := b.mergeBody(path+"."+rblk.Name, rblk.Body, nestedTS)
		labels := rblk.Labels
		if labels == nil {
			labels = []string{}
		}
		body.Blocks[rblk.Name] = &SchemaNestedBlock{
			Labels:     labels,
			Repeatable: rblk.Repeatable,
			Required:   rblk.Required,
			SchemaBody: *nestedBody,
		}
	}
	for _, name := range sortedKeys(ts.Blocks) {
		if rb.block(name) == nil {
			b.problemf("%s: documented block %q does not exist", path, name)
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
