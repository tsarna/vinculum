package once

import (
	"context"
	_ "embed"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/trigger_once.vcl
var triggerOnceVCL []byte

//go:embed testdata/trigger_once_cached.vcl
var triggerOnceCachedVCL []byte

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

func TestTriggerOnce(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(triggerOnceVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Once trigger stores an OnceCapsuleType value (not the raw computed value)
	require.Contains(t, c.CtyTriggerMap, "myinit")
	val := c.CtyTriggerMap["myinit"]
	assert.Equal(t, OnceCapsuleType, val.Type())

	// trigger.myinit is available in the eval context
	triggerVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok, "trigger variable should be set in evalCtx")
	assert.Equal(t, OnceCapsuleType, triggerVar.GetAttr("myinit").Type())

	// No Startables or Stoppables added
	assert.Empty(t, c.Startables)
	assert.Empty(t, c.Stoppables)

	// Name is tracked for uniqueness enforcement
	assert.Contains(t, c.TriggerDefRanges, "myinit")
}

func TestTriggerOnceGet(t *testing.T) {
	o := &OnceTrigger{}
	// Manually poke a pre-computed value in to test the Get() caching path
	// without needing a full config/HCL expression.
	o.once.Do(func() {
		o.value = cty.StringVal("cached")
	})

	result, err := o.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("cached")))

	// Second call still returns cached value
	result, err = o.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("cached")))
}

func TestTriggerOnceCaching(t *testing.T) {
	// This test uses VCL assert blocks to verify:
	// 1. get(trigger.myinit) returns the action result
	// 2. Calling get() again returns the same cached value
	// 3. The action expression is only evaluated once (counter == 1, not 2)
	_, diags := cfg.NewConfig().WithSources(triggerOnceCachedVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
}

func TestTriggerOnceDependencyId(t *testing.T) {
	h := cfg.NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"once", "myinit"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.myinit", id)
}
