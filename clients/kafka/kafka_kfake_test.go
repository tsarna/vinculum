package kafka_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	_ "github.com/tsarna/vinculum/clients/otlp" // register client "otlp" for the test config
	cfg "github.com/tsarna/vinculum/config"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
)

// startConfig builds and starts a config, returning it alongside a function
// that stops it. Stopping is registered as cleanup too, so a test only calls
// the returned function when it needs the shutdown to happen at a particular
// moment — as the commit-mode tests below do, where the commit a consumer
// makes on its way out of the group is the thing under test.
func startConfig(t *testing.T, src string) (*cfg.Config, func()) {
	t.Helper()

	config, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	for _, s := range config.Startables {
		require.NoError(t, s.Start())
	}

	var once sync.Once
	stop := func() {
		once.Do(func() {
			for i := len(config.Stoppables) - 1; i >= 0; i-- {
				_ = config.Stoppables[i].Stop()
			}
		})
	}
	t.Cleanup(stop)

	return config, stop
}

// captureSubscriber records the baggage on the context of the first event it
// receives and signals via a channel.
type captureSubscriber struct {
	bus.BaseSubscriber
	once sync.Once
	got  chan map[string]string
}

func (c *captureSubscriber) OnEvent(ctx context.Context, _ string, _ any, _ map[string]string) error {
	m := map[string]string{}
	for _, mem := range baggage.FromContext(ctx).Members() {
		m[mem.Key()] = mem.Value()
	}
	c.once.Do(func() { c.got <- m })
	return nil
}

// runKfakeBaggageRoundTrip produces a Kafka record carrying a baggage header,
// consumes it through a real vinculum kafka receiver (with the given baggage{}
// block), and returns the baggage the delivered action context actually sees.
// This exercises the whole inbound path: record header → kotel extraction →
// the receiver's baggage filter → subscriber.
func runKfakeBaggageRoundTrip(t *testing.T, baggageBlock string) map[string]string {
	t.Helper()

	// kotel extracts via the global propagator; install the composite that
	// includes Baggage, and restore it afterward.
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	// In-process fake Kafka broker with the source topic pre-created.
	cluster, err := kfake.NewCluster(kfake.SeedTopics(1, "intopic"))
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	addr := cluster.ListenAddrs()[0]

	// Quietly absorb OTLP exports so the client has a real TracerProvider
	// (required for the kotel extraction hook to be installed) without noise.
	otlpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(otlpSrv.Close)

	src := fmt.Sprintf(`
bus "main" {}

client "otlp" "t" {
  endpoint     = %q
  service_name = "kfake-test"
}

client "kafka" "k" {
  brokers = [%q]
  tracing = client.t

  receiver "r" {
    group_id     = "g"
    start_offset = "earliest"
    subscriber   = bus.main
%s
    subscription "intopic" {
      vinculum_topic = "in"
    }
  }
}
`, otlpSrv.URL, addr, baggageBlock)

	config, _ := startConfig(t, src)

	cap := &captureSubscriber{got: make(chan map[string]string, 1)}
	require.NoError(t, config.Buses["main"].Subscribe(context.Background(), "#", cap))

	// Produce one record with a W3C baggage header.
	prod, err := kgo.NewClient(kgo.SeedBrokers(addr))
	require.NoError(t, err)
	t.Cleanup(prod.Close)
	rec := &kgo.Record{
		Topic: "intopic",
		Value: []byte(`"hello"`),
		Headers: []kgo.RecordHeader{
			{Key: "baggage", Value: []byte("tenant_id=acme,secret=x")},
		},
	}
	require.NoError(t, prod.ProduceSync(context.Background(), rec).FirstErr())

	select {
	case got := <-cap.got:
		return got
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for the consumed message")
		return nil
	}
}

func TestKfakeConsumeStripsBaggageByDefault(t *testing.T) {
	// No baggage block → secure default → all inbound baggage stripped.
	got := runKfakeBaggageRoundTrip(t, "")
	assert.Empty(t, got)
}

func TestKfakeConsumeAllowsListedBaggage(t *testing.T) {
	// allow = ["tenant_id"] → only that key survives; "secret" is dropped.
	got := runKfakeBaggageRoundTrip(t, `    baggage { allow = ["tenant_id"] }`)
	assert.Equal(t, map[string]string{"tenant_id": "acme"}, got)
}

func TestKfakeConsumePassthroughTrustsAll(t *testing.T) {
	got := runKfakeBaggageRoundTrip(t, `    baggage { passthrough = true }`)
	assert.Equal(t, map[string]string{"tenant_id": "acme", "secret": "x"}, got)
}

