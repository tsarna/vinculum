package watchdog

import (
	_ "embed"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/trigger_watchdog.vcl
var triggerWatchdogVCL []byte

//go:embed testdata/trigger_watchdog_full.vcl
var triggerWatchdogFullVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func TestTriggerWatchdog(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerWatchdogVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	require.Contains(t, c.CtyTriggerMap, "heartbeat")
	val := c.CtyTriggerMap["heartbeat"]
	assert.Equal(t, WatchdogCapsuleType, val.Type())

	// trigger.heartbeat available in eval context.
	triggerVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok)
	assert.Equal(t, WatchdogCapsuleType, triggerVar.GetAttr("heartbeat").Type())

	// Adds one Startable and one Stoppable.
	assert.Len(t, c.Startables, 1)
	assert.Len(t, c.Stoppables, 1)

	wdog, err := GetWatchdogTriggerFromCapsule(val)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, wdog.window)
	assert.Equal(t, time.Duration(0), wdog.initialGrace) // default: use window
	assert.False(t, wdog.repeat)

	assert.Contains(t, c.TriggerDefRanges, "heartbeat")
}

func TestTriggerWatchdogFull(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerWatchdogFullVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	wdog, err := GetWatchdogTriggerFromCapsule(c.CtyTriggerMap["full"])
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, wdog.window)
	assert.Equal(t, 2*time.Minute, wdog.initialGrace)
	assert.True(t, wdog.repeat)
}

func TestTriggerWatchdogDependencyId(t *testing.T) {
	h := cfg.NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"watchdog", "heartbeat"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.heartbeat", id)
}

func TestTriggerWatchdogGetBeforeSet(t *testing.T) {
	wdog := &WatchdogTrigger{}
	result, err := wdog.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "expected null before first set()")
}

func TestTriggerWatchdogSetAndGet(t *testing.T) {
	wdog := &WatchdogTrigger{}
	result, err := wdog.Set([]cty.Value{cty.StringVal("alive")})
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("alive")))

	got, err := wdog.Get(nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.StringVal("alive")))
}

func TestTriggerWatchdogSetNoValue(t *testing.T) {
	wdog := &WatchdogTrigger{}
	result, err := wdog.Set(nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull())

	got, err := wdog.Get(nil)
	require.NoError(t, err)
	assert.True(t, got.IsNull())
}

func TestTriggerWatchdogSetResetsMissCount(t *testing.T) {
	wdog := &WatchdogTrigger{}
	wdog.mu.Lock()
	wdog.missCount = 5
	wdog.mu.Unlock()

	_, err := wdog.Set([]cty.Value{cty.True})
	require.NoError(t, err)

	wdog.mu.RLock()
	count := wdog.missCount
	wdog.mu.RUnlock()
	assert.Equal(t, int64(0), count)
}

func TestTriggerWatchdogStopBeforeFire(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerWatchdogVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	wdog, err := GetWatchdogTriggerFromCapsule(c.CtyTriggerMap["heartbeat"])
	require.NoError(t, err)

	require.NoError(t, wdog.Start())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = wdog.Stop()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked longer than expected")
	}

	// Action never fired — miss count stays 0.
	wdog.mu.RLock()
	count := wdog.missCount
	wdog.mu.RUnlock()
	assert.Equal(t, int64(0), count)
}

func TestTriggerWatchdogFiresAfterWindow(t *testing.T) {
	src := []byte(`
trigger "watchdog" "fast" {
    window = "50ms"
    action = "fired"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	wdog, err := GetWatchdogTriggerFromCapsule(c.CtyTriggerMap["fast"])
	require.NoError(t, err)

	require.NoError(t, wdog.Start())

	// Wait long enough for at least one fire (window = 50ms, grace = 50ms).
	time.Sleep(300 * time.Millisecond)

	wdog.mu.RLock()
	count := wdog.missCount
	wdog.mu.RUnlock()
	assert.GreaterOrEqual(t, count, int64(1), "expected watchdog to have fired at least once")

	require.NoError(t, wdog.Stop())
}
