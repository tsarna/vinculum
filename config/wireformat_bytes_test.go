package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bytescty "github.com/tsarna/bytes-cty-type"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
)

// bytesOf asserts val is a rich bytes object and returns its payload.
func bytesOf(t *testing.T, val any) []byte {
	t.Helper()
	cv, ok := val.(cty.Value)
	require.True(t, ok, "expected a cty.Value, got %T", val)
	b, err := bytescty.GetBytesFromValue(cv)
	require.NoError(t, err, "expected a bytes value, got %#v", cv)
	return b.Data
}

func TestCtyWireFormat_BytesFormatYieldsBytesObject(t *testing.T) {
	// Previously go2cty2go stringified []byte, so VCL saw text where the
	// format deliberately produced binary.
	wf := &CtyWireFormat{Inner: wire.Bytes}

	got, err := wf.Deserialize([]byte("not json {{"))
	require.NoError(t, err)
	assert.Equal(t, []byte("not json {{"), bytesOf(t, got))
}

func TestCtyWireFormat_AutoBytesSplitsJSONFromBinary(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.AutoBytes}

	// JSON still decodes to a normal cty value...
	got, err := wf.Deserialize([]byte(`{"a":1}`))
	require.NoError(t, err)
	cv, ok := got.(cty.Value)
	require.True(t, ok)
	// A decoded JSON record is an object regardless of whether its values
	// share a type.
	require.True(t, cv.Type().IsObjectType(), "JSON must decode, not become bytes; got %s", cv.Type().FriendlyName())
	// cty numbers carry big.Float internals, so compare with RawEquals
	// rather than reflect-based equality.
	assert.True(t, cty.NumberIntVal(1).RawEquals(cv.GetAttr("a")))

	// ...while non-JSON becomes bytes rather than a string.
	got, err = wf.Deserialize([]byte("not json {{"))
	require.NoError(t, err)
	assert.Equal(t, []byte("not json {{"), bytesOf(t, got))
}

func TestCtyWireFormat_AutoStillYieldsString(t *testing.T) {
	// auto is unchanged — this is what distinguishes it from auto_bytes.
	wf := &CtyWireFormat{Inner: wire.Auto}

	got, err := wf.Deserialize([]byte("not json {{"))
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("not json {{"), got)
}

func TestCtyWireFormat_BinaryIsPreservedNotMangled(t *testing.T) {
	raw := []byte{0xff, 0xfe, 0x00, 0x01, 0x80}
	wf := &CtyWireFormat{Inner: wire.AutoBytes}

	got, err := wf.Deserialize(raw)
	require.NoError(t, err)
	assert.Equal(t, raw, bytesOf(t, got), "arbitrary binary must survive the cty boundary")
}

func TestCtyWireFormat_BytesObjectRoundTrips(t *testing.T) {
	// A payload received as bytes must be sendable again unchanged.
	// Without the Serialize-side handling, CtyToAny would flatten the
	// object into its attributes and emit JSON garbage.
	raw := []byte{0xde, 0xad, 0xbe, 0xef}

	for _, name := range []string{"auto_bytes", "bytes", "auto"} {
		t.Run(name, func(t *testing.T) {
			wf := &CtyWireFormat{Inner: wire.ByName(name)}

			decoded, err := wf.Deserialize(raw)
			require.NoError(t, err)

			out, err := wf.Serialize(decoded)
			require.NoError(t, err)
			assert.Equal(t, raw, out, "%s: round-trip must be byte-identical", name)
		})
	}
}

func TestCtyWireFormat_SerializeStringAcceptsBytesObject(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.AutoBytes}

	s, err := wf.SerializeString(bytescty.BuildBytesObject([]byte("hello"), "text/plain"))
	require.NoError(t, err)
	assert.Equal(t, "hello", s)
}

func TestCtyWireFormat_SerializeNonBytesUnaffected(t *testing.T) {
	// The bytes fast-path must not intercept ordinary values.
	wf := &CtyWireFormat{Inner: wire.JSON}

	out, err := wf.Serialize(cty.ObjectVal(map[string]cty.Value{"a": cty.NumberIntVal(1)}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(out))

	out, err = wf.Serialize(cty.StringVal("plain"))
	require.NoError(t, err)
	assert.JSONEq(t, `"plain"`, string(out))
}

func TestCtyWireFormat_SerializeNullAndUnknownUnaffected(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.Auto}

	// rawBytesFromCty must reject a null rather than treating it as bytes.
	assert.NotPanics(t, func() { _, _ = wf.Serialize(cty.NullVal(cty.String)) })

	// An unknown reaches go2cty2go.CtyToAny, which reports it as an error
	// (it used to panic; fixed in go2cty2go v0.1.4). rawBytesFromCty
	// returns early for unknowns, so they take the normal conversion path.
	_, err := wf.Serialize(cty.UnknownVal(cty.String))
	assert.ErrorContains(t, err, "unknown")
}

func TestGetWireFormatFromValue_AutoBytesByName(t *testing.T) {
	wf, err := GetWireFormatFromValue(cty.StringVal("auto_bytes"))
	require.NoError(t, err)
	assert.Equal(t, "auto_bytes", wf.Name())
}
