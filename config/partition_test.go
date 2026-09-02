package config

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/tsarna/vinculum/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Each half of partitioning is inert without the half beneath it, so writing
// one alone is refused rather than quietly ignored — the schema's constraints
// describe this rule and Resolve is what applies it.
func TestPartitions_RequireWhatIsBeneathThem(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
	four := 4

	t.Run("partitions without queue_size", func(t *testing.T) {
		_, diags := SubscriberSource{
			Action:     parseExpr(t, `"x"`),
			Partitions: &four,
		}.Resolve(cfg, hcl.Range{}, "test", nil)
		require.True(t, diags.HasErrors())
		assert.Contains(t, diags.Error(), "partitions requires queue_size")
	})

	t.Run("partition_key without partitions", func(t *testing.T) {
		size := 10
		_, diags := SubscriberSource{
			Action:       parseExpr(t, `"x"`),
			QueueSize:    &size,
			PartitionKey: parseExpr(t, "ctx.topic"),
		}.Resolve(cfg, hcl.Range{}, "test", nil)
		require.True(t, diags.HasErrors())
		assert.Contains(t, diags.Error(), "partition_key requires partitions")
	})
}

// A partition count below one leaves no goroutine to process anything, which is
// never what was meant — so it is reported rather than rounded up.
func TestPartitions_MustBeAtLeastOne(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
	size, zero := 10, 0

	_, diags := SubscriberSource{
		Action:     parseExpr(t, `"x"`),
		QueueSize:  &size,
		Partitions: &zero,
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "partitions must be at least 1")
}

// Whether the payload is converted for the key expression is decided by what
// the expression asks for. This is the optimization the attribute's
// cost warning rests on: a key drawn from fields or the topic must not pay for
// converting a payload it never reads.
func TestPartitionKeyNeedsPayload(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want bool
		why  string
	}{
		{`"constant"`, false, "a constant reads nothing"},
		{`ctx.topic`, false, "the topic is already a string"},
		{`ctx.fields.device_id`, false, "fields are already strings"},
		{`"${ctx.fields.a}-${ctx.topic}"`, false, "several cheap reads are still cheap"},
		{`ctx.msg`, true, "the payload is read directly"},
		{`ctx.msg.device_id`, true, "the payload is read through"},
		{`upper(ctx.msg.id)`, true, "a call on the payload still reads it"},
		{`f(ctx)`, true, "the whole context could reach anything, including a functy body"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			assert.Equal(t, tc.want, partitionKeyNeedsPayload(parseExpr(t, tc.expr)), tc.why)
		})
	}
}

// A literal null asks for no ordering at all, which is a different request from
// an expression that happens to evaluate to null for one message. Only the
// first can be answered before any message exists.
func TestPartitionKeyIsUnordered(t *testing.T) {
	assert.True(t, partitionKeyIsUnordered(parseExpr(t, `null`)))
	assert.False(t, partitionKeyIsUnordered(parseExpr(t, `ctx.topic`)))
	assert.False(t, partitionKeyIsUnordered(parseExpr(t, `"key"`)))
}

// A key has to have a small, stable printed form, because it appears in logs
// and in a span attribute. A null is not a failure — it is a message with no
// key, and keyless messages share a partition so they stay ordered among
// themselves.
func TestPartitionKeyString(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})

	for _, tc := range []struct {
		expr string
		want string
	}{
		{`"device-7"`, "device-7"},
		{`42`, "42"},
		{`true`, "true"},
		{`null`, ""},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			val, diags := parseExpr(t, tc.expr).Value(cfg.evalCtx)
			require.False(t, diags.HasErrors())
			got, err := partitionKeyString(val)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("a list is refused rather than formatted", func(t *testing.T) {
		val, diags := parseExpr(t, `["a", "b"]`).Value(cfg.evalCtx)
		require.False(t, diags.HasErrors())
		_, err := partitionKeyString(val)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a string, number, or boolean")
	})
}

