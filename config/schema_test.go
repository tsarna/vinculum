package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// schemaFixtureBody exercises every field shape the reflector must handle.
type schemaFixtureBody struct {
	Name string `hcl:",label"`

	Listen   string            `hcl:"listen"`
	Enabled  *bool             `hcl:"enabled,optional"`
	Count    int               `hcl:"count,optional"`
	Ratio    *float64          `hcl:"ratio,optional"`
	Topics   []string          `hcl:"topics"`
	Labels   map[string]string `hcl:"labels,optional"`
	Timeout  time.Duration     `hcl:"timeout,optional"`
	Action   hcl.Expression    `hcl:"action,optional"`
	Handler  hcl.Expression    `hcl:"handler"`
	Value    cty.Value         `hcl:"value,optional"`
	Disabled bool              `hcl:"disabled,optional"`

	TLS      *schemaFixtureTLS      `hcl:"tls,block"`
	Handlers []schemaFixtureHandler `hcl:"handle,block"`
	Spec     schemaFixtureSpec      `hcl:"spec,block"`

	DefRange      hcl.Range `hcl:",def_range"`
	RemainingBody hcl.Body  `hcl:",remain"`

	notAConfigField string
}

type schemaFixtureTLS struct {
	CertFile string `hcl:"cert_file,optional"`
	KeyFile  string `hcl:"key_file,optional"`
}

type schemaFixtureHandler struct {
	Route    string         `hcl:"route,label"`
	Method   string         `hcl:"method,label"`
	Action   hcl.Expression `hcl:"action,optional"`
	Params   []schemaParam  `hcl:"param,block"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type schemaParam struct {
	Name     string `hcl:"name,label"`
	Type     string `hcl:"type"`
	Required *bool  `hcl:"required,optional"`
}

type schemaFixtureSpec struct {
	Kind string `hcl:"kind"`
}

func fixtureSchema() TypeSchema {
	return TypeSchema{
		Sample:  &schemaFixtureBody{},
		Summary: "A fixture block.",
		Doc:     "Longer *Markdown* prose.",
		Attrs: map[string]AttrMeta{
			"listen": {
				Summary: "Listen address.",
				Hint:    HintListenAddr,
			},
			"topics": {
				Summary: "Topic patterns to match.",
				Doc:     "MQTT syntax: `+` and `#`.",
				Hint:    HintTopicPattern,
			},
			"count": {
				Summary:    "Old counter.",
				Deprecated: "Use `ratio` instead.",
				Enum:       []string{"1", "2"},
			},
		},
		Blocks: map[string]TypeSchema{
			"handle": {
				Summary: "A route handler.",
				Attrs: map[string]AttrMeta{
					"action": {
						Summary: "Expression evaluated per request.",
						Hint:    HintActionExpression,
						Context: "http-request",
					},
				},
				Blocks: map[string]TypeSchema{
					"param": {Summary: "A typed parameter."},
				},
			},
		},
		Constraints: []Constraint{
			MutuallyExclusive("action", "handler"),
		},
	}
}

// --- reflection -------------------------------------------------------------

func TestReflectSampleAttributes(t *testing.T) {
	body, err := reflectSample(&schemaFixtureBody{})
	require.NoError(t, err)

	type want struct {
		required bool
		typ      string
	}
	expected := map[string]want{
		"listen":   {true, attrTypeString},
		"enabled":  {false, attrTypeBool},
		"count":    {false, attrTypeNumber},
		"ratio":    {false, attrTypeNumber},
		"topics":   {true, attrTypeList},
		"labels":   {false, attrTypeMap},
		"timeout":  {false, attrTypeNumber},
		"action":   {false, attrTypeExpression},
		"handler":  {true, attrTypeExpression},
		"value":    {false, attrTypeExpression},
		"disabled": {false, attrTypeBool},
	}

	require.Len(t, body.Attrs, len(expected), "unexpected attribute count")
	for name, w := range expected {
		attr := body.attr(name)
		if assert.NotNil(t, attr, "attribute %q missing", name) {
			assert.Equal(t, w.required, attr.Required, "attribute %q required", name)
			assert.Equal(t, w.typ, attr.Type, "attribute %q type", name)
		}
	}

	// Declaration order is preserved, so docs and completions read naturally.
	assert.Equal(t, "listen", body.Attrs[0].Name)
	assert.Equal(t, "enabled", body.Attrs[1].Name)

	// Labels, def_range, remain, and untagged fields are not config surface.
	for _, hidden := range []string{"", "name", "notAConfigField"} {
		assert.Nil(t, body.attr(hidden))
	}
}

