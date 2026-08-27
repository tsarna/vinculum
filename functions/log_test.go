package functions

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
)

// logOne calls log::warn with one argument and returns the JSON the log line
// actually produced.
//
// Against zap's real JSON encoder rather than an observer: what is being tested
// is how a value reaches an operator's log, and zapcore.MapObjectEncoder
// renders several types (times among them) differently from the encoder a
// running process uses. Asserting on the observer would have passed while the
// real output was wrong, and vice versa.
func logOne(t *testing.T, arg cty.Value) map[string]any {
	t.Helper()

	var buf zaptest.Buffer
	logger := zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(&buf),
		zapcore.DebugLevel,
	))

	fns := GetLogFunctions(logger)
	_, err := fns["log::warn"].Call([]cty.Value{cty.StringVal("m"), arg})
	require.NoError(t, err)

	lines := buf.Lines()
	require.Len(t, lines, 1)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &out), "log line: %s", lines[0])
	delete(out, "level")
	delete(out, "ts")
	delete(out, "msg")
	return out
}

func TestLogRendersAListOfObjectsAsStructuredData(t *testing.T) {
	// doc/health.md's recommended line:
	//     log::warn("not ready", {problems = health::failing(ctx)})
	// This used to emit cty's Go source syntax —
	// `[cty.ObjectVal(map[string]cty.Value{"component":cty.StringVal(...`
	// which is unreadable and not JSON.
	got := logOne(t, cty.ObjectVal(map[string]cty.Value{
		"problems": cty.ListVal([]cty.Value{
			cty.ObjectVal(map[string]cty.Value{
				"component": cty.StringVal("check.upstream"),
				"ready":     cty.False,
				"reason":    cty.StringVal("no route to host"),
			}),
		}),
	}))

	problems, ok := got["problems"].([]any)
	require.True(t, ok, "problems should be an array, got %T: %v", got["problems"], got["problems"])
	require.Len(t, problems, 1)

	entry, ok := problems[0].(map[string]any)
	require.True(t, ok, "entry should be an object, got %T", problems[0])
	assert.Equal(t, "check.upstream", entry["component"])
	assert.Equal(t, false, entry["ready"])
	assert.Equal(t, "no route to host", entry["reason"])
}

func TestLogRendersACapsuleAsItsUnderlyingValue(t *testing.T) {
	// A health entry's `since` is a time capsule. GoString would have logged an
	// opaque handle; the value behind it is a timestamp.
	when := time.Date(2026, 3, 4, 8, 51, 22, 0, time.UTC)
	got := logOne(t, cty.ObjectVal(map[string]cty.Value{
		"since": timecty.NewTimeCapsule(when),
	}))

	assert.Equal(t, "2026-03-04T08:51:22Z", got["since"])
}

func TestLogRendersATimeTheSameAtEveryDepth(t *testing.T) {
	// zap.Any would not: its fmt.Stringer case claims a top-level time and
	// renders Go's "2026-03-04 08:51:22 +0000 UTC", while the same value inside
	// a list goes through Reflect and comes out RFC 3339. One value, two
	// renderings, decided by nesting depth.
	when := time.Date(2026, 3, 4, 8, 51, 22, 0, time.UTC)
	got := logOne(t, cty.ObjectVal(map[string]cty.Value{
		"top":    timecty.NewTimeCapsule(when),
		"nested": cty.ListVal([]cty.Value{timecty.NewTimeCapsule(when)}),
	}))

	assert.Equal(t, "2026-03-04T08:51:22Z", got["top"])
	assert.Equal(t, []any{"2026-03-04T08:51:22Z"}, got["nested"])
}

func TestLogRendersANestedObjectAsAnObject(t *testing.T) {
	// Not a list, so this took the other old branch — a bare GoString() — and
	// was just as unreadable.
	got := logOne(t, cty.ObjectVal(map[string]cty.Value{
		"outer": cty.ObjectVal(map[string]cty.Value{
			"inner": cty.StringVal("v"),
			"n":     cty.NumberIntVal(3),
		}),
	}))

	outer, ok := got["outer"].(map[string]any)
	require.True(t, ok, "outer should be an object, got %T: %v", got["outer"], got["outer"])
	assert.Equal(t, "v", outer["inner"])
	assert.Equal(t, float64(3), outer["n"])
}

func TestLogRendersAListOfScalarsAsAnArray(t *testing.T) {
	// This case did produce readable output before — as the *string*
	// `["a", "b"]`. A real array is what a structured sink can filter on.
	got := logOne(t, cty.ObjectVal(map[string]cty.Value{
		"topics": cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
	}))

	assert.Equal(t, []any{"a", "b"}, got["topics"])
}

func TestLogKeepsScalarsTyped(t *testing.T) {
	// The scalar paths are untouched, and must stay that way: a number logged
	// as a JSON string rather than a number would break every dashboard built
	// on it.
	got := logOne(t, cty.ObjectVal(map[string]cty.Value{
		"s": cty.StringVal("text"),
		"n": cty.NumberIntVal(42),
		"f": cty.NumberFloatVal(1.5),
		"b": cty.True,
	}))

	assert.Equal(t, "text", got["s"])
	assert.Equal(t, float64(42), got["n"])
	assert.Equal(t, 1.5, got["f"])
	assert.Equal(t, true, got["b"])
}

func TestLogRendersNullAndEmptyCollections(t *testing.T) {
	got := logOne(t, cty.ObjectVal(map[string]cty.Value{
		"nothing": cty.NullVal(cty.String),
		"none":    cty.ListValEmpty(cty.String),
	}))

	assert.Equal(t, "<null>", got["nothing"])

	// An empty result is health's *good* case — `health::failing` returning
	// nothing — so it must log as `[]`, not `null`. A consumer doing
	// `.problems | length` breaks on the latter.
	//
	// This needed go2cty2go v0.3.1: it returned a nil slice for an empty
	// collection, which Go marshals as null. Fixed there rather than here, so
	// send::go was corrected by the same change.
	assert.Equal(t, []any{}, got["none"])

	// Null and empty stay distinct — that is the point.
	assert.NotEqual(t, got["nothing"], got["none"])
}
