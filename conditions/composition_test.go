package conditions

import (
	"context"
	_ "embed"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	_ "github.com/tsarna/vinculum/triggers/watch"
	"github.com/zclconf/go-cty/cty"
)

//go:embed testdata/composition.vcl
var compositionVCL []byte

//go:embed testdata/circular.vcl
var circularVCL []byte

// TestComposition exercises the spec §Composition pipeline end-to-end:
//   - threshold high_temp derives a boolean from a numeric var
//   - trigger "watch" counts each rising edge of high_temp into a latched counter
//   - timer system_fault latches on either high_temp OR the counter's preset
func TestComposition(t *testing.T) {
	c := buildConfig(t, compositionVCL)
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	for _, s := range c.PostStartables {
		require.NoError(t, s.PostStart())
	}
	defer func() {
		for _, s := range c.Stoppables {
			_ = s.Stop()
		}
	}()

	high := c.CtyConditionMap["high_temp"].EncapsulatedValue().(*ThresholdCondition)
	events := c.CtyConditionMap["high_temp_events"].EncapsulatedValue().(*CounterCondition)
	fault := c.CtyConditionMap["system_fault"].EncapsulatedValue().(*TimerCondition)

	temp := c.CtyVarMap["temp"].EncapsulatedValue().(interface {
		Set(context.Context, []cty.Value) (cty.Value, error)
	})

	setTemp := func(v float64) {
		_, err := temp.Set(context.Background(), []cty.Value{cty.NumberFloatVal(v)})
		require.NoError(t, err)
	}

	// Cycle high/low three times: each rising edge fires the watch trigger,
	// which increments the latched counter. After three cycles the counter
	// reaches its preset and latches active.
	for i := 0; i < 3; i++ {
		setTemp(90)
		setTemp(60)
	}
	// Watch trigger fires its action on a background goroutine, so let it
	// catch up before checking accumulated state.
	require.Eventually(t, func() bool {
		cnt, _ := events.Count(context.Background())
		return cnt == 3
	}, time.Second, 5*time.Millisecond, "watch trigger should increment once per rising edge")

	assert.Equal(t, "active", stateMust(t, events), "counter latched at preset")

	// system_fault should be latched active because high_temp_events is true.
	require.Eventually(t, func() bool {
		s, _ := fault.State(context.Background())
		return s == "active"
	}, time.Second, 5*time.Millisecond)

	// Even with both upstream conditions falling away later, the latch holds.
	setTemp(50)
	assert.Equal(t, "inactive", stateMust(t, high))
	assert.Equal(t, "active", stateMust(t, events), "counter latched")
	assert.Equal(t, "active", stateMust(t, fault), "fault latched")
}

func TestCircularConditionRejected(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources(circularVCL).
		WithLogger(testLogger(t)).Build()
	require.True(t, diags.HasErrors(),
		"a → b → a cycle must be rejected at config load")
	assert.Contains(t, diags.Error(), "Circular dependency")
}
