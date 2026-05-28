package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// writeVinit drops src into <tempdir>/<name>.vinit and returns the tempdir.
// The dir can be passed to WithSources directly.
func writeVinit(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".vinit"), []byte(src), 0o644))
	return dir
}

func TestVinit_NoFiles_NoOp(t *testing.T) {
	// Directory exists but has no .vinit content — Build should succeed
	// with whatever VCL is present (here, none).
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.vcl"), []byte(""), 0o644))

	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
}

func TestVinit_PluginWithoutPluginPath_Fatal(t *testing.T) {
	dir := writeVinit(t, "boot", `plugin "rabbitmq" {}`)

	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(), "expected error for missing --plugin-path")
	assert.Contains(t, diags.Error(), "requires --plugin-path")
}

func TestVinit_DisabledPluginShortCircuits(t *testing.T) {
	// A disabled plugin block should never invoke the loader — even with
	// no --plugin-path set and no .so file present, Build must succeed.
	dir := writeVinit(t, "boot", `plugin "nonexistent" { disabled = true }`)

	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
}

func TestVinit_DisabledViaEnv(t *testing.T) {
	t.Setenv("VINIT_DISABLE_TEST", "true")
	dir := writeVinit(t, "boot", `
plugin "nonexistent" {
    disabled = env.VINIT_DISABLE_TEST == "true"
}
`)

	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
}

func TestVinit_StdlibFunctionsAvailable(t *testing.T) {
	// upper() comes from the cty stdlib. The disabled expression should
	// evaluate using it.
	dir := writeVinit(t, "boot", `
plugin "nonexistent" {
    disabled = upper("yes") == "YES"
}
`)

	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
}

func TestVinit_InvalidLabelRejected(t *testing.T) {
	cases := []struct {
		name  string
		label string
	}{
		{"dot", "foo.bar"},
		{"slash", "foo/bar"},
		{"dotdot", ".."},
		{"leadinghyphen", "-foo"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := `plugin "` + tc.label + `" {}`
			dir := writeVinit(t, "boot", src)
			_, diags := config.NewConfig().
				WithSources(dir).
				WithLogger(zap.NewNop()).
				WithPluginPath("/nonexistent").
				Build()
			require.True(t, diags.HasErrors(), "expected error for label %q", tc.label)
			// The empty label is rejected by HCL itself as a parse error
			// rather than by the regex check; the diagnostic differs but
			// both are fatal which is what we care about.
			if tc.label != "" {
				assert.Contains(t, diags.Error(), "Invalid plugin label")
			}
		})
	}
}

func TestVinit_ValidLabelsAccepted(t *testing.T) {
	// These labels pass the regex; the loader then fails to find the .so
	// (we point at an empty tempdir). The diagnostic message confirms the
	// regex check passed and the loader was reached.
	cases := []string{"foo", "foo_bar", "foo-bar", "_x", "Foo123"}
	for _, label := range cases {
		t.Run(label, func(t *testing.T) {
			src := `plugin "` + label + `" {}`
			cfgDir := writeVinit(t, "boot", src)
			pluginsDir := t.TempDir()
			_, diags := config.NewConfig().
				WithSources(cfgDir).
				WithLogger(zap.NewNop()).
				WithPluginPath(pluginsDir).
				Build()
			require.True(t, diags.HasErrors(), "expected loader-level error for label %q", label)
			assert.NotContains(t, diags.Error(), "Invalid plugin label",
				"regex should accept %q", label)
		})
	}
}

func TestVinit_DuplicateLabelFatal(t *testing.T) {
	dir := writeVinit(t, "boot", `
plugin "dup" { disabled = true }
plugin "dup" { disabled = true }
`)
	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(), "expected duplicate-label diagnostic")
	assert.Contains(t, diags.Error(), "Duplicate plugin label")
}

func TestVinit_DuplicateLabelAcrossFilesFatal(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.vinit"),
		[]byte(`plugin "dup" { disabled = true }`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.vinit"),
		[]byte(`plugin "dup" { disabled = true }`), 0o644))

	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "Duplicate plugin label")
}

func TestVinit_UnknownBlockTypeFatal(t *testing.T) {
	dir := writeVinit(t, "boot", `
weather "tomorrow" {
    rain = true
}
`)
	_, diags := config.NewConfig().
		WithSources(dir).
		WithLogger(zap.NewNop()).
		Build()
	require.True(t, diags.HasErrors(), "expected unknown-block-type diagnostic")
}

func TestVinit_MissingSOFile_Fatal(t *testing.T) {
	cfgDir := writeVinit(t, "boot", `plugin "nope" {}`)
	pluginsDir := t.TempDir() // empty

	_, diags := config.NewConfig().
		WithSources(cfgDir).
		WithLogger(zap.NewNop()).
		WithPluginPath(pluginsDir).
		Build()
	require.True(t, diags.HasErrors(), "expected missing-so diagnostic")
	assert.Contains(t, diags.Error(), "Plugin file not found")
}
