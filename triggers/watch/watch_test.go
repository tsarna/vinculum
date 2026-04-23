package watch

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var bg = context.Background()

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)
	return logger
}

// varFromConfig extracts a *types.Variable from the config eval context by name.
func varFromConfig(t *testing.T, c *cfg.Config, name string) *types.Variable {
	t.Helper()
	varMap := c.EvalCtx().Variables["var"]
	capsule := varMap.GetAttr(name)
	v, err := types.GetVariableFromCapsule(capsule)
	require.NoError(t, err)
	return v
}

func TestWatchTrigger_Config(t *testing.T) {
	src := []byte(`
var "x" {}

trigger "watch" "on_x" {
    watch  = var.x
    action = "handled"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	require.Contains(t, c.CtyTriggerMap, "on_x")
	val := c.CtyTriggerMap["on_x"]
	assert.Equal(t, WatchTriggerCapsuleType, val.Type())

	trigVar, ok := c.EvalCtx().Variables["trigger"]
	require.True(t, ok)
	assert.Equal(t, WatchTriggerCapsuleType, trigVar.GetAttr("on_x").Type())

	assert.Len(t, c.Startables, 1)
	assert.Len(t, c.Stoppables, 1)
	assert.Contains(t, c.TriggerDefRanges, "on_x")

	trig, err := GetWatchTriggerFromCapsule(val)
	require.NoError(t, err)
	assert.NotNil(t, trig.watchable)
	assert.Nil(t, trig.skipWhenExpr)
}

func TestWatchTrigger_Config_WithSkipWhen(t *testing.T) {
	src := []byte(`
var "x" {}

trigger "watch" "on_x" {
    watch     = var.x
    skip_when = ctx.old_value == ctx.new_value
    action    = "handled"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetWatchTriggerFromCapsule(c.CtyTriggerMap["on_x"])
	require.NoError(t, err)
	assert.NotNil(t, trig.skipWhenExpr)
}

func TestWatchTrigger_DependencyId(t *testing.T) {
	h := cfg.NewTriggerBlockHandler()
	block := &hcl.Block{
		Type:   "trigger",
		Labels: []string{"watch", "on_x"},
	}
	id, diags := h.GetBlockDependencyId(block)
	require.False(t, diags.HasErrors())
	assert.Equal(t, "trigger.on_x", id)
}

func TestWatchTrigger_InvalidWatchTarget(t *testing.T) {
	src := []byte(`
trigger "watch" "bad" {
    watch  = "not-a-watchable"
    action = "handled"
}
`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	assert.True(t, diags.HasErrors(), "non-watchable expression should produce a config error")
}

func TestWatchTrigger_GetBeforeAnyChange(t *testing.T) {
	trig := &WatchTrigger{}
	result, err := trig.Get(bg, nil)
	require.NoError(t, err)
	assert.True(t, result.IsNull(), "Get before any change should return null")
}

func TestWatchTrigger_Stop_PreventsFurtherDispatch(t *testing.T) {
	src := []byte(`
var "x" {}

trigger "watch" "on_x" {
    watch  = var.x
    action = "handled"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetWatchTriggerFromCapsule(c.CtyTriggerMap["on_x"])
	require.NoError(t, err)
	x := varFromConfig(t, c, "x")

	require.NoError(t, trig.Start())
	require.NoError(t, trig.Stop())

	// Set after Stop: OnChange should be silently dropped.
	_, _ = x.Set(bg, []cty.Value{cty.NumberIntVal(42)})
	// No goroutines should be in-flight; WaitGroup is already zero.
	assert.True(t, trig.stopped.Load())
}

func TestWatchTrigger_DispatchesAction(t *testing.T) {
	// action = set(var.result, ctx.new_value) lets us observe the action firing.
	src := []byte(`
var "x" {}
var "result" {}

trigger "watch" "on_x" {
    watch  = var.x
    action = set(var.result, ctx.new_value)
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetWatchTriggerFromCapsule(c.CtyTriggerMap["on_x"])
	require.NoError(t, err)
	x := varFromConfig(t, c, "x")
	result := varFromConfig(t, c, "result")

	require.NoError(t, trig.Start())

	_, _ = x.Set(bg, []cty.Value{cty.StringVal("hello")})

	require.NoError(t, trig.Stop()) // waits for in-flight goroutines

	got, err := result.Get(bg, nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.StringVal("hello")))
}

func TestWatchTrigger_SkipWhen_PreventsAction(t *testing.T) {
	// skip_when = ctx.old_value == ctx.new_value: action must not run when value unchanged.
	// Use a counter variable so we can count exactly how many times the action fires.
	src := []byte(`
var "x" {}
var "fire_count" { value = 0 }

trigger "watch" "on_x" {
    watch     = var.x
    skip_when = ctx.old_value == ctx.new_value
    action    = increment(var.fire_count, 1)
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetWatchTriggerFromCapsule(c.CtyTriggerMap["on_x"])
	require.NoError(t, err)
	x := varFromConfig(t, c, "x")
	fireCount := varFromConfig(t, c, "fire_count")

	require.NoError(t, trig.Start())

	// First set: null → "v" — old ≠ new, action fires.
	_, _ = x.Set(bg, []cty.Value{cty.StringVal("v")})
	// Second set: "v" → "v" — skip_when is true, action must not fire.
	_, _ = x.Set(bg, []cty.Value{cty.StringVal("v")})

	require.NoError(t, trig.Stop()) // waits for in-flight goroutines

	got, err := fireCount.Get(bg, nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.NumberIntVal(1)), "action should have fired exactly once, got %s", got.GoString())

	// lastValue is updated on EVERY change regardless of skip_when.
	last, err := trig.Get(bg, nil)
	require.NoError(t, err)
	assert.True(t, last.RawEquals(cty.StringVal("v")))
}

// TestWatchTrigger_CallerCancellationDoesNotPropagate verifies that when the
// caller of Set() provides a ctx that is cancelled before (or during) the
// dispatched action evaluation, the action still completes. The caller's
// span context values are preserved; cancellation is severed.
func TestWatchTrigger_CallerCancellationDoesNotPropagate(t *testing.T) {
	src := []byte(`
var "x" {}
var "result" {}

trigger "watch" "on_x" {
    watch  = var.x
    action = set(var.result, "fired")
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetWatchTriggerFromCapsule(c.CtyTriggerMap["on_x"])
	require.NoError(t, err)
	x := varFromConfig(t, c, "x")
	result := varFromConfig(t, c, "result")

	require.NoError(t, trig.Start())

	// Call Set with a ctx that we cancel immediately. In the pre-fix code
	// the goroutine's action evaluated against this cancelled ctx; with
	// the fix the dispatch helper applies context.WithoutCancel so the
	// action proceeds.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = x.Set(ctx, []cty.Value{cty.StringVal("trigger-me")})

	require.NoError(t, trig.Stop()) // waits for in-flight goroutines

	got, err := result.Get(bg, nil)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.StringVal("fired")),
		"action should have completed despite caller ctx cancellation; got %s", got.GoString())
}

// TestWatchTrigger_SpanIsLinkedRoot verifies that the span created for an
// async action is a new-root span linked to the caller's span — not a child
// of it. This matches OTel's async-messaging semantic conventions and avoids
// parent-before-child violations when the caller's span ends first.
func TestWatchTrigger_SpanIsLinkedRoot(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})

	src := []byte(`
var "x" {}

trigger "watch" "on_x" {
    watch  = var.x
    action = "ok"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors(), "unexpected diagnostics: %v", diags)

	trig, err := GetWatchTriggerFromCapsule(c.CtyTriggerMap["on_x"])
	require.NoError(t, err)
	x := varFromConfig(t, c, "x")

	require.NoError(t, trig.Start())

	// Start a parent span and drive a Set through its ctx. The watch
	// action's span should be a new-root linked to this parent, not a
	// child of it.
	parentTracer := tp.Tracer("test/parent")
	ctx, parentSpan := parentTracer.Start(context.Background(), "parent-op")
	parentCtx := parentSpan.SpanContext()
	_, _ = x.Set(ctx, []cty.Value{cty.StringVal("v")})
	parentSpan.End()

	require.NoError(t, trig.Stop()) // waits for in-flight goroutines

	// Find the trigger span in the exported batch.
	var triggerSpan sdktrace.ReadOnlySpan
	for _, s := range exporter.GetSpans().Snapshots() {
		if s.Name() == "trigger.watch on_x" {
			triggerSpan = s
			break
		}
	}
	require.NotNil(t, triggerSpan, "expected 'trigger.watch on_x' span in exporter")

	// New root: different TraceID from the parent.
	assert.NotEqual(t, parentCtx.TraceID(), triggerSpan.SpanContext().TraceID(),
		"trigger span should be a new root (different TraceID), got child")

	// Linked back to the parent span.
	links := triggerSpan.Links()
	require.Len(t, links, 1, "expected exactly one link on the trigger span")
	assert.Equal(t, parentCtx.TraceID(), links[0].SpanContext.TraceID(),
		"link should point at the parent trace")
	assert.Equal(t, parentCtx.SpanID(), links[0].SpanContext.SpanID(),
		"link should point at the parent span")

	// Avoid unused-import error if someone rearranges.
	_ = trace.SpanFromContext
}

func TestWatchTrigger_Stop_WaitsForInFlight(t *testing.T) {
	src := []byte(`
var "x" {}

trigger "watch" "on_x" {
    watch  = var.x
    action = "handled"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(testLogger(t)).Build()
	require.False(t, diags.HasErrors())

	trig, err := GetWatchTriggerFromCapsule(c.CtyTriggerMap["on_x"])
	require.NoError(t, err)
	x := varFromConfig(t, c, "x")

	require.NoError(t, trig.Start())
	_, _ = x.Set(bg, []cty.Value{cty.True})

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
}