// ─── commit_mode ─────────────────────────────────────────────────────────────

// firstMessageSubscriber forwards the first message it receives to a channel,
// as a string. The two consumers below deliver different Go types for the same
// payload — one arrives via send(), which publishes a cty.Value — so both are
// normalized here.
type firstMessageSubscriber struct {
	bus.BaseSubscriber
	once sync.Once
	got  chan string
}

func (s *firstMessageSubscriber) OnEvent(_ context.Context, _ string, msg any, _ map[string]string) error {
	str := fmt.Sprint(msg)
	if v, ok := msg.(cty.Value); ok && v.Type() == cty.String {
		str = v.AsString()
	}
	s.once.Do(func() { s.got <- str })
	return nil
}

// failingAction publishes what it saw and then fails, by adding a number to a
// string. The publish is what gives the test a positive signal that the record
// really was processed — a settle policy's whole job is to react to what
// happened next.
const failingAction = `
    action = [
      send(ctx, bus.main, "seen", ctx.msg),
      ctx.msg + 1,
    ]`

// firstRecordAfterFailure consumes one record under the given receiver
// settings, with an action that fails.
func firstRecordAfterFailure(t *testing.T, receiverAttrs string) string {
	t.Helper()
	return firstRecordAfterFailureWithAction(t, receiverAttrs, failingAction)
}

// firstRecordAfterFailureWithAction consumes one record under the given
// receiver settings and action, then rejoins the same consumer group with a
// subscriber that succeeds and asks what it is handed first.
//
// A marker record produced to the second consumer is what makes the answer a
// positive assertion rather than a wait: getting the original record back means
// its offset was never committed, and getting only the marker means it was.
func firstRecordAfterFailureWithAction(t *testing.T, receiverAttrs, action string) string {
	t.Helper()

	cluster, err := kfake.NewCluster(kfake.SeedTopics(1, "intopic"))
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	addr := cluster.ListenAddrs()[0]

	prod, err := kgo.NewClient(kgo.SeedBrokers(addr))
	require.NoError(t, err)
	t.Cleanup(prod.Close)

	produce := func(value string) {
		t.Helper()
		require.NoError(t, prod.ProduceSync(context.Background(), &kgo.Record{
			Topic: "intopic", Value: []byte(`"` + value + `"`),
		}).FirstErr())
	}

	// `start_offset = "earliest"` on both runs is load-bearing. "stored" resets
	// to the *end* of the partition when the group has no committed offset, so
	// the second consumer would skip an uncommitted record and report the same
	// answer as a committed one.
	failing := fmt.Sprintf(`
bus "main" {}

client "kafka" "k" {
  brokers = [%q]

  receiver "r" {
    group_id     = "g"
    start_offset = "earliest"
%s
%s

    subscription "intopic" {
      vinculum_topic = "in"
    }
  }
}
`, addr, receiverAttrs, action)

	first, stopFirst := startConfig(t, failing)
	seen := &firstMessageSubscriber{got: make(chan string, 1)}
	require.NoError(t, first.Buses["main"].Subscribe(context.Background(), "seen", seen))

	produce("A")

	select {
	case msg := <-seen.got:
		require.Equal(t, "A", msg, "the first consumer should have processed the record")
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for the first consumer to process the record")
	}

	// Closing the client leaves the group, and a group left with autocommit on
	// commits everything polled on its way out — so this is where a periodic
	// commit lands, with no timer to wait for.
	stopFirst()

	replay := fmt.Sprintf(`
bus "main" {}

client "kafka" "k" {
  brokers = [%q]

  receiver "r" {
    group_id     = "g"
    start_offset = "earliest"
    subscriber   = bus.main

    subscription "intopic" {
      vinculum_topic = "in"
    }
  }
}
`, addr)

	second, _ := startConfig(t, replay)
	got := &firstMessageSubscriber{got: make(chan string, 1)}
	require.NoError(t, second.Buses["main"].Subscribe(context.Background(), "in", got))

	produce("B")

	select {
	case msg := <-got.got:
		return msg
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for the second consumer to receive a record")
		return ""
	}
}

func TestKfakeAutoRedeliversAFailedRecord(t *testing.T) {
	// At-least-once: the offset advances only once the message was handled, so
	// a record whose action failed is still there for the next consumer. This
	// is what `commit_mode = "after_process"` did, under the name every
	// receiver now shares, and the assertion is unchanged — which is the point
	// of the rename.
	assert.Equal(t, "A", firstRecordAfterFailure(t, `    ack = "auto"`))
}

