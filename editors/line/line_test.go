package line

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// testEvalCtx returns an HCL eval context with a basic set of functions for tests.
func testEvalCtx() *hcl.EvalContext {
	return &hcl.EvalContext{
		Functions: map[string]function.Function{
			"tostring": stdlib.MakeToFunc(cty.String),
		},
	}
}

// buildEditor parses hclSrc as the body content of an editor block and returns
// the compiled function. paramNames are the editor's declared parameter names.
func buildEditor(t *testing.T, writeDir, hclSrc string, paramNames ...string) function.Function {
	t.Helper()
	file, diags := hclsyntax.ParseConfig([]byte(hclSrc), "test.hcl", hcl.InitialPos)
	require.False(t, diags.HasErrors(), "HCL parse error: %v", diags)

	config := &cfg.Config{WriteDir: writeDir}
	evalCtxFn := func() *hcl.EvalContext { return testEvalCtx() }

	def := &cfg.EditorDefinition{
		Type:     "line",
		Name:     "test",
		Params:   paramNames,
		Body:     file.Body,
		DefRange: hcl.Range{Filename: "test.hcl"},
	}

	fn, diags := processLineEditor(config, evalCtxFn, def)
	require.False(t, diags.HasErrors(), "processLineEditor error: %v", diags)
	return fn
}

// buildEditorExpectError builds an editor and asserts that it produces a config-time error.
func buildEditorExpectError(t *testing.T, writeDir, hclSrc string) hcl.Diagnostics {
	t.Helper()
	file, diags := hclsyntax.ParseConfig([]byte(hclSrc), "test.hcl", hcl.InitialPos)
	require.False(t, diags.HasErrors(), "HCL parse error: %v", diags)

	config := &cfg.Config{WriteDir: writeDir}
	evalCtx := &hcl.EvalContext{}
	evalCtxFn := func() *hcl.EvalContext { return evalCtx }

	def := &cfg.EditorDefinition{
		Type:     "line",
		Name:     "test",
		Body:     file.Body,
		DefRange: hcl.Range{Filename: "test.hcl"},
	}

	_, diags = processLineEditor(config, evalCtxFn, def)
	require.True(t, diags.HasErrors(), "expected config-time error, got none")
	return diags
}

// ctxVal creates a cty value suitable for the ctx parameter.
func ctxVal(t *testing.T) cty.Value {
	t.Helper()
	val, err := richcty.NewContextObject(context.Background()).Build()
	require.NoError(t, err)
	return val
}

// callString calls a string-mode editor function and returns the result string or error.
func callString(t *testing.T, fn function.Function, input string, extraArgs ...cty.Value) (string, error) {
	t.Helper()
	args := []cty.Value{ctxVal(t), cty.StringVal(input)}
	args = append(args, extraArgs...)
	result, err := fn.Call(args)
	if err != nil {
		return "", err
	}
	return result.AsString(), nil
}

// callFile calls a file-mode editor function and returns changed (true/false) or error.
func callFile(t *testing.T, fn function.Function, path string, extraArgs ...cty.Value) (bool, error) {
	t.Helper()
	args := []cty.Value{ctxVal(t), cty.StringVal(path)}
	args = append(args, extraArgs...)
	result, err := fn.Call(args)
	if err != nil {
		return false, err
	}
	return result.True(), nil
}

// readFile is a test helper that reads a file and returns its content as a string.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// writeFile is a test helper that writes a string to a file.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

// --- String mode tests ---

func TestStringModeBasicReplace(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "foo" {
    replace = "bar\n"
}
`)
	out, err := callString(t, fn, "foo\nbaz\n")
	require.NoError(t, err)
	assert.Equal(t, "bar\nbaz\n", out)
}

func TestStringModeNoMatch(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "NOTPRESENT" {
    replace = "replaced\n"
}
`)
	out, err := callString(t, fn, "hello\nworld\n")
	require.NoError(t, err)
	assert.Equal(t, "hello\nworld\n", out)
}

func TestStringModeMultipleRules(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "aaa" {
    replace = "AAA\n"
}
match "bbb" {
    replace = "BBB\n"
}
`)
	out, err := callString(t, fn, "aaa\nbbb\nccc\n")
	require.NoError(t, err)
	assert.Equal(t, "AAA\nBBB\nccc\n", out)
}

func TestStringModeFirstRuleWins(t *testing.T) {
	// When multiple rules could match a line, the first one wins.
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    replace = "FIRST\n"
}
match "x" {
    replace = "SECOND\n"
}
`)
	out, err := callString(t, fn, "x\n")
	require.NoError(t, err)
	assert.Equal(t, "FIRST\n", out)
}