// A key expression that cannot be evaluated fails for every message, at message
// rate, on the goroutine the queue exists to keep moving. So the message still
// goes somewhere sensible — its topic's partition — and the failure is reported
// once rather than once per message.
func TestPartitionKeyFallsBackAndReportsOnce(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
	cfg.UserLogger = zap.New(core)

	// `nope` is not in scope, so evaluation fails for every message.
	keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, "nope.key"), "subscription/test")
	require.False(t, diags.HasErrors())
	require.NotNil(t, keyFn)

	for i := 0; i < 5; i++ {
		key := keyFn(bus.EventBusMessage{Ctx: context.Background(), Topic: "sensor/1"})
		assert.Equal(t, "sensor/1", key, "a message with no computable key orders by topic")
	}

	assert.Equal(t, 1, logs.FilterMessageSnippet("partition_key").Len(),
		"a failure at message rate must be reported once, not per message")
}

// The ordinary path: a key drawn from the message's fields, evaluated per
// message, with no payload conversion.
func TestPartitionKeyFromFields(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
	cfg.UserLogger = zap.NewNop()

	keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, "ctx.fields.device"), "test")
	require.False(t, diags.HasErrors())
	require.NotNil(t, keyFn)

	key := keyFn(bus.EventBusMessage{
		Ctx:    context.Background(),
		Topic:  "sensor/1",
		Fields: map[string]string{"device": "abc"},
	})
	assert.Equal(t, "abc", key)
}

// The keys people actually write — a field, or the topic — are answered from
// the message itself, with the name curried in at construction. Recognition has
// to be exact: an expression that merely mentions the field is a different
// expression and must take the general path.
func TestPartitionKeyRecognisesTheCommonShapes(t *testing.T) {
	t.Run("field", func(t *testing.T) {
		name, ok := partitionKeyField(parseExpr(t, "ctx.fields.device"))
		assert.True(t, ok)
		assert.Equal(t, "device", name)
	})

	t.Run("topic", func(t *testing.T) {
		assert.True(t, partitionKeyIsTopic(parseExpr(t, "ctx.topic")))
	})

	for _, expr := range []string{
		`lower(ctx.fields.device)`,
		`"${ctx.fields.device}"`,
		`ctx.fields.device.inner`,
		`ctx.msg.device`,
		`ctx.fields`,
		`"literal"`,
	} {
		t.Run("not a field: "+expr, func(t *testing.T) {
			_, ok := partitionKeyField(parseExpr(t, expr))
			assert.False(t, ok)
		})
	}

	for _, expr := range []string{`ctx.topic.inner`, `upper(ctx.topic)`, `ctx.fields.topic`} {
		t.Run("not the topic: "+expr, func(t *testing.T) {
			assert.False(t, partitionKeyIsTopic(parseExpr(t, expr)))
		})
	}
}

// The fast path and the general path must answer identically, including for the
// case they could most easily disagree on: a message that does not carry the
// field. `ctx.fields` is built from what is present, so reading an absent
// attribute is an evaluation failure — and how the key was spelled must not
// decide whether that is an error or an empty key.
func TestPartitionKeyFastPathAgreesWithGeneral(t *testing.T) {
	newKeyFn := func(t *testing.T, expr string) (subutils.PartitionKeyFunc, *observer.ObservedLogs) {
		core, logs := observer.New(zap.ErrorLevel)
		cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
		cfg.UserLogger = zap.New(core)

		keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, expr), "test")
		require.False(t, diags.HasErrors())
		require.NotNil(t, keyFn)
		return keyFn, logs
	}

	// The same field, spelled two ways: bare (fast), and wrapped in a template
	// that yields the same string (general).
	fast, fastLogs := newKeyFn(t, "ctx.fields.device")
	general, generalLogs := newKeyFn(t, `"${ctx.fields.device}"`)

	present := bus.EventBusMessage{
		Ctx:    context.Background(),
		Topic:  "sensor/1",
		Fields: map[string]string{"device": "abc"},
	}
	assert.Equal(t, "abc", fast(present))
	assert.Equal(t, "abc", general(present))

	missing := bus.EventBusMessage{Ctx: context.Background(), Topic: "sensor/1"}
	assert.Equal(t, "sensor/1", fast(missing), "a missing field falls back to the topic")
	assert.Equal(t, "sensor/1", general(missing), "and does so the same way on either path")
	assert.Equal(t, 1, fastLogs.FilterMessageSnippet("partition_key").Len())
	assert.Equal(t, 1, generalLogs.FilterMessageSnippet("partition_key").Len())

	// An empty field is a key, not a missing one, so it is not a failure.
	empty := bus.EventBusMessage{
		Ctx:    context.Background(),
		Topic:  "sensor/1",
		Fields: map[string]string{"device": ""},
	}
	assert.Equal(t, "", fast(empty))
	assert.Equal(t, "", general(empty))
	assert.Equal(t, 1, fastLogs.FilterMessageSnippet("partition_key").Len(),
		"an empty key is not a failure to report")
}

