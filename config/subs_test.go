package config

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
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

// ---------------------------------------------------------------------------
// subscription.<name>
// ---------------------------------------------------------------------------

// subscriptionHandleFor builds a config from src and returns the handle behind
// `subscription.<name>`, reached the way an expression reaches it.
func subscriptionHandleFor(t *testing.T, src string, name string) *SubscriptionHandle {
	t.Helper()

	config, diags := NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	val, ok := config.CtySubscriptionMap[name]
	require.True(t, ok, "no subscription.%s", name)

	handle, err := GetSubscriptionFromCapsule(val)
	require.NoError(t, err)
	return handle
}

// getMember reads one member the way `get(subscription.x, "…")` does.
func getMember(t *testing.T, h *SubscriptionHandle, member string) cty.Value {
	t.Helper()
	var gettable richcty.Gettable = h
	val, err := gettable.Get(context.Background(), []cty.Value{cty.StringVal(member)})
	require.NoError(t, err)
	return val
}

func floatMember(t *testing.T, h *SubscriptionHandle, member string) float64 {
	t.Helper()
	f, _ := getMember(t, h, member).AsBigFloat().Float64()
	return f
}

const partitionedSubscription = `
bus "main" {}

subscription "to_broker" {
    target        = bus.main
    topics        = ["out/#"]
    action        = true
    queue_size    = 200
    partitions    = 8
    partition_key = ctx.fields.device
}
`

func TestSubscriptionIsGettable(t *testing.T) {
	handle := subscriptionHandleFor(t, partitionedSubscription, "to_broker")

	assert.True(t, getMember(t, handle, "queue_depth").RawEquals(cty.NumberIntVal(0)))
	assert.True(t, getMember(t, handle, "dropped").RawEquals(cty.NumberIntVal(0)))
	assert.True(t, getMember(t, handle, "partitions").RawEquals(cty.NumberIntVal(8)))

	// queue_size is per partition, so the capacity a configuration can read is
	// the product — the number `queue_size` alone would have it guess wrong.
	assert.True(t, getMember(t, handle, "queue_capacity").RawEquals(cty.NumberIntVal(1600)))

	assert.Equal(t, 0.0, floatMember(t, handle, "queue_ratio"))

	// An empty queue is trivially even, so skew is 1 rather than 0/0.
	assert.Equal(t, 1.0, floatMember(t, handle, "skew"))
}

// A one-partition subscription reads the same vocabulary, and reads it the same
// way: queue_ratio is a maximum at every partition count, and where there is one
// partition the maximum and the aggregate coincide.
func TestSubscriptionMembersAtOnePartition(t *testing.T) {
	handle := subscriptionHandleFor(t, `
bus "main" {}

subscription "plain" {
    target     = bus.main
    topics     = ["out/#"]
    action     = true
    queue_size = 50
}
`, "plain")

	assert.True(t, getMember(t, handle, "queue_capacity").RawEquals(cty.NumberIntVal(50)))
	assert.True(t, getMember(t, handle, "partitions").RawEquals(cty.NumberIntVal(1)))
	assert.Equal(t, 1.0, floatMember(t, handle, "skew"))
}

// blockingSubscriber holds its worker goroutine until release is closed, so the
// queue in front of it can be filled deterministically.
type blockingSubscriber struct {
	bus.BaseSubscriber
	release chan struct{}
}

func (b *blockingSubscriber) OnEvent(_ context.Context, _ string, _ any, _ map[string]string) error {
	<-b.release
	return nil
}