func TestStringModeMaxConstraint(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    max     = 2
    replace = "X\n"
}
`)
	// Three lines match, but max = 2 so only first two are replaced.
	out, err := callString(t, fn, "x\nx\nx\n")
	require.NoError(t, err)
	assert.Equal(t, "X\nX\nx\n", out)
}

func TestStringModeRequiredMet(t *testing.T) {
	// required = true with a matching line — should succeed.
	fn := buildEditor(t, "", `
mode = "string"
match "TARGET" {
    required = true
    replace  = "FOUND\n"
}
`)
	out, err := callString(t, fn, "TARGET\n")
	require.NoError(t, err)
	assert.Equal(t, "FOUND\n", out)
}

func TestStringModeRequiredNotMet(t *testing.T) {
	// required = true but pattern never matches — soft abort → error in string mode.
	fn := buildEditor(t, "", `
mode = "string"
match "NOTHERE" {
    required = true
}
`)
	_, err := callString(t, fn, "hello\n")
	assert.Error(t, err)
}

func TestStringModeRequiredCount(t *testing.T) {
	// required = 3 but only two lines match.
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    required = 3
}
`)
	_, err := callString(t, fn, "x\nx\n")
	assert.Error(t, err)
}

func TestStringModeWhenTrue(t *testing.T) {
	// when = true — rule always applies.
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    when    = 1 > 0
    replace = "X\n"
}
`)
	out, err := callString(t, fn, "x\n")
	require.NoError(t, err)
	assert.Equal(t, "X\n", out)
}

func TestStringModeWhenFalse(t *testing.T) {
	// when = false — rule never applies even when regex matches.
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    when    = 1 > 2
    replace = "X\n"
}
`)
	out, err := callString(t, fn, "x\n")
	require.NoError(t, err)
	assert.Equal(t, "x\n", out)
}

func TestStringModeWhenFalseContinues(t *testing.T) {
	// when = false — rule never applies even when regex matches, but later matches do
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    when    = 1 > 2
    replace = "X\n"
}
match "x" {
    when    = 2 > 1
	replace = "Y\n"
}
`)
	out, err := callString(t, fn, "x\n")
	require.NoError(t, err)
	assert.Equal(t, "Y\n", out)
}

func TestStringModeAbort(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "ABORT" {
    abort = true
}
`)
	_, err := callString(t, fn, "ABORT\n")
	assert.Error(t, err)
}

func TestStringModeAbortNotTriggered(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "ABORT" {
    abort = true
}
`)
	out, err := callString(t, fn, "safe\n")
	require.NoError(t, err)
	assert.Equal(t, "safe\n", out)
}

func TestStringModeBeforeBlock(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
before {
    content = "# HEADER\n"
}
match "x" {
    replace = "X\n"
}
`)
	out, err := callString(t, fn, "x\nbody\n")
	require.NoError(t, err)
	assert.Equal(t, "# HEADER\nX\nbody\n", out)
}

func TestStringModeAfterBlock(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
after {
    content = "# FOOTER\n"
}
`)
	out, err := callString(t, fn, "body\n")
	require.NoError(t, err)
	assert.Equal(t, "body\n# FOOTER\n", out)
}

func TestStringModeBeforeAndAfter(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
before {
    content = "START\n"
}
after {
    content = "END\n"
}
`)
	out, err := callString(t, fn, "middle\n")
	require.NoError(t, err)
	assert.Equal(t, "START\nmiddle\nEND\n", out)
}

func TestStringModeState(t *testing.T) {
	// State accumulates across lines; after block sees final state.
	fn := buildEditor(t, "", `
mode = "string"
state = { count = 0 }
match "x" {
    update_state = { count = state.count + 1 }
}
after {
    content = "total: ${tostring(state.count)}\n"
}
`)
	out, err := callString(t, fn, "x\ny\nx\n")
	require.NoError(t, err)
	assert.Equal(t, "x\ny\nx\ntotal: 2\n", out)
}

func TestStringModeBeforeWithAccumulatedState(t *testing.T) {
	// before block references state accumulated during line processing.
	// This is the dynamic-prepend-with-state feature.
	fn := buildEditor(t, "", `
mode = "string"
state = { found = false }
match "TARGET" {
    update_state = { found = true }
}
before {
    content = "found=${tostring(state.found)}\n"
}
`)
	// TARGET appears in the body, so before sees found=true despite before coming first in output.
	out, err := callString(t, fn, "preamble\nTARGET\npostamble\n")
	require.NoError(t, err)
	assert.Equal(t, "found=true\npreamble\nTARGET\npostamble\n", out)
}

func TestStringModeBeforeWithStateNotFound(t *testing.T) {
	// before block with state where the pattern never matches.
	fn := buildEditor(t, "", `
mode = "string"
state = { found = false }
match "TARGET" {
    update_state = { found = true }
}
before {
    content = "found=${tostring(state.found)}\n"
}
`)
	out, err := callString(t, fn, "no match here\n")
	require.NoError(t, err)
	assert.Equal(t, "found=false\nno match here\n", out)
}