// The topic longhand answers from the message and never fails, so it needs no
// fallback at all.
func TestPartitionKeyTopicLonghand(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
	cfg.UserLogger = zap.NewNop()

	keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, "ctx.topic"), "test")
	require.False(t, diags.HasErrors())
	require.NotNil(t, keyFn)

	assert.Equal(t, "sensor/1", keyFn(bus.EventBusMessage{Ctx: context.Background(), Topic: "sensor/1"}))
}

// What the recognition is for. The fast path does no allocation per message;
// the general path builds a context object and a cty map to read a string back
// out of it.
func BenchmarkPartitionKey(b *testing.B) {
	build := func(expr string) subutils.PartitionKeyFunc {
		cfg := &Config{Logger: zap.NewNop(), UserLogger: zap.NewNop(), evalCtx: &hcl.EvalContext{}}
		parsed, diags := hclsyntax.ParseExpression([]byte(expr), "bench", hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			b.Fatal(diags)
		}
		keyFn, kDiags := buildPartitionKeyFunc(cfg, parsed, "bench")
		if kDiags.HasErrors() {
			b.Fatal(kDiags)
		}
		return keyFn
	}

	msg := bus.EventBusMessage{
		Ctx:    context.Background(),
		Topic:  "sensor/1",
		Fields: map[string]string{"device": "abc", "region": "eu", "seq": "12"},
	}

	for _, tc := range []struct {
		name string
		expr string
	}{
		{"field/fast", "ctx.fields.device"},
		{"field/general", `"${ctx.fields.device}"`},
		{"topic/fast", "ctx.topic"},
	} {
		keyFn := build(tc.expr)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = keyFn(msg)
			}
		})
	}
}

// The claim the cost documentation rests on, asserted rather than reasoned
// about: a key that does not mention the payload never converts one.
//
// The payload here *cannot* be converted — go2cty2go refuses a channel — so a
// key that computes anyway is proof that nothing tried. Both the fast path and
// the general path are checked, because they decide it differently: the first
// never builds a context at all, the second builds one without `msg` because
// the expression was seen not to ask for it.
func TestPartitionKeyDoesNotTouchAnUnreadPayload(t *testing.T) {
	// The expected key is asserted rather than "not the topic", because for
	// ctx.topic a fallback and a correct answer are the same string — there it
	// is the absence of a log line that says nothing went wrong.
	for _, tc := range []struct{ name, expr, want string }{
		{"fast path", "ctx.fields.device", "abc"},
		{"general path", `"${ctx.fields.device}-x"`, "abc-x"},
		{"topic", "ctx.topic", "sensor/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.ErrorLevel)
			cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
			cfg.UserLogger = zap.New(core)

			keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, tc.expr), "test")
			require.False(t, diags.HasErrors())
			require.NotNil(t, keyFn)

			key := keyFn(bus.EventBusMessage{
				Ctx:     context.Background(),
				Topic:   "sensor/1",
				Payload: make(chan int), // unconvertible: touching it would fail
				Fields:  map[string]string{"device": "abc"},
			})

			assert.Equal(t, tc.want, key)
			assert.Zero(t, logs.Len(), "converting a payload nothing reads")
		})
	}

	// The mirror, so the test above cannot pass by the conversion having been
	// removed altogether: a key that *does* read the payload fails on the same
	// message, falls back to the topic, and says so.
	core, logs := observer.New(zap.ErrorLevel)
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
	cfg.UserLogger = zap.New(core)

	keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, "ctx.msg.id"), "test")
	require.False(t, diags.HasErrors())

	key := keyFn(bus.EventBusMessage{
		Ctx:     context.Background(),
		Topic:   "sensor/1",
		Payload: make(chan int),
		Fields:  map[string]string{"device": "abc"},
	})
	assert.Equal(t, "sensor/1", key)
	assert.Equal(t, 1, logs.Len())
}

