package config

import (
	"context"
	"testing"

	_ "embed"

	fsm "github.com/tsarna/vinculum-fsm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/fsm_basic.vcl
var fsmBasicTest []byte

func TestFsm_BasicConfig(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().WithSources(fsmBasicTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	// FSM should be registered in the eval context.
	assert.Contains(t, config.Constants, "fsm")
	assert.Contains(t, config.CtyFsmMap, "door")

	// Extract the instance.
	capsule := config.CtyFsmMap["door"]
	inst, err := fsm.GetInstanceFromCapsule(capsule)
	require.NoError(t, err)

	assert.Equal(t, "door", inst.Name())

	// Start the FSM.
	for _, s := range config.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for i := len(config.Stoppables) - 1; i >= 0; i-- {
			config.Stoppables[i].Stop()
		}
	}()

	// Verify initial state.
	state, err := inst.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "closed", state)

	// Verify initial storage.
	val, err := inst.Get(context.Background(), []cty.Value{cty.StringVal("open_count")})
	require.NoError(t, err)
	assert.Equal(t, "0", val.AsBigFloat().String())

	val, err = inst.Get(context.Background(), []cty.Value{cty.StringVal("last_user")})
	require.NoError(t, err)
	assert.Equal(t, "unknown", val.AsString())
}

func TestFsm_SendEvent(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().WithSources(fsmBasicTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	capsule := config.CtyFsmMap["door"]
	inst, err := fsm.GetInstanceFromCapsule(capsule)
	require.NoError(t, err)

	for _, s := range config.Startables {
		require.NoError(t, s.Start())
	}
	defer func() {
		for i := len(config.Stoppables) - 1; i >= 0; i-- {
			config.Stoppables[i].Stop()
		}
	}()

	// Send events via OnEvent (simulating send() or subscription).
	err = inst.OnEvent(context.Background(), "open", nil, nil)
	require.NoError(t, err)

	// Stop to drain queue.
	for i := len(config.Stoppables) - 1; i >= 0; i-- {
		config.Stoppables[i].Stop()
	}

	state, err := inst.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "open", state)
}

func TestFsm_WildcardTransition(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().WithSources(fsmBasicTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	capsule := config.CtyFsmMap["door"]
	inst, err := fsm.GetInstanceFromCapsule(capsule)
	require.NoError(t, err)

	for _, s := range config.Startables {
		require.NoError(t, s.Start())
	}

	// Lock the door first.
	inst.OnEvent(context.Background(), "lock", nil, nil)
	// Emergency from locked -> closed (wildcard).
	inst.OnEvent(context.Background(), "emergency", nil, nil)

	for i := len(config.Stoppables) - 1; i >= 0; i-- {
		config.Stoppables[i].Stop()
	}

	state, err := inst.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "closed", state)
}