// The numbers have to move, or they are decoration. A queue one deep behind an
// action that blocks fills, and what arrives after that is dropped and counted.
func TestSubscriptionQueueCountsWhatItRefuses(t *testing.T) {
	release := make(chan struct{})
	cfg := newSubscriberSourceTestConfig(t, &blockingSubscriber{release: release})

	queueSize := 1
	_, queue, diags := SubscriberSource{
		Subscriber: parseExpr(t, "sub"),
		QueueSize:  &queueSize,
	}.ResolveQueue(cfg, hcl.Range{}, "subscription/slow", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)
	require.NotNil(t, queue)
	defer func() {
		close(release)
		queue.Close()
	}()

	handle := &SubscriptionHandle{name: "slow", queue: queue}

	// One message reaches the blocked worker, one fills the single slot, and
	// everything after that is refused.
	for i := 0; i < 12; i++ {
		_ = queue.OnEvent(context.Background(), "t", i, nil)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dropped, _ := getMember(t, handle, "dropped").AsBigFloat().Int64(); dropped > 0 {
			assert.Equal(t, 1.0, floatMember(t, handle, "queue_ratio"),
				"a queue refusing messages is full, and the ratio must say so")
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("a full queue's drops were never visible")
}

// skew's two ends, on a queue that is actually lopsided. A constant
// partition_key hashes every message to one partition, which is the
// partitioning trap the member exists to expose: four partitions, one of them
// holding everything, reads 4.0.
func TestSubscriptionSkewReportsALopsidedQueue(t *testing.T) {
	release := make(chan struct{})
	cfg := newSubscriberSourceTestConfig(t, &blockingSubscriber{release: release})

	queueSize, partitions := 50, 4
	_, queue, diags := SubscriberSource{
		Subscriber:   parseExpr(t, "sub"),
		QueueSize:    &queueSize,
		Partitions:   &partitions,
		PartitionKey: parseExpr(t, `"always-the-same"`),
	}.ResolveQueue(cfg, hcl.Range{}, "subscription/lopsided", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)
	require.NotNil(t, queue)
	defer func() {
		close(release)
		queue.Close()
	}()

	handle := &SubscriptionHandle{name: "lopsided", queue: queue}

	// Nothing queued yet: trivially even.
	require.Equal(t, 1.0, floatMember(t, handle, "skew"))

	for i := 0; i < 9; i++ {
		require.NoError(t, queue.OnEvent(context.Background(), "t", i, nil))
	}

	// One message is taken by the blocked worker; the other eight pile into
	// that same partition's queue while the other three stay empty.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if depth, _ := getMember(t, handle, "queue_depth").AsBigFloat().Int64(); depth == 8 {
			assert.Equal(t, 4.0, floatMember(t, handle, "skew"),
				"everything on one of four partitions is a skew of 4")

			// The other two partition-aware members, on the same lopsided
			// queue: the ratio is that one partition's, not the average, so it
			// reads 8/50 rather than 8/200.
			assert.InDelta(t, 8.0/50.0, floatMember(t, handle, "queue_ratio"), 1e-9)
			assert.True(t, getMember(t, handle, "queue_capacity").RawEquals(cty.NumberIntVal(200)))
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the queue never reached the expected depth; got %s",
		getMember(t, handle, "queue_depth").GoString())
}

// The worked rows of doc/config.md's skew table, checked against the arithmetic
// that actually ships. Every row holds the same 200 messages, which is the
// property the docs claim and the reason skew is a ratio: it moves with the
// distribution and not with the amount.
func TestSkewIsARatioNotACount(t *testing.T) {
	for _, tc := range []struct {
		depths []int
		want   float64
	}{
		{[]int{50, 50, 50, 50}, 1.0},
		{[]int{100, 50, 50, 0}, 2.0},
		{[]int{200, 0, 0, 0}, 4.0},
		// A tenth of the messages, distributed identically: same answer.
		{[]int{10, 5, 5, 0}, 2.0},
		{[]int{0, 0, 0, 0}, 1.0},
	} {
		total, max := 0, 0
		for _, d := range tc.depths {
			total += d
			if d > max {
				max = d
			}
		}

		assert.InDelta(t, tc.want, queueSkew(max, total, len(tc.depths)), 1e-9,
			"depths %v", tc.depths)
	}
}

// A subscription with no queue_size has no queue. Answering zeros would be
// indistinguishable from a healthy empty one, and would let a threshold
// silently watch nothing — so it reports what is missing, by name.
func TestSubscriptionWithoutQueueSaysWhy(t *testing.T) {
	handle := subscriptionHandleFor(t, `
bus "main" {}

subscription "direct" {
    target = bus.main
    topics = ["out/#"]
    action = true
}
`, "direct")

	var gettable richcty.Gettable = handle
	_, err := gettable.Get(context.Background(), []cty.Value{cty.StringVal("queue_depth")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue_size")
	assert.Contains(t, err.Error(), "direct")

	// The other mistake is still the other mistake: a name that is not a member
	// is reported as one, not as a missing queue.
	_, err = gettable.Get(context.Background(), []cty.Value{cty.StringVal("queue_dept")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no member")
	assert.NotContains(t, err.Error(), "queue_size")
}

func TestSubscriptionGetRejectsWhatItCannotAnswer(t *testing.T) {
	handle := subscriptionHandleFor(t, partitionedSubscription, "to_broker")
	var gettable richcty.Gettable = handle

	_, err := gettable.Get(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue_depth", "the diagnostic should list the members")

	_, err = gettable.Get(context.Background(), []cty.Value{cty.StringVal("undelivered")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no member",
		"a subscriber's queue has one consumer, so nothing can go unmatched in it")

	_, err = gettable.Get(context.Background(), []cty.Value{cty.NumberIntVal(3)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a string")
}

// The root exists whether or not anything fills it, so a reference to a
// subscription that is not declared is reported as that rather than as a name
// the language does not have.
func TestSubscriptionRootExistsWithNoSubscriptions(t *testing.T) {
	config, diags := NewConfig().
		WithSources([]byte("bus \"main\" {}\n")).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	root, ok := config.Constants["subscription"]
	require.True(t, ok, "the subscription root must exist with no subscription blocks")
	require.True(t, root.Type().IsObjectType())
	assert.Empty(t, root.Type().AttributeTypes())
}

// A disabled block creates nothing, so it publishes no name — the rule every
// other block follows, and the one the deferred-reference checker assumes.
func TestDisabledSubscriptionPublishesNoName(t *testing.T) {
	config, diags := NewConfig().
		WithSources([]byte(`
bus "main" {}

subscription "off" {
    target   = bus.main
    topics   = ["out/#"]
    action   = true
    disabled = true
}
`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	assert.NotContains(t, config.CtySubscriptionMap, "off")
}

// The label used to be decorative. It addresses the queue now, so two blocks
// sharing one is a reference with two answers.
func TestDuplicateSubscriptionIsReported(t *testing.T) {
	_, diags := NewConfig().
		WithSources([]byte(`
bus "main" {}

subscription "dupe" {
    target = bus.main
    topics = ["a/#"]
    action = true
}

subscription "dupe" {
    target = bus.main
    topics = ["b/#"]
    action = true
}
`)).
		WithLogger(zap.NewNop()).
		Build()

	require.True(t, diags.HasErrors(), "a duplicate subscription label must be reported")
	assert.Contains(t, diags.Error(), "Duplicate subscription")
}

// The namespace entry is what `vinculum check` reads, so a typo below the root
// is caught at load rather than at the first event.
func TestSubscriptionTypoIsCaught(t *testing.T) {
	_, diags := NewConfig().
		WithSources([]byte(`
bus "main" {}

subscription "watcher" {
    target     = bus.main
    topics     = ["out/#"]
    action     = true
    queue_size = 10
}

subscription "reporter" {
    target = bus.main
    topics = ["report/#"]
    action = log::info("depth", { d = get(subscription.watchre, "queue_depth") })
}
`)).
		WithLogger(zap.NewNop()).
		Build()

	require.True(t, diags.HasErrors(), "a subscription typo must be reported")
	assert.Contains(t, diags.Error(), "watchre")
}
