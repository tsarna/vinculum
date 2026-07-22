package protobuf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bytescty "github.com/tsarna/bytes-cty-type"
	"github.com/zclconf/go-cty/cty"
)

func TestAny_KnownTypeUnpacked(t *testing.T) {
	f := formatFor(t, anyFDS(t), "acme.any.v1.Envelope", modeNative)

	in := cty.ObjectVal(map[string]cty.Value{
		"payload": cty.ObjectVal(map[string]cty.Value{
			"@type":    cty.StringVal("type.googleapis.com/acme.any.v1.Inner"),
			"greeting": cty.StringVal("hi"),
		}),
	})
	bin, err := f.Serialize(in)
	require.NoError(t, err)
	out, err := f.Deserialize(bin)
	require.NoError(t, err)

	payload := out.(cty.Value).GetAttr("payload")
	assert.Equal(t, "hi", payload.GetAttr("greeting").AsString())
	assert.Equal(t, "type.googleapis.com/acme.any.v1.Inner", payload.GetAttr("@type").AsString())
}

func TestAny_UnknownTypeOpaque(t *testing.T) {
	f := formatFor(t, anyFDS(t), "acme.any.v1.Envelope", modeNative)

	in := cty.ObjectVal(map[string]cty.Value{
		"payload": cty.ObjectVal(map[string]cty.Value{
			"@type": cty.StringVal("type.googleapis.com/unknown.Foo"),
			"value": bytescty.BuildBytesObject([]byte{1, 2, 3}, "application/octet-stream"),
		}),
	})
	bin, err := f.Serialize(in)
	require.NoError(t, err)
	out, err := f.Deserialize(bin)
	require.NoError(t, err)

	payload := out.(cty.Value).GetAttr("payload")
	assert.Equal(t, "type.googleapis.com/unknown.Foo", payload.GetAttr("@type").AsString())
	b, err := bytescty.GetBytesFromValue(payload.GetAttr("value"))
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, b.Data)
}