// A key reading the payload works, and is the case the documentation warns
// about rather than forbids.
func TestPartitionKeyFromPayload(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})
	cfg.UserLogger = zap.NewNop()

	keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, "ctx.msg.id"), "test")
	require.False(t, diags.HasErrors())
	require.NotNil(t, keyFn)

	key := keyFn(bus.EventBusMessage{
		Ctx:     context.Background(),
		Topic:   "orders",
		Payload: map[string]any{"id": "order-9"},
	})
	assert.Equal(t, "order-9", key)
}

// `partition_key = null` is not a key that always returns nothing: it is a
// request for no ordering, answered by a nil function so the caller selects
// round-robin instead.
func TestPartitionKeyNullSelectsRoundRobin(t *testing.T) {
	cfg := newSubscriberSourceTestConfig(t, &recorderSubscriber{})

	keyFn, diags := buildPartitionKeyFunc(cfg, parseExpr(t, "null"), "test")
	require.False(t, diags.HasErrors())
	assert.Nil(t, keyFn)
}

// Partitions are filled by hashing the key, which defaults to the topic — so a
// subscription naming fewer topics than partitions has asked for parallelism it
// cannot get. Where the topics are literal, that is knowable at load.
func TestPartitionTopicWarning(t *testing.T) {
	eight, one := 8, 1

	t.Run("fewer literal topics than partitions warns", func(t *testing.T) {
		diags := partitionTopicWarning(SubscriptionDefinition{
			Topics:     []string{"a", "b", "c"},
			Partitions: &eight,
		})
		require.Len(t, diags, 1)
		assert.Equal(t, hcl.DiagWarning, diags[0].Severity)
		assert.Contains(t, diags[0].Detail, "3 literal topics")
		assert.Contains(t, diags[0].Detail, "8 partitions")
	})

	t.Run("a wildcard makes the count unknowable", func(t *testing.T) {
		assert.Empty(t, partitionTopicWarning(SubscriptionDefinition{
			Topics:     []string{"sensor/#"},
			Partitions: &eight,
		}))
	})

	t.Run("a partition_key makes the topic irrelevant", func(t *testing.T) {
		assert.Empty(t, partitionTopicWarning(SubscriptionDefinition{
			Topics:       []string{"a"},
			Partitions:   &eight,
			PartitionKey: parseExpr(t, "ctx.fields.device"),
		}))
	})

	t.Run("enough topics is quiet", func(t *testing.T) {
		assert.Empty(t, partitionTopicWarning(SubscriptionDefinition{
			Topics:     []string{"a", "b", "c", "d", "e", "f", "g", "h"},
			Partitions: &eight,
		}))
	})

	t.Run("one partition is not partitioned", func(t *testing.T) {
		assert.Empty(t, partitionTopicWarning(SubscriptionDefinition{
			Topics:     []string{"a"},
			Partitions: &one,
		}))
	})
}

