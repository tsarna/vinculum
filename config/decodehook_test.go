package config

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// newHookTestConfig returns a built Config plus an observer over its
// UserLogger, so tests can assert on what a failing hook logged.
func newHookTestConfig(t *testing.T) (*Config, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	config, diags := NewConfig().
		WithSources([]byte("")).
		WithLogger(zap.New(core)).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	return config, logs
}

func TestMakeDecodeErrorHook_AbsentExpressionYieldsNilHook(t *testing.T) {
	config, _ := newHookTestConfig(t)

	// A nil hook is how a receiver learns there is no observer to invoke.
	assert.Nil(t, MakeDecodeErrorHook(config, nil, "test receiver"))

	var missing hcl.Expression
	assert.Nil(t, MakeDecodeErrorHook(config, missing, "test receiver"))
}

func TestMakeDecodeErrorHook_EvalContext(t *testing.T) {
	config, _ := newHookTestConfig(t)

	// Capture the whole context by evaluating an object of everything the
	// hook is documented to expose.
	expr := parseExpr(t, `{
		raw         = tostring(ctx.raw)
		error       = ctx.error
		wire_format = ctx.wire_format
		topic       = ctx.topic
		device      = ctx.fields.device
		routing_key = ctx.routing_key
	}`)

	var captured map[string]string
	hook := MakeDecodeErrorHook(config, &capturingExpr{inner: expr, out: &captured}, "test receiver")
	require.NotNil(t, hook)

	hook(context.Background(), wire.DecodeError{
		Raw:    []byte("not json {{"),
		Err:    errors.New("boom"),
		Format: "json",
		Topic:  "sensors/temp",
		Fields: map[string]string{"device": "abc"},
		Attrs:  map[string]string{"routing_key": "sensors.temp"},
	})

	assert.Equal(t, "not json {{", captured["raw"])
	assert.Equal(t, "boom", captured["error"])
	assert.Equal(t, "json", captured["wire_format"])
	assert.Equal(t, "sensors/temp", captured["topic"])
	assert.Equal(t, "abc", captured["device"])
	assert.Equal(t, "sensors.temp", captured["routing_key"])
}

func TestMakeDecodeErrorHook_EmptyFieldsIsIndexableObject(t *testing.T) {
	config, logs := newHookTestConfig(t)

	// fields must never be null, so expressions can index it safely.
	hook := MakeDecodeErrorHook(config, parseExpr(t, `keys(ctx.fields)`), "test receiver")
	hook(context.Background(), wire.DecodeError{Raw: []byte("x"), Err: errors.New("e"), Format: "json"})

	assert.Empty(t, logs.FilterMessageSnippet("on_decode_error").All(),
		"indexing empty fields must not error")
}

func TestMakeDecodeErrorHook_AttrsCannotShadowFixedAttributes(t *testing.T) {
	config, _ := newHookTestConfig(t)

	var captured map[string]string
	expr := parseExpr(t, `{ wire_format = ctx.wire_format, topic = ctx.topic }`)
	hook := MakeDecodeErrorHook(config, &capturingExpr{inner: expr, out: &captured}, "test receiver")

	hook(context.Background(), wire.DecodeError{
		Raw:    []byte("x"),
		Err:    errors.New("e"),
		Format: "json",
		Topic:  "real/topic",
		Attrs: map[string]string{
			"wire_format": "spoofed",
			"topic":       "spoofed",
		},
	})

	assert.Equal(t, "json", captured["wire_format"])
	assert.Equal(t, "real/topic", captured["topic"])
}

func TestMakeDecodeErrorHook_EvalFailureIsLoggedNotPropagated(t *testing.T) {
	config, logs := newHookTestConfig(t)

	// Referencing a nonexistent variable makes evaluation fail.
	hook := MakeDecodeErrorHook(config, parseExpr(t, `ctx.no_such_attribute`), "rabbitmq receiver \"in\"")
	require.NotNil(t, hook)

	// A broken hook must not panic and must not change the error path.
	assert.NotPanics(t, func() {
		hook(context.Background(), wire.DecodeError{
			Raw: []byte("x"), Err: errors.New("e"), Format: "json",
		})
	})

	entries := logs.FilterMessageSnippet("on_decode_error").All()
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Message, `rabbitmq receiver "in"`)
}

func TestIsStrictWireFormat(t *testing.T) {
	// The always-succeeds built-ins can't poison a receiver.
	for _, name := range []string{"auto", "string", "bytes"} {
		assert.False(t, IsStrictWireFormat(name), name)
	}
	// json and any custom/plugin format are treated as strict.
	for _, name := range []string{"json", "protobuf", ""} {
		assert.True(t, IsStrictWireFormat(name), name)
	}
}

// capturingExpr wraps an expression and records its evaluated object value
// as a string map, so tests can assert on the eval context contents.
type capturingExpr struct {
	inner hcl.Expression
	out   *map[string]string
}

func (e *capturingExpr) Value(ctx *hcl.EvalContext) (cty.Value, hcl.Diagnostics) {
	val, diags := e.inner.Value(ctx)
	if !diags.HasErrors() && val.Type().IsObjectType() {
		m := make(map[string]string)
		for k, v := range val.AsValueMap() {
			if v.IsKnown() && !v.IsNull() && v.Type() == cty.String {
				m[k] = v.AsString()
			}
		}
		*e.out = m
	}
	return val, diags
}

func (e *capturingExpr) Variables() []hcl.Traversal { return e.inner.Variables() }
func (e *capturingExpr) Range() hcl.Range           { return e.inner.Range() }
func (e *capturingExpr) StartRange() hcl.Range      { return e.inner.StartRange() }

func TestOnDecodeErrorExcludedFromBlockDependencies(t *testing.T) {
	// on_decode_error is evaluated at message time, not config load, so it
	// must not contribute a config-load dependency. Without the exclusion a
	// hook that publishes to a bus the client also feeds would cycle.
	src := []byte(`
bus "errors" {}

client "mqtt" "in" {
  broker = "tcp://localhost:1883"
  receiver "r" {
    topic_subscription { topic = "sensors/#" }
    wire_format     = "json"
    action          = send(ctx, bus.errors, "ok", msg)
    on_decode_error = send(ctx, bus.errors, "bad", ctx.error)
  }
}
`)

	block := singleClientBlock(t, src)
	deps, diags := (&ClientBlockHandler{}).GetBlockDependencies(block)
	require.False(t, diags.HasErrors(), "%s", diags)

	assert.NotContains(t, deps, "bus.errors",
		"on_decode_error and action are runtime-evaluated; neither may create a load-time dependency")
}

// singleClientBlock parses src and returns its one client block.
func singleClientBlock(t *testing.T, src []byte) *hcl.Block {
	t.Helper()
	f, diags := hclsyntax.ParseConfig(src, "test.vcl", hcl.InitialPos)
	require.False(t, diags.HasErrors(), "%s", diags)

	content, _, cDiags := f.Body.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "bus", LabelNames: []string{"name"}},
			{Type: "client", LabelNames: []string{"type", "name"}},
		},
	})
	require.False(t, cDiags.HasErrors(), "%s", cDiags)

	for _, b := range content.Blocks {
		if b.Type == "client" {
			return b
		}
	}
	t.Fatal("no client block found")
	return nil
}
