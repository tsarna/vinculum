//go:build integration && (linux || darwin || freebsd)

package config_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPluginLoader_LoadsRealSO builds the fixture plugin and a real
// (non-test) vinculum binary, then runs `vinculum check` pointed at a
// configuration that declares the plugin and calls a function the plugin
// registered.
//
// The host has to be a non-test binary: the test binary that `go test`
// produces is compiled with extra test-only instrumentation, which makes
// Go's plugin loader reject any .so built from the same source tree as
// "different version of package X". This mirrors how the production
// `vinculum` binary loads plugins, so it's the more useful check anyway.
//
// Run with: go test -tags integration ./config/...
func TestPluginLoader_LoadsRealSO(t *testing.T) {
	tmp := t.TempDir()

	binPath := filepath.Join(tmp, "vinculum")
	buildBin := exec.Command("go", "build", "-o", binPath, "github.com/tsarna/vinculum")
	buildBin.Stderr = os.Stderr
	require.NoError(t, buildBin.Run(), "go build of host binary failed")

	pluginsDir := filepath.Join(tmp, "plugins")
	require.NoError(t, os.MkdirAll(pluginsDir, 0o755))
	soPath := filepath.Join(pluginsDir, "sample.so")
	buildPlugin := exec.Command("go", "build",
		"-buildmode=plugin",
		"-o", soPath,
		"github.com/tsarna/vinculum/config/testdata/plugins/sample")
	buildPlugin.Stderr = os.Stderr
	require.NoError(t, buildPlugin.Run(), "go build -buildmode=plugin failed")

	configDir := filepath.Join(tmp, "conf")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "boot.vinit"),
		[]byte(`plugin "sample" {}`),
		0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "main.vcl"),
		[]byte(`assert "plugin_fn_works" {
    condition = vinculum_plugin_integration_test_hello() == "hello from plugin"
}`),
		0o644))

	check := exec.Command(binPath, "check", "--plugin-path", pluginsDir, configDir)
	out, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("vinculum check failed: %v\noutput:\n%s", err, out)
	}

	t.Run("schema describes plugin-contributed types", func(t *testing.T) {
		schema := exec.Command(binPath, "schema", "--plugin-path", pluginsDir, configDir)
		out, err := schema.Output()
		if err != nil {
			t.Fatalf("vinculum schema failed: %v", err)
		}

		var doc struct {
			Plugins []string `json:"plugins"`
			Blocks  map[string]struct {
				Variants map[string]struct {
					Summary    string `json:"summary"`
					Attributes []struct {
						Name     string `json:"name"`
						Required bool   `json:"required"`
						Summary  string `json:"summary"`
						Hint     string `json:"hint"`
					} `json:"attributes"`
				} `json:"variants"`
			} `json:"blocks"`
		}
		require.NoError(t, json.Unmarshal(out, &doc))

		// Only what the plugin added, not the whole registry — both of its
		// contributions, the block type and the function family.
		require.Equal(t, []string{
			"client.plugin_sample",
			"functions.vinculum_plugin_integration_test",
		}, doc.Plugins)

		variant, ok := doc.Blocks["client"].Variants["plugin_sample"]
		require.True(t, ok, "plugin client type missing from schema")
		require.Equal(t, "Integration-test fixture client contributed by a plugin.", variant.Summary)

		attrs := map[string]bool{}
		for _, a := range variant.Attributes {
			attrs[a.Name] = a.Required
			require.NotEmpty(t, a.Summary, "attribute %q has no summary", a.Name)
		}
		// The plugin's own attributes, plus `disabled` from the client envelope.
		require.True(t, attrs["greeting"], "greeting should be required")
		require.Contains(t, attrs, "loud")
		require.Contains(t, attrs, "disabled")
	})

	// Without --plugin-path the same binary describes a stock binary, so the
	// plugin's type is absent and there is no plugins key at all.
	t.Run("schema without plugins is unchanged", func(t *testing.T) {
		schema := exec.Command(binPath, "schema")
		out, err := schema.Output()
		require.NoError(t, err)

		var doc struct {
			Plugins []string `json:"plugins"`
			Blocks  map[string]struct {
				Variants map[string]any `json:"variants"`
			} `json:"blocks"`
		}
		require.NoError(t, json.Unmarshal(out, &doc))
		require.Empty(t, doc.Plugins)
		require.NotContains(t, doc.Blocks["client"].Variants, "plugin_sample")
	})
}
