package schemadoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// callCheck calls man::check and returns its object's attributes.
func callCheck(t *testing.T, source string) map[string]cty.Value {
	t.Helper()
	got, err := manCheckFunc().Call([]cty.Value{cty.StringVal(source)})
	require.NoError(t, err, "man::check reports problems in its result, never as an error")
	return got.AsValueMap()
}

func TestManCheckAcceptsAValidConfig(t *testing.T) {
	r := callCheck(t, `
bus "main" {}

subscription "s" {
  target = bus.main
  topics = ["a/#"]
  action = ctx.topic
}
`)
	assert.True(t, r["valid"].True())
	assert.Equal(t, "The configuration is valid.\n", r["text"].AsString())
	assert.Equal(t, 0, r["diagnostics"].LengthInt())
}

func TestManCheckReportsAnErrorAgainstItsLine(t *testing.T) {
	r := callCheck(t, `bus "main" {}

subscription "s" {
  target = bus.mian
  topics = ["a"]
  action = 1
}
`)
	require.False(t, r["valid"].True())
	assert.Equal(t, "1", r["errors"].AsBigFloat().String())

	text := r["text"].AsString()
	t.Log("\n" + text)
	assert.True(t, strings.HasPrefix(text, "The configuration is not valid: 1 error.\n\n"), text)
	assert.Contains(t, text, "on config.vcl line 4", "the source is named for the reader")
	assert.Contains(t, text, "target = bus.mian", "and its line is quoted")
	assert.NotContains(t, text, "<bytes@")

	d := r["diagnostics"].AsValueSlice()[0].AsValueMap()
	assert.Equal(t, "error", d["severity"].AsString())
	assert.Equal(t, "4", d["line"].AsBigFloat().String())
	assert.False(t, d["summary"].IsNull())
}

func TestManCheckRefusesALargeSource(t *testing.T) {
	old := checkMaxSourceBytes
	checkMaxSourceBytes = 100
	t.Cleanup(func() { checkMaxSourceBytes = old })

	r := callCheck(t, "# "+strings.Repeat("x", 200))
	assert.False(t, r["valid"].True())
	assert.Contains(t, r["text"].AsString(), "Configuration too large")

	d := r["diagnostics"].AsValueSlice()[0].AsValueMap()
	assert.True(t, d["line"].IsNull(), "a refusal has no place in the source")
}

// This package links no function plugins, so what needs them — try(), the file
// functions — is checked in cmd's TestManCheckWithEveryBuiltin.

func TestManCheckHidesTheEnvironment(t *testing.T) {
	t.Setenv("VINCULUM_CHECK_SECRET", "hunter2")

	r := callCheck(t, `const { x = env.VINCULUM_CHECK_SECRET }`)
	assert.False(t, r["valid"].True(), "the variable is not there to read")
	assert.NotContains(t, r["text"].AsString(), "hunter2")
}

func TestManCheckSurvivesUnboundedRecursion(t *testing.T) {
	r := callCheck(t, `
function "f" {
  params = [x]
  result = f(x)
}
const { y = f(1) }
`)
	assert.False(t, r["valid"].True())
	assert.Contains(t, r["text"].AsString(), `function "f" was called more than 500 deep`)
}

// A check that cannot finish in time answers, and holds its slot until it does
// finish, so a second check is turned away rather than started beside it.
func TestManCheckTimesOutAndHoldsItsSlot(t *testing.T) {
	oldTimeout, oldBuild := checkTimeout, checkBuild
	checkTimeout = 50 * time.Millisecond
	release := make(chan struct{})
	checkBuild = func(source []byte) checked {
		<-release
		return build(source)
	}
	t.Cleanup(func() { checkTimeout, checkBuild = oldTimeout, oldBuild })

	start := time.Now()
	r := callCheck(t, `bus "main" {}`)
	assert.Less(t, time.Since(start), 5*time.Second, "the caller is not held for the build")
	assert.False(t, r["valid"].True())
	assert.Contains(t, r["text"].AsString(), "Check did not finish")

	r = callCheck(t, `bus "main" {}`)
	assert.Contains(t, r["text"].AsString(), "Check not run", "the slot is still held by the build that timed out")

	// Once the stuck build returns, its slot is free again.
	close(release)
	require.Eventually(t, func() bool {
		return callCheck(t, `bus "main" {}`)["valid"].True()
	}, 5*time.Second, 20*time.Millisecond)
}

func TestManCheckRemovesItsScratchDirectory(t *testing.T) {
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "vinculum-check-*"))
	callCheck(t, `bus "main" {}`)
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "vinculum-check-*"))
	assert.ElementsMatch(t, before, after)
}

// A build that panics is on a goroutine of its own, where a panic would end
// the process serving the check.
func TestManCheckRecoversFromAPanickingBuild(t *testing.T) {
	old := checkBuild
	checkBuild = func([]byte) checked { panic("boom") }
	t.Cleanup(func() { checkBuild = old })

	r := callCheck(t, `bus "main" {}`)
	assert.False(t, r["valid"].True())
	assert.Contains(t, r["text"].AsString(), "Check failed")
	assert.Contains(t, r["text"].AsString(), "boom")
}
