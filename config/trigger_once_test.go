package config

import (
	_ "embed"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

//go:embed testdata/trigger_once.vcl
var triggerOnceVCL []byte

//go:embed testdata/trigger_once_cached.vcl
var triggerOnceCachedVCL []byte

func TestTriggerOnce(t *testing.T) {
	cfg, diags := NewConfig().WithSources(triggerOnceVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	// Once trigger stores an OnceCapsuleType value (not the raw computed value)
	require.Contains(t, cfg.CtyTriggerMap, "myinit")
	val := cfg.CtyTriggerMap["myinit"]
	assert.Equal(t, OnceCapsuleType, val.Type())

	// trigger.myinit is available in the eval context
	triggerVar, ok := cfg.evalCtx.Variables["trigger"]
	require.True(t, ok, "trigger variable should be set in evalCtx")
	assert.Equal(t, OnceCapsuleType, triggerVar.GetAttr("myinit").Type())

	// No Startables or Stoppables added
	assert.Empty(t, cfg.Startables)
	assert.Empty(t, cfg.Stoppables)

	// Name is tracked for uniqueness enforcement
	assert.Contains(t, cfg.TriggerDefRanges, "myinit")
}

func TestTriggerOnceGet(t *testing.T) {
	o := &OnceTrigger{}
	// Manually poke a pre-computed value in to test the Get() caching path
	// without needing a full config/HCL expression.
	o.once.Do(func() {
		o.value = cty.StringVal("cached")
	})

	result, err := o.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("cached")))

	// Second call still returns cached value
	result, err = o.Get(nil)
	require.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("cached")))
}

func TestTriggerOnceCaching(t *testing.T) {
	// This test uses VCL assert blocks to verify:
	// 1. get(trigger.myinit) returns the action result
	// 2. Calling get() again returns the same cached value
	// 3. The action expression is only evaluated once (counter == 1, not 2)
	_, diags := NewConfig().WithSources(triggerOnceCachedVCL).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)
}

func TestTriggerOnceDependencyId(t *testing.T) {
	h := NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"once", "myinit"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.myinit", id)
}
