//go:build integration

package file

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// waitForRunCount polls until the trigger has fired at least once more than
// baseline, or returns false on timeout.
func waitForRunCount(trig *FileTrigger, baseline int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if trig.runCount.Load() > baseline {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// startTrigger builds a config from vcl, extracts the "watch" trigger, starts it,
// waits for the watcher to initialize, and registers Stop as a test cleanup.
func startTrigger(t *testing.T, vcl string) *FileTrigger {
	t.Helper()
	c, diags := withReadfiles(cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(testLogger(t))).Build()
	require.False(t, diags.HasErrors(), "config diagnostics: %v", diags)

	trig, err := GetFileTriggerFromCapsule(c.CtyTriggerMap["watch"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	t.Cleanup(func() { _ = trig.Stop() })

	// Allow the kernel-level watcher to register before we generate events.
	time.Sleep(100 * time.Millisecond)
	return trig
}

// TestFSEventCreate verifies that creating a new file fires a "create" event.
func TestFSEventCreate(t *testing.T) {
	dir := t.TempDir()
	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path   = %q
    events = ["create"]
    action = "triggered"
}`, dir))

	baseline := trig.runCount.Load()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello"), 0o644))

	assert.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected create event within 3s")
}

// TestFSEventWrite verifies that writing to an existing file fires a "write" event.
func TestFSEventWrite(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "existing.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("initial"), 0o644))

	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path   = %q
    events = ["write"]
    action = "triggered"
}`, dir))

	baseline := trig.runCount.Load()
	require.NoError(t, os.WriteFile(testFile, []byte("updated"), 0o644))

	assert.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected write event within 3s")
}

// TestFSEventDelete verifies that removing a file fires a "delete" event.
func TestFSEventDelete(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "to_delete.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("bye"), 0o644))

	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path   = %q
    events = ["delete"]
    action = "triggered"
}`, dir))

	baseline := trig.runCount.Load()
	require.NoError(t, os.Remove(testFile))

	assert.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected delete event within 3s")
}

// TestFSEventContextValues verifies that event_path and event are correctly set
// in the action eval context. Runtime variables are exposed under the "ctx"
// object, so the action expression references them as ctx.event_path, ctx.event.
func TestFSEventContextValues(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "ctx_test.txt")

	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path   = %q
    events = ["create"]
    action = ctx.event_path
}`, dir))

	baseline := trig.runCount.Load()
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	require.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected create event within 3s")

	trig.lastMu.RLock()
	result := trig.lastResult
	trig.lastMu.RUnlock()

	require.Equal(t, cty.String, result.Type())
	assert.Equal(t, testFile, result.AsString())
}

// TestFSEventRecursive verifies that events from subdirectories are delivered
// when recursive = true.
func TestFSEventRecursive(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(subdir, 0o755))

	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path      = %q
    events    = ["create"]
    recursive = true
    action    = "triggered"
}`, dir))

	baseline := trig.runCount.Load()
	require.NoError(t, os.WriteFile(filepath.Join(subdir, "deep.txt"), []byte("deep"), 0o644))

	assert.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected create event from subdirectory within 3s")
}

// TestFSEventRecursiveNewSubdir verifies that a subdirectory created after
// Start() is also watched when recursive = true.
func TestFSEventRecursiveNewSubdir(t *testing.T) {
	dir := t.TempDir()

	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path      = %q
    events    = ["create"]
    recursive = true
    action    = "triggered"
}`, dir))

	// Create the subdirectory after the watcher is running.
	subdir := filepath.Join(dir, "lateborn")
	require.NoError(t, os.Mkdir(subdir, 0o755))
	// Give handleFsEvent time to call watcher.Add on the new dir.
	time.Sleep(150 * time.Millisecond)

	baseline := trig.runCount.Load()
	require.NoError(t, os.WriteFile(filepath.Join(subdir, "file.txt"), []byte("hi"), 0o644))

	assert.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected create event from newly-watched subdirectory within 3s")
}

// TestFSEventGlobFilter verifies that non-matching files are silently ignored
// while matching files fire the action.
func TestFSEventGlobFilter(t *testing.T) {
	dir := t.TempDir()
	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path   = %q
    events = ["create"]
    filter = "*.txt"
    action = "triggered"
}`, dir))

	baseline := trig.runCount.Load()

	// Non-matching file — must NOT trigger.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skip.json"), []byte("{}"), 0o644))
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, baseline, trig.runCount.Load(), "json file should not have triggered")

	// Matching file — must trigger.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "match.txt"), []byte("txt"), 0o644))
	assert.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected create event for .txt file within 3s")

	// Exactly one dispatch total.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, baseline+1, trig.runCount.Load(), "expected exactly one dispatch (matching file only)")
}

// TestFSEventDebounce verifies that rapid writes to the same file collapse into
// a single dispatch after the quiet period expires.
func TestFSEventDebounce(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "debounce.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("v0"), 0o644))

	trig := startTrigger(t, fmt.Sprintf(`
trigger "file" "watch" {
    path     = %q
    events   = ["write"]
    debounce = "300ms"
    action   = "triggered"
}`, dir))

	baseline := trig.runCount.Load()

	// Five rapid writes spaced 30ms apart (well within the 300ms debounce window).
	for i := range 5 {
		require.NoError(t, os.WriteFile(testFile, []byte(fmt.Sprintf("v%d", i+1)), 0o644))
		time.Sleep(30 * time.Millisecond)
	}

	// Wait for the single debounced dispatch.
	assert.True(t, waitForRunCount(trig, baseline, 3*time.Second), "expected at least one dispatch after debounce")

	// Allow any spurious second dispatch to arrive before asserting count.
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, baseline+1, trig.runCount.Load(), "expected exactly one debounced dispatch")
}

// TestFSEventOnStartExistingCount verifies that PostStart dispatches one
// synthetic create event per pre-existing file.
func TestFSEventOnStartExistingCount(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644))
	}

	c, diags := withReadfiles(cfg.NewConfig().WithSources([]byte(fmt.Sprintf(`
trigger "file" "watch" {
    path              = %q
    on_start_existing = true
    action            = "triggered"
}`, dir))).WithLogger(testLogger(t))).Build()
	require.False(t, diags.HasErrors(), "config diagnostics: %v", diags)

	trig, err := GetFileTriggerFromCapsule(c.CtyTriggerMap["watch"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())
	t.Cleanup(func() { _ = trig.Stop() })

	// Wait for all three pre-existing files to dispatch.
	require.True(t, waitForRunCount(trig, 2, 3*time.Second), "expected 3 dispatches within 3s")
	// Brief pause to ensure no extra dispatches from real FS events.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int64(3), trig.runCount.Load(), "expected one dispatch per pre-existing file")
}
