package after

import (
	"context"
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

//go:embed testdata/trigger_after.vcl
var triggerAfterVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func TestTriggerAfter(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerAfterVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	require.Contains(t, c.CtyTriggerMap, "warmup")
	val := c.CtyTriggerMap["warmup"]
	assert.Equal(t, AfterCapsuleType, val.Type())

	// trigger.warmup is available in the eval context.
	triggerVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok)
	assert.Equal(t, AfterCapsuleType, triggerVar.GetAttr("warmup").Type())

	// Adds one Startable, one PostStartable, and one Stoppable.
	assert.Len(t, c.Startables, 1)
	assert.Len(t, c.PostStartables, 1)
	assert.Len(t, c.Stoppables, 1)

	// Delay is parsed at config time.
	trig, err := GetAfterTriggerFromCapsule(val)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, trig.delay)

	assert.Contains(t, c.TriggerDefRanges, "warmup")
}

func TestTriggerAfterDependencyId(t *testing.T) {
	h := cfg.NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"after", "warmup"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.warmup", id)
}

func TestTriggerAfterGetBeforeFire(t *testing.T) {
	trig := &AfterTrigger{}
	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "expected null before trigger fires")
}

func TestTriggerAfterGetAfterFire(t *testing.T) {
	trig := &AfterTrigger{}
	trig.mu.Lock()
	trig.result = cty.StringVal("warmed up")
	trig.mu.Unlock()

	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("warmed up")))
}

func TestTriggerAfterGetError(t *testing.T) {
	trig := &AfterTrigger{}
	trig.mu.Lock()
	trig.err = assert.AnError
	trig.mu.Unlock()

	_, err := trig.Get(context.Background(), nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestTriggerAfterStopBeforeFire(t *testing.T) {
	// Stop() before the delay elapses must return promptly without firing.
	c, diags := cfg.NewConfig().WithSources(triggerAfterVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetAfterTriggerFromCapsule(c.CtyTriggerMap["warmup"])
	require.NoError(t, err)

	require.NoError(t, trig.Start())
	require.NoError(t, trig.PostStart())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = trig.Stop()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked longer than expected")
	}

	// Action was skipped — result remains null.
	result, err := trig.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "action should not have fired after Stop()")
}
