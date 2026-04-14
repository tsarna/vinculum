package start

import (
	_ "embed"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/trigger_start.vcl
var triggerStartVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func TestTriggerStart(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerStartVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// trigger "start" is a PostStartable only — no Startable or Stoppable.
	assert.Len(t, c.Startables, 0)
	assert.Len(t, c.Stoppables, 0)
	assert.Len(t, c.PostStartables, 1)

	// Before PostStart(), the trigger value is null.
	require.Contains(t, c.CtyTriggerMap, "init")
	assert.True(t, c.CtyTriggerMap["init"].IsNull(), "expected null before PostStart()")

	triggerVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok)
	assert.True(t, triggerVar.GetAttr("init").IsNull(), "expected null in eval context before PostStart()")

	assert.Contains(t, c.TriggerDefRanges, "init")
}

func TestTriggerStartDependencyId(t *testing.T) {
	h := cfg.NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"start", "init"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.init", id)
}

func TestTriggerStartPostStart(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerStartVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	require.Len(t, c.PostStartables, 1)
	require.NoError(t, c.PostStartables[0].PostStart())

	// After PostStart(), the trigger value is the action result.
	val := c.CtyTriggerMap["init"]
	assert.Equal(t, cty.StringVal("started"), val)

	// The eval context is also updated.
	triggerVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok)
	assert.Equal(t, cty.StringVal("started"), triggerVar.GetAttr("init"))
}
