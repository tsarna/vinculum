package config

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/zclconf/go-cty/cty"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// recorderSubscriber captures OnEvent calls so we can assert delivery across
// wrappers (transforms, async queue).
type recorderSubscriber struct {
	bus.BaseSubscriber
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	topic   string
	message any
	fields  map[string]string
}

func (r *recorderSubscriber) OnEvent(_ context.Context, topic string, message any, fields map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{topic: topic, message: message, fields: fields})
	return nil
}

func (r *recorderSubscriber) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *recorderSubscriber) first() recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[0]
}

// newSubscriberSourceTestConfig returns a minimal Config that has a single
// subscriber capsule variable named `sub` in scope plus a logger.
func newSubscriberSourceTestConfig(t *testing.T, sub bus.Subscriber) *Config {
	t.Helper()
	cfg := &Config{
		Logger: zap.NewNop(),
		evalCtx: &hcl.EvalContext{
			Variables: map[string]cty.Value{
				"sub": NewSubscriberCapsule(sub),
			},
		},
	}
	return cfg
}

func TestSubscriberSource_ExactlyOneRequired(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})

	t.Run("neither", func(t *testing.T) {
		_, diags := SubscriberSource{}.Resolve(cfg, hcl.Range{}, "test", nil)
		require.True(t, diags.HasErrors())
		assert.Contains(t, diags.Error(), "Exactly one of subscriber or action")
	})

	t.Run("both", func(t *testing.T) {
		_, diags := SubscriberSource{
			Subscriber: parseExpr(t, "sub"),
			Action:     parseExpr(t, `"anything"`),
		}.Resolve(cfg, hcl.Range{}, "test", nil)
		require.True(t, diags.HasErrors())
		assert.Contains(t, diags.Error(), "Exactly one of subscriber or action")
	})
}

func TestSubscriberSource_SubscriberPath(t *testing.T) {
	underlying := &recorderSubscriber{}
	cfg := newSubscriberSourceTestConfig(t, underlying)

	got, diags := SubscriberSource{
		Subscriber: parseExpr(t, "sub"),
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	// Exact same subscriber — no wrappers applied.
	assert.Same(t, underlying, got)
}

func TestSubscriberSource_ActionPath(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})

	got, diags := SubscriberSource{
		Action: parseExpr(t, `"ignored result"`),
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	_, ok := got.(*ActionSubscriber)
	assert.True(t, ok, "expected *ActionSubscriber, got %T", got)
}

func TestSubscriberSource_TransformsWrap(t *testing.T) {
	underlying := &recorderSubscriber{}
	cfg := newSubscriberSourceTestConfig(t, underlying)

	got, diags := SubscriberSource{
		Subscriber: parseExpr(t, "sub"),
		Transforms: parseExpr(t, `[add_topic_prefix("out/")]`),
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	// The returned subscriber is the transforming wrapper, not the underlying.
	assert.NotSame(t, underlying, got)

	// Deliver an event; the prefix transform should rewrite the topic.
	require.NoError(t, got.OnEvent(context.Background(), "in", "hello", nil))
	require.Equal(t, 1, underlying.len())
	assert.Equal(t, "out/in", underlying.first().topic)
}

func TestSubscriberSource_QueueSizeWrapAndStarted(t *testing.T) {
	underlying := &recorderSubscriber{}
	cfg := newSubscriberSourceTestConfig(t, underlying)

	queueSize := 4
	got, diags := SubscriberSource{
		Subscriber: parseExpr(t, "sub"),
		QueueSize:  &queueSize,
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	async, ok := got.(*subutils.AsyncQueueingSubscriber)
	require.True(t, ok, "expected *AsyncQueueingSubscriber, got %T", got)
	defer async.Close()

	// If Start() were missing, this OnEvent call would queue but never drain.
	require.NoError(t, async.OnEvent(context.Background(), "t", "payload", nil))

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if underlying.len() > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.Equal(t, 1, underlying.len(), "async goroutine never delivered the event — .Start() not called?")
	assert.Equal(t, "t", underlying.first().topic)
	assert.Equal(t, "payload", underlying.first().message)
}

// waitForDelivery blocks until the async goroutine has delivered at least one
// event, or fails the test.
func waitForDelivery(t *testing.T, r *recorderSubscriber) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r.len() > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("async goroutine never delivered the event")
}

// The point of passing a TracerProvider through Resolve: the queue hands work
// to a background goroutine, and without a provider the trace simply stops at
// the enqueue. Asserted on the span the dispatch emits rather than on the
// plumbing that carries the provider, since the span is the thing anyone
// actually wanted.
func TestSubscriberSource_QueueTracesTheAsyncHop(t *testing.T) {
	underlying := &recorderSubscriber{}
	cfg := newSubscriberSourceTestConfig(t, underlying)

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	queueSize := 4
	got, diags := SubscriberSource{
		Subscriber: parseExpr(t, "sub"),
		QueueSize:  &queueSize,
	}.Resolve(cfg, hcl.Range{}, "test", tp)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	async, ok := got.(*subutils.AsyncQueueingSubscriber)
	require.True(t, ok, "expected *AsyncQueueingSubscriber, got %T", got)
	defer async.Close()

	require.NoError(t, async.OnEvent(context.Background(), "t", "payload", nil))
	waitForDelivery(t, underlying)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := recorder.Ended()
	require.Len(t, spans, 1, "the async dispatch should have emitted exactly one span")
	assert.Equal(t, trace.SpanKindConsumer, spans[0].SpanKind(),
		"the work happens on the far side of a queue, so it is a consumer")
}

// The counterpart, and what makes the assertion above mean something: with no
// provider the dispatch is untraced. This is what a `subscription` block did
// until `tracing` existed, whatever else the configuration had set up.
func TestSubscriberSource_QueueWithoutProviderEmitsNoSpans(t *testing.T) {
	underlying := &recorderSubscriber{}
	cfg := newSubscriberSourceTestConfig(t, underlying)

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	queueSize := 4
	got, diags := SubscriberSource{
		Subscriber: parseExpr(t, "sub"),
		QueueSize:  &queueSize,
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	async := got.(*subutils.AsyncQueueingSubscriber)
	defer async.Close()

	require.NoError(t, async.OnEvent(context.Background(), "t", "payload", nil))
	waitForDelivery(t, underlying)
	require.NoError(t, tp.ForceFlush(context.Background()))

	assert.Empty(t, recorder.Ended())
}

func TestSubscriberSource_TransformsThenQueue(t *testing.T) {
	underlying := &recorderSubscriber{}
	cfg := newSubscriberSourceTestConfig(t, underlying)

	queueSize := 4
	got, diags := SubscriberSource{
		Subscriber: parseExpr(t, "sub"),
		Transforms: parseExpr(t, `[add_topic_prefix("out/")]`),
		QueueSize:  &queueSize,
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	async, ok := got.(*subutils.AsyncQueueingSubscriber)
	require.True(t, ok, "outermost wrapper should be the async queue, got %T", got)
	defer async.Close()

	require.NoError(t, async.OnEvent(context.Background(), "in", "payload", nil))

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if underlying.len() > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.Equal(t, 1, underlying.len())
	assert.Equal(t, "out/in", underlying.first().topic, "transform should run before async delivery")
}