func TestReflectSampleBlocks(t *testing.T) {
	body, err := reflectSample(&schemaFixtureBody{})
	require.NoError(t, err)

	require.Len(t, body.Blocks, 3)

	tlsBlock := body.block("tls")
	require.NotNil(t, tlsBlock)
	assert.Empty(t, tlsBlock.Labels)
	assert.False(t, tlsBlock.Repeatable, "pointer block is not repeatable")
	assert.False(t, tlsBlock.Required, "pointer block is optional")
	assert.Len(t, tlsBlock.Body.Attrs, 2)

	handleBlock := body.block("handle")
	require.NotNil(t, handleBlock)
	assert.Equal(t, []string{"route", "method"}, handleBlock.Labels)
	assert.True(t, handleBlock.Repeatable, "slice block is repeatable")
	assert.False(t, handleBlock.Required, "slice block is optional")

	// Recursion continues to arbitrary depth.
	paramBlock := handleBlock.Body.block("param")
	require.NotNil(t, paramBlock)
	assert.Equal(t, []string{"name"}, paramBlock.Labels)
	assert.True(t, paramBlock.Repeatable)
	require.NotNil(t, paramBlock.Body.attr("type"))
	assert.True(t, paramBlock.Body.attr("type").Required)
	assert.False(t, paramBlock.Body.attr("required").Required, "pointer attr is optional")

	specBlock := body.block("spec")
	require.NotNil(t, specBlock)
	assert.True(t, specBlock.Required, "plain struct block is required")
	assert.False(t, specBlock.Repeatable)
}

// TestReflectMatchesGohcl guards the invariant that makes this feature
// trustworthy: the reflected structure describes exactly what the parser
// decodes.
func TestReflectMatchesGohcl(t *testing.T) {
	samples := []any{
		&schemaFixtureBody{},
		&schemaFixtureHandler{},
		// Real decode structs from the parser.
		&SubscriptionDefinition{},
		&ClientDefinition{},
		&ServerDefinition{},
		&TriggerDefinition{},
	}

	for _, sample := range samples {
		body, err := reflectSample(sample)
		require.NoError(t, err)

		implied, _ := gohcl.ImpliedBodySchema(sample)

		assert.ElementsMatch(t, attrNames(implied.Attributes), reflectedAttrNames(body),
			"attribute names for %T", sample)

		for _, ia := range implied.Attributes {
			ra := body.attr(ia.Name)
			require.NotNil(t, ra)
			if ra.Type == attrTypeExpression && ra.GoType == hclExpressionType {
				// gohcl always reports hcl.Expression attributes as optional
				// because it signals absence with a null value. The schema
				// reports the author's intent instead.
				continue
			}
			assert.Equal(t, ia.Required, ra.Required, "%T.%s required", sample, ia.Name)
		}

		require.Len(t, body.Blocks, len(implied.Blocks), "block count for %T", sample)
		for _, ib := range implied.Blocks {
			rb := body.block(ib.Type)
			require.NotNil(t, rb, "block %q missing for %T", ib.Type, sample)
			assert.Equal(t, ib.LabelNames, nilIfEmpty(rb.Labels), "block %q labels for %T", ib.Type, sample)
		}
	}
}

func TestReflectSampleErrors(t *testing.T) {
	_, err := reflectSample(nil)
	assert.Error(t, err)

	_, err = reflectSample("not a struct")
	assert.Error(t, err)

	type badTag struct {
		Field string `hcl:"field,bogus"`
	}
	_, err = reflectSample(&badTag{})
	assert.ErrorContains(t, err, "bogus")

	type badBlock struct {
		Field string `hcl:"field,block"`
	}
	_, err = reflectSample(&badBlock{})
	assert.ErrorContains(t, err, "must be a struct")

	_, err = reflectSample(&recursiveBlock{})
	assert.ErrorContains(t, err, "recursive")
}

type recursiveBlock struct {
	Nested *recursiveBlock `hcl:"nested,block"`
}

// --- merging curated metadata ----------------------------------------------

