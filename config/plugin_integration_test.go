//go:build integration && (linux || darwin || freebsd)

package config_test

import (
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
}