func TestFsm_GetSubscriberFromCapsule(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().WithSources(fsmBasicTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	capsule := config.CtyFsmMap["door"]
	sub, err := GetSubscriberFromCapsule(capsule)
	require.NoError(t, err)
	assert.NotNil(t, sub)
}

//go:embed testdata/fsm_disabled.vcl
var fsmDisabledTest []byte

func TestFsm_Disabled(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := NewConfig().WithSources(fsmDisabledTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	assert.NotContains(t, config.CtyFsmMap, "door")
}

//go:embed testdata/fsm_reactive.vcl
var fsmReactiveTest []byte

func TestFsm_ReactiveWhen(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(fsmReactiveTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	fsmCapsule := cfg.CtyFsmMap["hvac"]
	inst, err := fsm.GetInstanceFromCapsule(fsmCapsule)
	require.NoError(t, err)

	tempCapsule := cfg.CtyVarMap["temperature"]

	// Start everything.
	for _, s := range cfg.Startables {
		require.NoError(t, s.Start())
	}

	// Initially temperature=50, so "overheat" (>100) is false, state stays "idle".
	state, _ := inst.State(context.Background())
	assert.Equal(t, "idle", state)

	// Set temperature to 110 -- triggers overheat (false→true edge).
	setTemp := func(val int) {
		v := tempCapsule.EncapsulatedValue()
		s, ok := v.(interface {
			Set(context.Context, []cty.Value) (cty.Value, error)
		})
		require.True(t, ok, "temperature variable must be Settable")
		_, err := s.Set(context.Background(), []cty.Value{cty.NumberIntVal(int64(val))})
		require.NoError(t, err)
	}

	setTemp(110)

	// Stop to drain event queue and ensure transition completes.
	for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
		cfg.Stoppables[i].Stop()
	}

	state, _ = inst.State(context.Background())
	assert.Equal(t, "cooling", state)
}

func TestFsm_ReactiveEdgeTrigger(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(fsmReactiveTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	fsmCapsule := cfg.CtyFsmMap["hvac"]
	inst, err := fsm.GetInstanceFromCapsule(fsmCapsule)
	require.NoError(t, err)

	tempCapsule := cfg.CtyVarMap["temperature"]
	setTemp := func(val int) {
		v := tempCapsule.EncapsulatedValue()
		s := v.(interface {
			Set(context.Context, []cty.Value) (cty.Value, error)
		})
		s.Set(context.Background(), []cty.Value{cty.NumberIntVal(int64(val))})
	}

	for _, s := range cfg.Startables {
		require.NoError(t, s.Start())
	}

	// Set temperature above 100 -- triggers overheat.
	setTemp(110)

	// Set temperature to 120 -- still above 100, should NOT re-fire (edge-triggered).
	setTemp(120)

	// The FSM should have transitioned only once (idle→cooling).
	// If it fired again, the self-transition would still leave it in cooling,
	// but the transition count would be 2. Let's check count.
	for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
		cfg.Stoppables[i].Stop()
	}

	state, _ := inst.State(context.Background())
	assert.Equal(t, "cooling", state)

	count, _ := inst.Count(context.Background())
	assert.Equal(t, int64(1), count, "edge-triggered: should fire only once")
}

//go:embed testdata/fsm_hooks.vcl
var fsmHooksTest []byte

func TestFsm_Hooks(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(fsmHooksTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	fsmCapsule := cfg.CtyFsmMap["door"]
	inst, err := fsm.GetInstanceFromCapsule(fsmCapsule)
	require.NoError(t, err)

	logCapsule := cfg.CtyVarMap["log"]
	getLog := func() string {
		g := logCapsule.EncapsulatedValue().(interface {
			Get(context.Context, []cty.Value) (cty.Value, error)
		})
		v, _ := g.Get(context.Background(), nil)
		if v.IsNull() {
			return ""
		}
		return v.AsString()
	}

	// Start.
	for _, s := range cfg.Startables {
		require.NoError(t, s.Start())
	}

	// on_init should have fired.
	assert.Equal(t, "init", getLog())

	// Send "open" event: exit:closed -> action:open -> on_entry(increment) -> on_change
	inst.OnEvent(context.Background(), "open", nil, nil)

	// Send an unknown event while in "open" state to trigger on_event.
	inst.OnEvent(context.Background(), "unknown", nil, nil)

	// Stop to drain.
	for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
		cfg.Stoppables[i].Stop()
	}

	// After the sequence, var.log should be "on_event:open" (the last set).
	assert.Equal(t, "on_event:open", getLog())

	// open_count should be 1.
	val, err := inst.Get(context.Background(), []cty.Value{cty.StringVal("open_count")})
	require.NoError(t, err)
	assert.Equal(t, "1", val.AsBigFloat().String())

	state, _ := inst.State(context.Background())
	assert.Equal(t, "open", state)
}

func TestFsm_GuardExpression(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(fsmHooksTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	fsmCapsule := cfg.CtyFsmMap["door"]
	inst, err := fsm.GetInstanceFromCapsule(fsmCapsule)
	require.NoError(t, err)

	for _, s := range cfg.Startables {
		require.NoError(t, s.Start())
	}

	// Guard on "lock" checks state(fsm.door) == "closed", which is true initially.
	inst.OnEvent(context.Background(), "lock", nil, nil)

	for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
		cfg.Stoppables[i].Stop()
	}

	state, _ := inst.State(context.Background())
	assert.Equal(t, "locked", state)
}

func TestFsm_SnapshotRestore(t *testing.T) {
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	cfg, diags := NewConfig().WithSources(fsmBasicTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	fsmCapsule := cfg.CtyFsmMap["door"]
	inst, err := fsm.GetInstanceFromCapsule(fsmCapsule)
	require.NoError(t, err)

	for _, s := range cfg.Startables {
		require.NoError(t, s.Start())
	}

	// Transition to "open" and set some storage.
	inst.OnEvent(context.Background(), "open", nil, nil)

	// Take a snapshot (via Get with no args).
	// Stop first to ensure the transition is processed.
	for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
		cfg.Stoppables[i].Stop()
	}

	snap, err := inst.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "fsm", snap.GetAttr("_type").AsString())
	assert.Equal(t, "open", snap.GetAttr("state").AsString())

	// Create a fresh config and restore the snapshot.
	cfg2, diags := NewConfig().WithSources(fsmBasicTest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	inst2, err := fsm.GetInstanceFromCapsule(cfg2.CtyFsmMap["door"])
	require.NoError(t, err)

	for _, s := range cfg2.Startables {
		require.NoError(t, s.Start())
	}

	// Restore the snapshot.
	_, err = inst2.Set(context.Background(), []cty.Value{snap})
	require.NoError(t, err)

	// Stop to drain queue (restore is async).
	for i := len(cfg2.Stoppables) - 1; i >= 0; i-- {
		cfg2.Stoppables[i].Stop()
	}

	state, _ := inst2.State(context.Background())
	assert.Equal(t, "open", state)
}