// Everything an action can reach that holds state, driven hard through eight
// partitions. Under -race this is what says the invariant partitioning relaxes
// — that a subscriber is called from one goroutine at a time — is one the work
// below it can do without.
//
// It is not asserting a count. Two partitions running
// `set(ctx, var.n, get(var.n) + 1)` genuinely do lose updates, which no lock
// prevents and the attribute's documentation says out loud; what must not
// happen is corruption or a panic.
func TestPartitions_StatefulActionsUnderRace(t *testing.T) {
	logger := zap.NewNop()

	config, diags := NewConfig().
		WithSources("testdata/partitioned.vcl").
		WithLogger(logger).
		Build()
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	main := config.Buses["main"]
	require.NotNil(t, main)

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// The device is a named wildcard in the subscription's topic, so
				// it reaches the key expression as ctx.fields.device.
				topic := fmt.Sprintf("work/device-%d", (worker+i)%6)
				_ = main.Publish(context.Background(), topic, map[string]any{"n": i})
			}
		}(worker)
	}
	wg.Wait()

	// Drain the queue the way shutdown does, so what follows is asserted against
	// work that has finished rather than work still in flight.
	for _, holder := range config.InFlight {
		if holder.Close != nil {
			require.NoError(t, holder.Close())
		}
	}

	seen, err := types.GetVariableFromCapsule(config.CtyVarMap["seen"])
	require.NoError(t, err)
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		val, err := seen.Get(context.Background(), nil)
		assert.NoError(c, err)
		count, _ := val.AsBigFloat().Int64()
		assert.Positive(c, count, "nothing was handled at all")
	}, 5*time.Second, 20*time.Millisecond)
}

// blockingRecorder holds every message until released, and records which keys
// were in flight together. It is how the claim gets asserted end to end: not
// that a partitioned subscriber works, but that it does two things at once.
type blockingRecorder struct {
	bus.BaseSubscriber
	gate chan struct{}

	mu       sync.Mutex
	order    []string
	live     int
	maxLive  int
	byDevice map[string][]int
}

func newBlockingRecorder() *blockingRecorder {
	return &blockingRecorder{gate: make(chan struct{}), byDevice: map[string][]int{}}
}

func (b *blockingRecorder) OnEvent(_ context.Context, _ string, message any, fields map[string]string) error {
	b.mu.Lock()
	b.live++
	if b.live > b.maxLive {
		b.maxLive = b.live
	}
	device := fields["device"]
	b.order = append(b.order, device)
	b.byDevice[device] = append(b.byDevice[device], message.(int))
	b.mu.Unlock()

	<-b.gate

	b.mu.Lock()
	b.live--
	b.mu.Unlock()

	return nil
}

// End to end through Resolve, from the attributes a configuration writes to
// messages actually being handled at once: four partitions keyed on a field,
// several devices, each device's messages in order and different devices'
// messages concurrent.
func TestPartitions_ResolveBuildsAPartitionedQueue(t *testing.T) {
	recorder := newBlockingRecorder()
	cfg := newSubscriberSourceTestConfig(t, recorder)
	cfg.UserLogger = zap.NewNop()

	size, partitions := 100, 4
	got, diags := SubscriberSource{
		Subscriber:   parseExpr(t, "sub"),
		QueueSize:    &size,
		Partitions:   &partitions,
		PartitionKey: parseExpr(t, "ctx.fields.device"),
	}.Resolve(cfg, hcl.Range{}, "test", nil)
	require.False(t, diags.HasErrors(), "diags: %v", diags)

	// The queue registered itself for shutdown, and Close is what drains it.
	require.Len(t, cfg.InFlight, 1)

	var sent atomic.Int64
	for i := 0; i < 40; i++ {
		require.NoError(t, got.OnEvent(context.Background(), "topic", i,
			map[string]string{"device": string(rune('a' + i%8))}))
		sent.Add(1)
	}

	// Every partition should have picked up a message and be waiting on the
	// gate, which is the parallelism the attributes asked for.
	assert.Eventually(t, func() bool {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		return recorder.maxLive == partitions
	}, 2*time.Second, 10*time.Millisecond,
		"messages were not processed concurrently")

	close(recorder.gate)
	require.NoError(t, cfg.InFlight[0].Close())

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	assert.Len(t, recorder.order, int(sent.Load()),
		"Close must drain every partition before returning")

	// And within a device, the order the messages were enqueued in.
	for device, seen := range recorder.byDevice {
		assert.IsIncreasing(t, seen, "device %q was handled out of order", device)
	}
}
