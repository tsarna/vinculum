package protobuf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestJSONMode_Order(t *testing.T) {
	f := formatFor(t, ordersFDS(t), "acme.test.v1.Order", modeJSON)

	in := cty.ObjectVal(map[string]cty.Value{
		"id":     cty.NumberIntVal(42),
		"name":   cty.StringVal("x"),
		"status": cty.StringVal("ACTIVE"),
	})
	bin, err := f.Serialize(in)
	require.NoError(t, err)
	out, err := f.Deserialize(bin)
	require.NoError(t, err)
	v := out.(cty.Value)

	// protojson renders 64-bit ints as strings.
	assert.Equal(t, "42", v.GetAttr("id").AsString())
	assert.Equal(t, "x", v.GetAttr("name").AsString())
	assert.Equal(t, "ACTIVE", v.GetAttr("status").AsString())
}

func TestJSONMode_WKT(t *testing.T) {
	f := formatFor(t, wktFDS(t), "acme.test.v1.WktMsg", modeJSON)

	in := cty.ObjectVal(map[string]cty.Value{
		"ts":  cty.StringVal("2026-07-21T12:00:00Z"),
		"dur": cty.StringVal("1.5s"),
		"raw": cty.StringVal("AAEC"), // base64 of {0,1,2}
	})
	bin, err := f.Serialize(in)
	require.NoError(t, err)
	out, err := f.Deserialize(bin)
	require.NoError(t, err)
	v := out.(cty.Value)

	// json mode: timestamp/duration are strings, bytes is base64.
	assert.Equal(t, "2026-07-21T12:00:00Z", v.GetAttr("ts").AsString())
	assert.Equal(t, "1.500s", normalizeDur(v.GetAttr("dur").AsString()))
	assert.Equal(t, "AAEC", v.GetAttr("raw").AsString())
}

// protojson may render 1.5s as "1.500s"; accept either.
func normalizeDur(s string) string {
	if s == "1.5s" {
		return "1.500s"
	}
	return s
}