func TestKfakePeriodicCommitsDespiteFailure(t *testing.T) {
	// Offsets advance on a clock, not on an outcome. A record whose action
	// failed is gone — which is what `periodic` says it does, and what
	// `commit_mode = "manual"` silently did until it was rejected at load.
	assert.Equal(t, "B", firstRecordAfterFailure(t, `    ack = "periodic"`))
}

func TestKfakeAutoBehindAQueueStillRedeliversAFailedRecord(t *testing.T) {
	// The combination this receiver refused until it had a per-record settler,
	// and the whole point of having one. Delivery returns as soon as the record
	// is queued, so without a settler the offset would advance there and a
	// failing action would have nothing left to redeliver.
	//
	// The settler travels on the record's context instead, so the acknowledgement
	// is decided by the queue's worker rather than by the enqueue.
	assert.Equal(t, "A", firstRecordAfterFailure(t, `
    ack        = "auto"
    queue_size = 10`))
}

func TestKfakeAutoBehindAPartitionedQueueStillRedeliversAFailedRecord(t *testing.T) {
	// Partitioning is what makes completion order stop matching delivery order,
	// which is the case a low-water mark exists for. One record and one failure
	// is the degenerate version; TestKfakeMarkStopsAtTheRefusedRecord below is
	// the one that needs the prefix.
	assert.Equal(t, "A", firstRecordAfterFailure(t, `
    ack           = "auto"
    queue_size    = 10
    partitions    = 4
    partition_key = ctx.topic`))
}

func TestKfakeManualNackLeavesTheRecordForTheNextConsumer(t *testing.T) {
	// Under manual the configuration decides, and here it refuses. With no
	// dlq_topic there is nowhere to put the record, so the committed offset
	// stops at it.
	assert.Equal(t, "A", firstRecordAfterFailureWithAction(t, `
    ack            = "manual"
    settle_timeout = "30s"`, `
    action = [
      send(ctx, bus.main, "seen", ctx.msg),
      inbound::nack(ctx, "refused by the test"),
    ]`))
}

func TestKfakeManualAckCommitsEvenThoughTheActionFails(t *testing.T) {
	// The case ack = "auto" cannot express: acknowledge the record *even
	// though* handling failed. A delivery settles once, so the ack wins and the
	// nack that the failure would otherwise cause is a no-op.
	assert.Equal(t, "B", firstRecordAfterFailureWithAction(t, `
    ack            = "manual"
    settle_timeout = "30s"`, `
    action = [
      send(ctx, bus.main, "seen", ctx.msg),
      inbound::ack(ctx),
      ctx.msg + 1,
    ]`))
}

// countingSubscriber signals once it has seen n messages.
type countingSubscriber struct {
	bus.BaseSubscriber
	mu   sync.Mutex
	n    int
	want int
	done chan struct{}
}

func (s *countingSubscriber) OnEvent(_ context.Context, _ string, _ any, _ map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.n == s.want {
		close(s.done)
	}
	return nil
}