func TestStringModeWhenWithState(t *testing.T) {
	// when expression has access to current state.
	fn := buildEditor(t, "", `
mode = "string"
state = { active = true }
match "x" {
    when         = state.active
    replace      = "X\n"
    update_state = { active = false }
}
`)
	// First "x" matches (state.active = true), replaced; second "x" skipped (state.active = false).
	out, err := callString(t, fn, "x\nx\n")
	require.NoError(t, err)
	assert.Equal(t, "X\nx\n", out)
}

func TestStringModeUserParams(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "PLACEHOLDER" {
    replace = replacement
}
`, "replacement")
	out, err := callString(t, fn, "PLACEHOLDER\n", cty.StringVal("SUBSTITUTED\n"))
	require.NoError(t, err)
	assert.Equal(t, "SUBSTITUTED\n", out)
}

func TestStringModeCaptureGroups(t *testing.T) {
	// ctx.groups[0] is the full match; ctx.groups[1] is first capture group.
	fn := buildEditor(t, "", `
mode = "string"
match "hello (\\w+)" {
    replace = "hi ${ctx.groups[1]}\n"
}
`)
	out, err := callString(t, fn, "hello world\n")
	require.NoError(t, err)
	assert.Equal(t, "hi world\n", out)
}

func TestStringModeNamedGroups(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "(?P<name>\\w+)=(?P<val>\\w+)" {
    replace = "${ctx.named.name}: ${ctx.named.val}\n"
}
`)
	out, err := callString(t, fn, "key=value\n")
	require.NoError(t, err)
	assert.Equal(t, "key: value\n", out)
}

func TestStringModeMatchCount(t *testing.T) {
	// ctx.count tracks how many times this rule has fired so far (including this match).
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    replace = "${tostring(ctx.count)}\n"
}
`)
	out, err := callString(t, fn, "x\nx\nx\n")
	require.NoError(t, err)
	assert.Equal(t, "1\n2\n3\n", out)
}

func TestStringModeMatchWithoutReplace(t *testing.T) {
	// A match rule with no replace only counts for required; line is passed through.
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    required = true
}
`)
	out, err := callString(t, fn, "x\n")
	require.NoError(t, err)
	assert.Equal(t, "x\n", out)
}

func TestStringModeEmptyInput(t *testing.T) {
	fn := buildEditor(t, "", `
mode = "string"
match "x" {
    replace = "X\n"
}
`)
	out, err := callString(t, fn, "")
	require.NoError(t, err)
	assert.Equal(t, "", out)
}

func TestStringModeUpdateStateInReplace(t *testing.T) {
	// update_state is applied after replace; subsequent rules/lines see new state.
	fn := buildEditor(t, "", `
mode = "string"
state = { last = "" }
match "\\w+" {
    replace      = "${ctx.groups[0]}\n"
    update_state = { last = ctx.groups[0] }
}
after {
    content = "last=${state.last}\n"
}
`)
	out, err := callString(t, fn, "alpha\nbeta\n")
	require.NoError(t, err)
	assert.Equal(t, "alpha\nbeta\nlast=beta\n", out)
}

// --- File mode tests ---

func TestFileModeBasicReplace(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "foo" {
    replace = "bar\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "foo\nbaz\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "bar\nbaz\n", readFile(t, path))
}

func TestFileModeNoChange(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "NOTPRESENT" {
    replace = "replaced\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "hello\nworld\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, "hello\nworld\n", readFile(t, path))
}

func TestFileModeCreateIfAbsent(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
create_if_absent = true
after {
    content = "created\n"
}
`)
	path := filepath.Join(dir, "new.txt")
	require.NoFileExists(t, path)

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "created\n", readFile(t, path))
}

func TestFileModeCreateIfAbsentFalse(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "x" {
    replace = "X\n"
}
`)
	path := filepath.Join(dir, "missing.txt")
	_, err := callFile(t, fn, path)
	assert.Error(t, err)
}

func TestFileModeBackup(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
backup = "~"
match "old" {
    replace = "new\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "old\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "new\n", readFile(t, path))
	assert.Equal(t, "old\n", readFile(t, path+"~"), "backup should contain original content")
}