func TestMergeBodyAppliesCuration(t *testing.T) {
	b := &schemaBuilder{}
	body := b.bodyFromSample("fixture", fixtureSchema())
	assert.Empty(t, b.problems)

	assert.Equal(t, "A fixture block.", body.Summary)
	assert.Equal(t, "Longer *Markdown* prose.", body.Doc)

	listen := findAttr(body, "listen")
	require.NotNil(t, listen)
	assert.Equal(t, "Listen address.", listen.Summary)
	assert.Equal(t, HintListenAddr, listen.Hint)
	assert.True(t, listen.Required)
	assert.Equal(t, attrTypeString, listen.Type)

	count := findAttr(body, "count")
	require.NotNil(t, count)
	assert.Equal(t, "Use `ratio` instead.", count.Deprecated)
	assert.Equal(t, []string{"1", "2"}, count.Enum)

	// Uncurated attributes still appear, with structure only.
	ratio := findAttr(body, "ratio")
	require.NotNil(t, ratio)
	assert.Empty(t, ratio.Summary)

	// Nested blocks are reflected, and curation reaches arbitrary depth.
	handle := body.Blocks["handle"]
	require.NotNil(t, handle)
	assert.Equal(t, "A route handler.", handle.Summary)
	assert.Equal(t, []string{"route", "method"}, handle.Labels)
	assert.True(t, handle.Repeatable)
	assert.False(t, handle.Required)
	handleAction := findAttr(&handle.SchemaBody, "action")
	require.NotNil(t, handleAction)
	assert.Equal(t, "Expression evaluated per request.", handleAction.Summary)
	assert.Equal(t, HintActionExpression, handleAction.Hint)
	assert.Equal(t, "http-request", handleAction.Context)
	require.NotNil(t, handle.Blocks["param"])
	assert.Equal(t, "A typed parameter.", handle.Blocks["param"].Summary)

	// An uncurated nested block is still structurally described.
	tlsBlock := body.Blocks["tls"]
	require.NotNil(t, tlsBlock)
	assert.Empty(t, tlsBlock.Summary)
	assert.Len(t, tlsBlock.Attributes, 2)

	require.Len(t, body.Constraints, 1)
	assert.Equal(t, ConstraintMutuallyExclusive, body.Constraints[0].Kind)
	assert.Equal(t, "Specify at most one of action or handler.", body.Constraints[0].Message)
}

func TestMergeBodyDetectsOrphanedCuration(t *testing.T) {
	ts := fixtureSchema()
	ts.Attrs["listn"] = AttrMeta{Summary: "Typo."}
	ts.Blocks["handel"] = TypeSchema{Summary: "Typo."}
	ts.Blocks["handle"] = TypeSchema{
		Attrs: map[string]AttrMeta{"actoin": {Summary: "Typo."}},
	}
	ts.Constraints = append(ts.Constraints, Requires("listen", "nonesuch"))

	b := &schemaBuilder{}
	b.bodyFromSample("fixture", ts)

	require.Len(t, b.problems, 4)
	assert.ErrorContains(t, b.problems[0], `fixture: documented attribute "listn" does not exist`)
	assert.ErrorContains(t, b.problems[1], `fixture.handle: documented attribute "actoin" does not exist`)
	assert.ErrorContains(t, b.problems[2], `fixture: documented block "handel" does not exist`)
	assert.ErrorContains(t, b.problems[3], `fixture: constraint requires references unknown attribute "nonesuch"`)
}

func TestMergeBodyRequireDocs(t *testing.T) {
	b := &schemaBuilder{opts: SchemaGenOptions{RequireDocs: true}}
	b.bodyFromSample("fixture", fixtureSchema())

	// Everything without a curated summary is reported, at every depth.
	messages := problemStrings(b.problems)
	assert.Contains(t, messages, "fixture.ratio: missing summary")
	assert.Contains(t, messages, "fixture.tls: missing summary")
	assert.Contains(t, messages, "fixture.tls.cert_file: missing summary")
	assert.Contains(t, messages, "fixture.handle.param.type: missing summary")
	// ...and everything with one is not.
	assert.NotContains(t, messages, "fixture: missing summary")
	assert.NotContains(t, messages, "fixture.listen: missing summary")
	assert.NotContains(t, messages, "fixture.handle: missing summary")
}

func TestBodyFromSampleWithoutSample(t *testing.T) {
	b := &schemaBuilder{}
	body := b.bodyFromSample("fixture", TypeSchema{Summary: "No sample."})

	require.Len(t, b.problems, 1)
	assert.ErrorContains(t, b.problems[0], "no sample value")
	assert.Empty(t, body.Attributes)
	assert.Empty(t, body.Blocks)
}

