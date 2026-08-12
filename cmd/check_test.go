package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCheckCmd runs `vinculum check` against a temp directory holding the given
// files (name → content) and returns its stdout, stderr, and error.
func runCheckCmd(t *testing.T, files map[string]string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}

	// Flags bind to package-level variables that cobra does not reset between runs.
	logLevel, filePath, writePath, pluginPath = "error", "", "", ""

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append([]string{"check"}, append(args, dir)...))
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

// `check` used to return its diagnostics as an error for main to render with
// %v, and hcl.Diagnostics.Error() is the first diagnostic plus a count — so a
// file with three errors printed one of them, with no quoted source line and
// an "and 2 other diagnostic(s)" tail.
func TestCheckReportsEveryDiagnosticWithSourceContext(t *testing.T) {
	src := `bus "main" {}

serverr "http" "web" {
  listen = ":8080"
}

whatever = 42
`
	_, stderr, err := runCheckCmd(t, map[string]string{"bad.vcl": src})

	require.Error(t, err)
	assert.True(t, Reported(err), "check has printed the diagnostics itself; main must not restate one of them")
	assert.Equal(t, 1, ExitCode(err))

	assert.NotContains(t, stderr, "other diagnostic(s)")
	// Both errors, each quoting its own line.
	assert.Contains(t, stderr, `Blocks of type "serverr" are not expected here.`)
	assert.Contains(t, stderr, `serverr "http" "web" {`)
	assert.Contains(t, stderr, `An argument named "whatever" is not expected here.`)
	assert.Contains(t, stderr, "whatever = 42")
	assert.Contains(t, stderr, "bad.vcl line 3")
	assert.Contains(t, stderr, "bad.vcl line 7")
}

// A .cty parse failure is reported against its own source, which reaches the
// file map by a different route (functy's own parser, not hclparse).
func TestCheckReportsFunctySourceContext(t *testing.T) {
	files := map[string]string{
		"ok.vcl": "bus \"main\" {}\n",
		"broken.cty": `func f(n) {
  return n +
}
`,
	}
	_, stderr, err := runCheckCmd(t, files)

	require.Error(t, err)
	assert.Contains(t, stderr, "broken.cty line")
}

// A .vinit failure is reported before any .vcl is parsed, so its file map is
// the vinit pass's own.
func TestCheckReportsVinitSourceContext(t *testing.T) {
	files := map[string]string{
		"ok.vcl":     "bus \"main\" {}\n",
		"boot.vinit": "plugn \"x\" {\n  path = \"x.so\"\n}\n",
	}
	_, stderr, err := runCheckCmd(t, files)

	require.Error(t, err)
	assert.Contains(t, stderr, "boot.vinit line 1")
	assert.Contains(t, stderr, `Did you mean "plugin"?`)
}

// Warnings are surfaced on a config that is otherwise valid — with the same
// source context as an error, and without failing the check.
func TestCheckReportsWarningsWithSourceContext(t *testing.T) {
	src := `var "v" {
  type  = "string"
  value = "hi"
}
`
	stdout, stderr, err := runCheckCmd(t, map[string]string{"warn.vcl": src})

	require.NoError(t, err)
	assert.Contains(t, stdout, "Configuration is valid.")
	assert.Contains(t, stderr, "Warning: Deprecated var type string")
	assert.Contains(t, stderr, "warn.vcl line 2")
}

// A path that does not exist is a diagnostic, not a nil-pointer panic: the
// failed os.Stat left info nil and the code read info.IsDir() anyway.
func TestCheckMissingPathIsReported(t *testing.T) {
	logLevel, filePath, writePath, pluginPath = "error", "", "", ""

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"check", filepath.Join(t.TempDir(), "nosuch.vcl")})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.True(t, strings.Contains(errOut.String(), "Failed to stat file"), "got %q", errOut.String())
}
