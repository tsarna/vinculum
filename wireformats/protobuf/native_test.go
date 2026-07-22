package protobuf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bytescty "github.com/tsarna/bytes-cty-type"
	timecty "github.com/tsarna/time-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// formatFor loads a schema and returns the CtyWireFormat-wrapped format for one
// message. The wrapper is the path clients actually use, so tests exercise the
// cty<->native reduction, not just the bare format.
func formatFor(t *testing.T, fds []byte, msg string, m mode) *cfg.CtyWireFormat {
	t.Helper()
	sch, err := loadSchema(fds)
	require.NoError(t, err)
	md, ok := sch.messages[protoreflect.FullName(msg)]
	require.True(t, ok, "message %s present", msg)
	return &cfg.CtyWireFormat{Inner: newProtoFormat(sch, md, m)}
}

func TestNativeRoundTrip_Order(t *testing.T) {
	f := formatFor(t, ordersFDS(t), "acme.test.v1.Order", modeNative)

	in := cty.ObjectVal(map[string]cty.Value{
		"id":     cty.NumberIntVal(42),
		"name":   cty.StringVal("widget"),
		"active": cty.True,
		"amount": cty.NumberFloatVal(19.99),
		"data":   bytescty.BuildBytesObject([]byte{1, 2, 3}, "application/octet-stream"),
		"status": cty.StringVal("ACTIVE"),
		"item":   cty.ObjectVal(map[string]cty.Value{"sku": cty.StringVal("X1"), "qty": cty.NumberIntVal(3)}),
		"tags":   cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
		"attrs":  cty.ObjectVal(map[string]cty.Value{"k": cty.StringVal("v")}),
		"note":   cty.StringVal("hello"),
		"a":      cty.StringVal("choiceA"),
	})

	bin, err := f.Serialize(in)
	require.NoError(t, err)

	got, err := f.Deserialize(bin)
	require.NoError(t, err)
	out := got.(cty.Value)

	assert.True(t, out.GetAttr("id").RawEquals(cty.NumberIntVal(42)))
	assert.Equal(t, "widget", out.GetAttr("name").AsString())
	assert.True(t, out.GetAttr("active").True())
	assert.True(t, out.GetAttr("amount").RawEquals(cty.NumberFloatVal(19.99)))
	assert.Equal(t, "ACTIVE", out.GetAttr("status").AsString())
	assert.Equal(t, "X1", out.GetAttr("item").GetAttr("sku").AsString())
	assert.Equal(t, "hello", out.GetAttr("note").AsString())
	assert.Equal(t, "choiceA", out.GetAttr("a").AsString())

	// bytes decode to a rich bytes object.
	b, err := bytescty.GetBytesFromValue(out.GetAttr("data"))
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, b.Data)

	// repeated + map.
	assert.Equal(t, 2, out.GetAttr("tags").LengthInt())
	assert.Equal(t, "v", out.GetAttr("attrs").GetAttr("k").AsString())

	// The unset oneof member and unset scalars.
	assert.True(t, out.GetAttr("b").IsNull(), "unset oneof member is null")
}

func TestNative_Presence_NullVsZero(t *testing.T) {
	f := formatFor(t, ordersFDS(t), "acme.test.v1.Order", modeNative)

	// Empty message: no-presence scalars default to zero; presence fields
	// (proto3 optional note, oneof members) are null.
	bin, err := f.Serialize(cty.EmptyObjectVal)
	require.NoError(t, err)
	out, err := f.Deserialize(bin)
	require.NoError(t, err)
	v := out.(cty.Value)

	assert.True(t, v.GetAttr("id").RawEquals(cty.NumberIntVal(0)), "no-presence scalar -> zero")
	assert.Equal(t, "", v.GetAttr("name").AsString())
	assert.True(t, v.GetAttr("note").IsNull(), "proto3 optional unset -> null")
	assert.True(t, v.GetAttr("a").IsNull(), "oneof member unset -> null")
}

func TestNative_UnknownFieldErrors(t *testing.T) {
	f := formatFor(t, ordersFDS(t), "acme.test.v1.Order", modeNative)
	_, err := f.Serialize(cty.ObjectVal(map[string]cty.Value{
		"nope": cty.StringVal("x"),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestNative_EnumByNameOrNumber(t *testing.T) {
	f := formatFor(t, ordersFDS(t), "acme.test.v1.Order", modeNative)

	for _, in := range []cty.Value{cty.StringVal("CLOSED"), cty.NumberIntVal(2)} {
		bin, err := f.Serialize(cty.ObjectVal(map[string]cty.Value{"status": in}))
		require.NoError(t, err)
		out, err := f.Deserialize(bin)
		require.NoError(t, err)
		assert.Equal(t, "CLOSED", out.(cty.Value).GetAttr("status").AsString())
	}
}

func TestNative_WKT(t *testing.T) {
	f := formatFor(t, wktFDS(t), "acme.test.v1.WktMsg", modeNative)

	in := cty.ObjectVal(map[string]cty.Value{
		"ts":     cty.StringVal("2026-07-21T12:00:00Z"),
		"dur":    cty.StringVal("1.5s"),
		"i64w":   cty.NumberIntVal(7),
		"strw":   cty.StringVal("wrapped"),
		"bytesw": bytescty.BuildBytesObject([]byte{9}, "application/octet-stream"),
		"mask":   cty.ListVal([]cty.Value{cty.StringVal("a.b"), cty.StringVal("c")}),
		"empty":  cty.EmptyObjectVal,
		"raw":    bytescty.BuildBytesObject([]byte{7, 8}, "application/octet-stream"),
	})
	bin, err := f.Serialize(in)
	require.NoError(t, err)
	out, err := f.Deserialize(bin)
	require.NoError(t, err)
	v := out.(cty.Value)

	// Timestamp -> Time capsule.
	assert.Equal(t, timecty.TimeCapsuleType, v.GetAttr("ts").Type())
	// Duration -> Duration capsule.
	assert.Equal(t, timecty.DurationCapsuleType, v.GetAttr("dur").Type())
	// wrappers unwrap to scalars.
	assert.True(t, v.GetAttr("i64w").RawEquals(cty.NumberIntVal(7)))
	assert.Equal(t, "wrapped", v.GetAttr("strw").AsString())
	// FieldMask -> list of strings.
	assert.Equal(t, 2, v.GetAttr("mask").LengthInt())
	// Empty -> {}.
	assert.True(t, v.GetAttr("empty").RawEquals(cty.EmptyObjectVal))
}