// --- emitted JSON -----------------------------------------------------------

func TestSchemaBlockJSONShapes(t *testing.T) {
	b := &schemaBuilder{}
	body := b.bodyFromSample("fixture", fixtureSchema())
	require.Empty(t, b.problems)

	t.Run("plain", func(t *testing.T) {
		doc := &SchemaDocument{
			SchemaVersion:   SchemaFormatVersion,
			VinculumVersion: "0.0.0-test",
			Blocks: map[string]*SchemaBlock{
				"fixture": {Labels: []string{"name"}, Body: body},
			},
		}
		out := marshalToMap(t, doc)

		assert.Equal(t, SchemaFormatVersion, out["schemaVersion"])
		assert.Equal(t, "0.0.0-test", out["vinculumVersion"])

		block := out["blocks"].(map[string]any)["fixture"].(map[string]any)
		assert.Equal(t, []any{"name"}, block["labels"])
		assert.Equal(t, "A fixture block.", block["summary"])
		assert.NotContains(t, block, "variantLabel")
		assert.NotContains(t, block, "variants")
		assert.NotContains(t, block, "undocumented")

		// Body collections are always present, even when empty.
		require.Contains(t, block, "attributes")
		require.Contains(t, block, "blocks")
		require.Contains(t, block, "constraints")

		attrs := block["attributes"].([]any)
		listen := attrs[0].(map[string]any)
		assert.Equal(t, "listen", listen["name"])
		assert.Equal(t, true, listen["required"])
		assert.Equal(t, "string", listen["type"])
		assert.Equal(t, "listen-addr", listen["hint"])
		// Absent curated fields are omitted rather than emitted as null.
		assert.NotContains(t, listen, "enum")
		assert.NotContains(t, listen, "deprecated")
		assert.NotContains(t, listen, "doc")

		handle := block["blocks"].(map[string]any)["handle"].(map[string]any)
		assert.Equal(t, []any{"route", "method"}, handle["labels"])
		assert.Equal(t, true, handle["repeatable"])
		assert.Equal(t, false, handle["required"])
		assert.Contains(t, handle, "attributes")

		constraint := block["constraints"].([]any)[0].(map[string]any)
		assert.Equal(t, "mutually_exclusive", constraint["kind"])
		assert.Equal(t, []any{"action", "handler"}, constraint["attributes"])
	})

	t.Run("typed", func(t *testing.T) {
		variantBody := b.bodyFromSample("fixture", fixtureSchema())
		variantBody.Conditional = true
		block := &SchemaBlock{
			Labels:       []string{"type", "name"},
			VariantLabel: "type",
			Summary:      "A typed fixture.",
			Variants:     map[string]*SchemaBody{"one": variantBody},
		}
		out := marshalToMap(t, block)

		assert.Equal(t, "type", out["variantLabel"])
		assert.Equal(t, []any{"type", "name"}, out["labels"])
		// A typed block has no body of its own.
		assert.NotContains(t, out, "attributes")
		assert.NotContains(t, out, "blocks")
		assert.NotContains(t, out, "constraints")

		variant := out["variants"].(map[string]any)["one"].(map[string]any)
		assert.Equal(t, true, variant["conditional"])
		assert.Contains(t, variant, "attributes")
	})

	t.Run("undocumented", func(t *testing.T) {
		out := marshalToMap(t, &SchemaBlock{Undocumented: true})
		assert.Equal(t, true, out["undocumented"])
		assert.Equal(t, []any{}, out["labels"], "label-less blocks emit an empty list, not null")
		assert.Equal(t, []any{}, out["attributes"])
		assert.Equal(t, map[string]any{}, out["blocks"])
	})
}

// --- helpers ----------------------------------------------------------------

func attrNames(attrs []hcl.AttributeSchema) []string {
	names := make([]string, len(attrs))
	for i, a := range attrs {
		names[i] = a.Name
	}
	return names
}

func reflectedAttrNames(body *reflectedBody) []string {
	names := make([]string, len(body.Attrs))
	for i, a := range body.Attrs {
		names[i] = a.Name
	}
	return names
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func findAttr(body *SchemaBody, name string) *SchemaAttr {
	for _, a := range body.Attributes {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func problemStrings(problems []error) []string {
	out := make([]string, len(problems))
	for i, p := range problems {
		out[i] = p.Error()
	}
	return out
}

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}