func TestFileModeBackupHardLink(t *testing.T) {
	// Backup is a hard link, so modifying the original doesn't affect the backup.
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
backup = ".bak"
match "v1" {
    replace = "v2\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "v1\n")

	_, err := callFile(t, fn, path)
	require.NoError(t, err)

	// Verify both exist and content is correct.
	assert.Equal(t, "v2\n", readFile(t, path))
	assert.Equal(t, "v1\n", readFile(t, path+".bak"))
}

func TestFileModeRequiredNotMet(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "MUSTEXIST" {
    required = true
    replace  = "FOUND\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "something else\n")
	original := readFile(t, path)

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.False(t, changed)
	// File must not have been modified.
	assert.Equal(t, original, readFile(t, path))
}

func TestFileModeAbort(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "STOP" {
    abort = true
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "STOP\n")
	original := readFile(t, path)

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, original, readFile(t, path))
}

func TestFileModePathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "x" {
    replace = "X\n"
}
`)
	_, err := callFile(t, fn, "../escape.txt")
	assert.Error(t, err)
}

func TestFileModePathOutsideBaseDirRejected(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "x" { replace = "X\n" }
`)
	_, err := callFile(t, fn, "/etc/passwd")
	assert.Error(t, err)
}

func TestFileModeBeforeBlock(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
before {
    content = "# HEADER\n"
}
match "x" {
    replace = "X\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "x\nbody\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "# HEADER\nX\nbody\n", readFile(t, path))
}

func TestFileModeAfterBlock(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
after {
    content = "# FOOTER\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "body\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "body\n# FOOTER\n", readFile(t, path))
}

func TestFileModeBeforeWithAccumulatedState(t *testing.T) {
	// Two-pass: before sees state accumulated during line processing.
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
state = { count = 0 }
match "item" {
    update_state = { count = state.count + 1 }
}
before {
    content = "# Items: ${tostring(state.count)}\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "item\nitem\nother\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "# Items: 2\nitem\nitem\nother\n", readFile(t, path))
}

func TestFileModeStateAfterBlock(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
state = { total = 0 }
match "\\d+" {
    replace      = "${ctx.groups[0]}\n"
    update_state = { total = state.total + 1 }
}
after {
    content = "# Count: ${tostring(state.total)}\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "42\nhello\n99\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "42\nhello\n99\n# Count: 2\n", readFile(t, path))
}

func TestFileModePermissionsPreserved(t *testing.T) {
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "x" {
    replace = "X\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "x\n")
	require.NoError(t, os.Chmod(path, 0600))

	_, err := callFile(t, fn, path)
	require.NoError(t, err)

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
}

// --- Config-time error tests ---

func TestConfigErrorInvalidMode(t *testing.T) {
	diags := buildEditorExpectError(t, "", `mode = "invalid"`)
	assert.Contains(t, diags.Error(), "Invalid editor mode")
}

func TestConfigErrorFileModeRequiresWritefiles(t *testing.T) {
	// mode = "file" (default) without a write dir set.
	diags := buildEditorExpectError(t, "", `
match "x" { replace = "X\n" }
`)
	assert.Contains(t, diags.Error(), "writefiles")
}

func TestConfigErrorStringModeNoWritefilesNeeded(t *testing.T) {
	// mode = "string" should succeed even with no write dir.
	fn := buildEditor(t, "", `
mode = "string"
match "x" { replace = "X\n" }
`)
	out, err := callString(t, fn, "x\n")
	require.NoError(t, err)
	assert.Equal(t, "X\n", out)
}

func TestConfigErrorInvalidRegex(t *testing.T) {
	diags := buildEditorExpectError(t, t.TempDir(), `
match "([unclosed" {
    replace = "x\n"
}
`)
	assert.Contains(t, diags.Error(), "Invalid regex")
}

// --- Incidental tests ---

func TestFileModeIncidentalOnlyNoWrite(t *testing.T) {
	// If all replacements are incidental, the file must not be written.
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "timestamp" {
    incidental = true
    replace    = "timestamp updated\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "timestamp\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, "timestamp\n", readFile(t, path), "file must not be modified")
}

func TestFileModeIncidentalPlusRealChange(t *testing.T) {
	// An incidental replacement plus a real change → file is written.
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
match "serial" {
    incidental = true
    replace    = "serial 2\n"
}
match "ip" {
    replace = "ip 10.0.0.2\n"
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "serial 1\nip 10.0.0.1\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "serial 2\nip 10.0.0.2\n", readFile(t, path))
}

func TestFileModeIncidentalBeforeOnlyNoWrite(t *testing.T) {
	// before block with incidental = true and no other change → no write.
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
before {
    content    = "# header\n"
    incidental = true
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "body\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, "body\n", readFile(t, path))
}

func TestFileModeIncidentalAfterOnlyNoWrite(t *testing.T) {
	// after block with incidental = true and no other change → no write.
	dir := t.TempDir()
	fn := buildEditor(t, dir, `
after {
    content    = "# footer\n"
    incidental = true
}
`)
	path := filepath.Join(dir, "test.txt")
	writeFile(t, path, "body\n")

	changed, err := callFile(t, fn, path)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, "body\n", readFile(t, path))
}