func TestKfakeMarkStopsAtTheRefusedRecordHoweverMuchCompletesAboveIt(t *testing.T) {
	// The property a low-water mark exists for, and the one a naive
	// "commit what just finished" gets wrong.
	//
	// Six records are processed across four partitions, so completion order is
	// genuinely not delivery order. The record at offset 2 fails; the three
	// above it succeed, and under a naive tracker one of those completions
	// would commit offset 6 and lose the failure. The committed offset must
	// stop at 2, so 2 is what the next consumer is handed.
	cluster, err := kfake.NewCluster(kfake.SeedTopics(1, "intopic"))
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	addr := cluster.ListenAddrs()[0]

	prod, err := kgo.NewClient(kgo.SeedBrokers(addr))
	require.NoError(t, err)
	t.Cleanup(prod.Close)

	produce := func(key, value string) {
		t.Helper()
		require.NoError(t, prod.ProduceSync(context.Background(), &kgo.Record{
			Topic: "intopic", Key: []byte(key), Value: []byte(value),
		}).FirstErr())
	}

	// `ctx.msg + 1` succeeds on a number and fails on a string, so the JSON
	// value is what decides which record is refused — no timing involved.
	//
	// The record key reaches vinculum_topic as ctx.key and nothing later, so
	// routing it into the topic is what gives partition_key something to spread
	// on. That is the documented idiom, exercised here.
	first := fmt.Sprintf(`
bus "main" {}

client "kafka" "k" {
  brokers = [%q]

  receiver "r" {
    group_id      = "g"
    start_offset  = "earliest"
    ack           = "auto"
    queue_size    = 50
    partitions    = 4
    partition_key = ctx.topic

    action = [
      send(ctx, bus.main, "seen", ctx.msg),
      ctx.msg + 1,
    ]

    subscription "intopic" {
      vinculum_topic = "in/${ctx.key}"
    }
  }
}
`, addr)

	config, stopFirst := startConfig(t, first)
	seen := &countingSubscriber{want: 6, done: make(chan struct{})}
	require.NoError(t, config.Buses["main"].Subscribe(context.Background(), "seen", seen))

	values := []string{`0`, `1`, `"R2"`, `3`, `4`, `5`}
	for i, v := range values {
		produce(fmt.Sprintf("k%d", i), v)
	}

	select {
	case <-seen.done:
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for all six records to be processed")
	}

	stopFirst()

	replay := fmt.Sprintf(`
bus "main" {}

client "kafka" "k" {
  brokers = [%q]

  receiver "r" {
    group_id     = "g"
    start_offset = "earliest"
    subscriber   = bus.main

    subscription "intopic" {
      vinculum_topic = "in"
    }
  }
}
`, addr)

	second, _ := startConfig(t, replay)
	got := &firstMessageSubscriber{got: make(chan string, 1)}
	require.NoError(t, second.Buses["main"].Subscribe(context.Background(), "in", got))

	produce("k6", `"MARKER"`)

	select {
	case msg := <-got.got:
		assert.Equal(t, "R2", msg,
			"the refused record is what the mark stopped at; MARKER means an offset was committed past it")
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for the second consumer to receive a record")
	}
}

func TestKfakeManualCommitsNothingWhenTheActionSettlesNothing(t *testing.T) {
	// An action that forgets to settle leaves the record outstanding, so the
	// mark never moves. The timeout here is long enough never to fire, which is
	// what makes this about the unsettled record rather than about the bound —
	// TestKfakeSettleTimeoutExpiryNacksAnUnsettledRecord covers the expiry.
	assert.Equal(t, "A", firstRecordAfterFailureWithAction(t, `
    ack            = "manual"
    settle_timeout = "30s"`, `
    action = send(ctx, bus.main, "seen", ctx.msg)`))
}

func TestKfakeSettleTimeoutExpiryNacksAnUnsettledRecord(t *testing.T) {
	// The bound that keeps ack = "manual" from wedging a partition on an
	// expression that forgets to settle. Nothing settles this record, so the
	// deadline is the only thing that can — and a nack with dlq_topic set is a
	// positive signal it fired: the record turns up in the dead-letter topic
	// carrying the reason, which nothing else in the receiver would put there.
	cluster, err := kfake.NewCluster(kfake.SeedTopics(1, "intopic", "dlq"))
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	addr := cluster.ListenAddrs()[0]

	prod, err := kgo.NewClient(kgo.SeedBrokers(addr))
	require.NoError(t, err)
	t.Cleanup(prod.Close)
	require.NoError(t, prod.ProduceSync(context.Background(), &kgo.Record{
		Topic: "intopic", Key: []byte("k"), Value: []byte(`"A"`),
	}).FirstErr())

	startConfig(t, fmt.Sprintf(`
bus "main" {}

client "kafka" "k" {
  brokers = [%q]

  receiver "r" {
    group_id       = "g"
    start_offset   = "earliest"
    ack            = "manual"
    settle_timeout = "250ms"
    dlq_topic      = "dlq"

    action = send(ctx, bus.main, "seen", ctx.msg)

    subscription "intopic" {
      vinculum_topic = "in"
    }
  }
}
`, addr))

	dlq, err := kgo.NewClient(
		kgo.SeedBrokers(addr),
		kgo.ConsumeTopics("dlq"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	require.NoError(t, err)
	t.Cleanup(dlq.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	fetches := dlq.PollRecords(ctx, 1)
	require.NoError(t, fetches.Err(), "nothing reached dlq_topic, so the deadline never nacked")

	records := fetches.Records()
	require.Len(t, records, 1)
	assert.Equal(t, []byte(`"A"`), records[0].Value, "the dead letter keeps the original value")

	var reason string
	for _, h := range records[0].Headers {
		if h.Key == "vinculum-error" {
			reason = string(h.Value)
		}
	}
	assert.Contains(t, reason, "settle_timeout expired",
		"the dead letter should say the deadline is what refused the record")
}
