package cmd

import (
	"bytes"
	"encoding/json"
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
	checkFormat = "text"

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

// The machine-readable form is what an editor drawing squiggles reads, so what
// it needs is pinned: a severity, the message, and a range per diagnostic — on
// stdout, uncontaminated by the text form or the "Configuration is valid." line.
func TestCheckJSONFormat(t *testing.T) {
	src := `bus "main" {}

subscription "s" {
  target = bus.main
  topics = ["in"]
  action = nosuchfn(ctx.msg)
}
`
	stdout, stderr, err := runCheckCmd(t, map[string]string{"bad.vcl": src}, "--format", "json")

	require.Error(t, err)
	assert.Equal(t, 1, ExitCode(err))
	assert.True(t, Reported(err), "the report is the explanation")
	assert.Empty(t, stderr, "diagnostics go to stdout in this format, not both")

	var report struct {
		Valid       bool `json:"valid"`
		Diagnostics []struct {
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
			Detail   string `json:"detail"`
			Location *struct {
				File      string `json:"file"`
				Line      int    `json:"line"`
				Column    int    `json:"column"`
				EndLine   int    `json:"end_line"`
				EndColumn int    `json:"end_column"`
			} `json:"location"`
		} `json:"diagnostics"`
		Summary struct {
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), "stdout is JSON and nothing else")

	assert.False(t, report.Valid)
	assert.Equal(t, 1, report.Summary.Errors)
	require.Len(t, report.Diagnostics, 1)

	d := report.Diagnostics[0]
	assert.Equal(t, "error", d.Severity)
	assert.Equal(t, "Call to unknown function", d.Summary)
	assert.Contains(t, d.Detail, `There is no function named "nosuchfn".`)
	require.NotNil(t, d.Location, "a squiggle needs somewhere to go")
	assert.Equal(t, "bad.vcl", filepath.Base(d.Location.File))
	assert.Equal(t, 6, d.Location.Line, "the action's line")
	assert.Equal(t, 6, d.Location.EndLine)
	assert.Greater(t, d.Location.EndColumn, d.Location.Column, "a range, not a point")
}

// A warning does not make a configuration invalid, so the two are reported
// separately rather than by counting diagnostics.
func TestCheckJSONWarningsAreValid(t *testing.T) {
	src := "var \"v\" {\n  type  = \"string\"\n  value = \"hi\"\n}\n"
	stdout, _, err := runCheckCmd(t, map[string]string{"warn.vcl": src}, "--format", "json")

	require.NoError(t, err, "warnings do not fail the check")

	var report struct {
		Valid       bool `json:"valid"`
		Diagnostics []struct {
			Severity string `json:"severity"`
		} `json:"diagnostics"`
		Summary struct {
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &report))

	assert.True(t, report.Valid)
	assert.Equal(t, 0, report.Summary.Errors)
	assert.Equal(t, 1, report.Summary.Warnings)
	require.Len(t, report.Diagnostics, 1)
	assert.Equal(t, "warning", report.Diagnostics[0].Severity)
}

// A clean configuration still emits a report: a consumer parses one shape
// whatever the answer, rather than an empty stream meaning success.
func TestCheckJSONValidConfig(t *testing.T) {
	stdout, _, err := runCheckCmd(t, map[string]string{"ok.vcl": "bus \"main\" {}\n"}, "--format", "json")

	require.NoError(t, err)
	assert.NotContains(t, stdout, "Configuration is valid.", "the text form's line has no place in the JSON")

	var report struct {
		Valid       bool  `json:"valid"`
		Diagnostics []any `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &report))
	assert.True(t, report.Valid)
	assert.NotNil(t, report.Diagnostics, "an empty list, not null")
	assert.Empty(t, report.Diagnostics)
}

func TestCheckUnknownFormatIsAUsageError(t *testing.T) {
	_, _, err := runCheckCmd(t, map[string]string{"ok.vcl": "bus \"main\" {}\n"}, "--format", "yaml")

	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	assert.Contains(t, err.Error(), "want text or json")
}

// A path that does not exist is a diagnostic, not a nil-pointer panic: the
// failed os.Stat left info nil and the code read info.IsDir() anyway.
func TestCheckMissingPathIsReported(t *testing.T) {
	logLevel, filePath, writePath, pluginPath = "error", "", "", ""
	checkFormat = "text"

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
