package protobuf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// buildConfig writes the given fixture and .vcl into a temp dir and runs the
// full config pipeline, returning the resulting wire_format value (or diags).
func buildConfig(t *testing.T, fds []byte, vcl string) (cty.Value, hcl.Diagnostics) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.binpb"), fds, 0o644))
	vclPath := filepath.Join(dir, "config.vcl")
	require.NoError(t, os.WriteFile(vclPath, []byte(vcl), 0o644))

	config, diags := cfg.NewConfig().
		WithLogger(zap.NewNop()).
		WithFeature("readfiles", dir).
		WithSources(vclPath).
		Build()
	if diags.HasErrors() {
		return cty.NilVal, diags
	}
	return config.CtyWireFormatMap["wf"], diags
}

func TestBlock_SingleMessage(t *testing.T) {
	val, diags := buildConfig(t, ordersFDS(t), `
wire_format "protobuf" "wf" {
  descriptor_set = "schema.binpb"
  message        = "acme.test.v1.Order"
}
`)
	require.False(t, diags.HasErrors(), "%s", diags)

	wf, err := cfg.GetWireFormatFromValue(val)
	require.NoError(t, err)
	assert.Equal(t, "protobuf:acme.test.v1.Order", wf.Name())
}

func TestBlock_MultiMessageAliasing(t *testing.T) {
	val, diags := buildConfig(t, collideFDS(t), `
wire_format "protobuf" "wf" {
  descriptor_set = "schema.binpb"
}
`)
	require.False(t, diags.HasErrors(), "%s", diags)
	require.True(t, val.Type().IsObjectType())

	// Full-name index keys are always present.
	for _, full := range []string{"acme.a.Order", "acme.b.Order", "acme.c.Widget"} {
		assert.True(t, val.Type().HasAttribute(full), "missing full-name key %s", full)
	}
	// Unique short name gets an alias; colliding short name does not.
	assert.True(t, val.Type().HasAttribute("Widget"), "unique short name aliased")
	assert.False(t, val.Type().HasAttribute("Order"), "colliding short name not aliased")
}

func TestBlock_Errors(t *testing.T) {
	cases := map[string]string{
		"bad mode": `
wire_format "protobuf" "wf" {
  descriptor_set = "schema.binpb"
  message        = "acme.test.v1.Order"
  mode           = "xml"
}`,
		"unknown message": `
wire_format "protobuf" "wf" {
  descriptor_set = "schema.binpb"
  message        = "acme.test.v1.Nope"
}`,
		"missing file": `
wire_format "protobuf" "wf" {
  descriptor_set = "does-not-exist.binpb"
  message        = "acme.test.v1.Order"
}`,
		"unknown attribute": `
wire_format "protobuf" "wf" {
  descriptor_set = "schema.binpb"
  bogus          = true
}`,
	}
	for name, vcl := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := buildConfig(t, ordersFDS(t), vcl)
			assert.True(t, diags.HasErrors(), "expected diagnostics for %s", name)
		})
	}
}
