package redisstream_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/clients/redisstream"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// boolRecorder collects the booleans a config sends it, which is how these
// tests read the return value of inbound::ack() from inside VCL.
//
// send() publishes a cty.Value rather than a Go bool, so the payload is
// unwrapped here rather than stringified — fmt.Sprint of a cty bool is
// "cty.True", which compares equal to nothing anyone would expect.
type boolRecorder struct {
	bus.BaseSubscriber
	mu  sync.Mutex
	got []bool
}

func (r *boolRecorder) OnEvent(_ context.Context, _ string, msg any, _ map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch v := msg.(type) {
	case cty.Value:
		r.got = append(r.got, v.Type() == cty.Bool && !v.IsNull() && v.True())
	case bool:
		r.got = append(r.got, v)
	default:
		r.got = append(r.got, fmt.Sprint(msg) == "true")
	}
	return nil
}

func (r *boolRecorder) values() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.got...)
}

func (r *boolRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func pendingIn(t *testing.T, addr string) int64 {
	t.Helper()
	rc := goredis.NewClient(&goredis.Options{Addr: addr})
	defer rc.Close()
	pending, err := rc.XPending(context.Background(), "events", "g").Result()
	require.NoError(t, err)
	return pending.Count
}

// pendingCount is pendingIn for a polling condition, where pendingIn must not
// be used: testify runs an Eventually or Never condition on its own goroutine,
// and require there calls t.FailNow off the test goroutine — which is
// runtime.Goexit from somewhere the testing package is not expecting it. The
// last poll can also outlive the test and find miniredis already closed by
// t.Cleanup, which turns an ordinary shutdown into a failure in whatever test
// runs next.
//
// A read that fails answers -1, which is neither "drained" nor "one pending",
// so a condition written against either keeps waiting rather than resolving on
// the strength of an error.
func pendingCount(addr string) int64 {
	rc := goredis.NewClient(&goredis.Options{Addr: addr})
	defer rc.Close()
	pending, err := rc.XPending(context.Background(), "events", "g").Result()
	if err != nil {
		return -1
	}
	return pending.Count
}

// Two subscriptions match one topic and both take responsibility for the entry.
// Exactly one of them settled it, and the broker heard about it once.
//
// This is the property the whole design rests on. A bus is publish/subscribe
// and an acknowledgement is point-to-point, so an ack has to mean "someone took
// responsibility" rather than "everyone finished" — refcounting the matching
// subscribers would be wrong, because the set changes at runtime and a
// subscription that only logs must not gate a broker ack.
func TestSettleOnceUnderFanOut(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
bus "main" {}
bus "results" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream         = "events"
        group          = "g"
        block_timeout  = "100ms"
        ack            = "manual"
        settle_timeout = "30s"
        subscriber     = bus.main
    }
}

subscription "first" {
    target = bus.main
    topics = ["events"]
    action = send(ctx, bus.results, "settled", inbound::ack(ctx))
}

subscription "second" {
    target = bus.main
    topics = ["events"]
    action = send(ctx, bus.results, "settled", inbound::ack(ctx))
}
`, mr.Addr())
	c := buildConfig(t, src)
	rec := &boolRecorder{}
	require.NoError(t, c.Buses["results"].Subscribe(context.Background(), "settled", rec))
	startLifecycle(t, c)

	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
	require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

	require.Eventually(t, func() bool { return rec.count() == 2 }, 3*time.Second, 20*time.Millisecond,
		"both subscriptions should have run")

	got := rec.values()
	settled := 0
	for _, v := range got {
		if v {
			settled++
		}
	}
	assert.Equal(t, 1, settled, "exactly one subscription should report that it settled the entry, got %v", got)

	assert.Eventually(t, func() bool { return pendingCount(mr.Addr()) == 0 }, 3*time.Second, 20*time.Millisecond,
		"the entry should have been acked")
}

// The settle survives every hop: an async queue, then a bus, then a
// subscription's action. This is the test that proves specs/CONTEXT-ACK.md §11.
//
// queue_size makes delivery return at the moment the entry is queued, which is
// what couples an auto acknowledgement to the wrong outcome — the receiver
// hears "delivered" before any work has happened. With the settle explicit and
// riding the context, delivery's return value stops being the acknowledgement,
// so the entry is still pending until something downstream says otherwise.
func TestSettleSurvivesTheQueueAndTheBus(t *testing.T) {
	const consumer = `
bus "main" {}
bus "results" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream         = "events"
        group          = "g"
        block_timeout  = "100ms"
        ack            = "manual"
        settle_timeout = "30s"
        queue_size     = 16
        subscriber     = bus.main
    }
}

subscription "worker" {
    target = bus.main
    topics = ["events"]
    action = %s
}
`

	t.Run("an action that settles it", func(t *testing.T) {
		mr := miniredis.RunT(t)
		c := buildConfig(t, fmt.Sprintf(consumer, mr.Addr(),
			`send(ctx, bus.results, "settled", inbound::ack(ctx))`))
		rec := &boolRecorder{}
		require.NoError(t, c.Buses["results"].Subscribe(context.Background(), "settled", rec))
		startLifecycle(t, c)

		wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
		require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

		require.Eventually(t, func() bool { return rec.count() == 1 }, 3*time.Second, 20*time.Millisecond,
			"the subscription should have run")
		assert.Equal(t, []bool{true}, rec.values())
		assert.Eventually(t, func() bool { return pendingCount(mr.Addr()) == 0 }, 3*time.Second, 20*time.Millisecond,
			"the entry should be acked once the action that handled it says so")
	})

	t.Run("an action that does not", func(t *testing.T) {
		mr := miniredis.RunT(t)
		c := buildConfig(t, fmt.Sprintf(consumer, mr.Addr(),
			`send(ctx, bus.results, "settled", false)`))
		rec := &boolRecorder{}
		require.NoError(t, c.Buses["results"].Subscribe(context.Background(), "settled", rec))
		startLifecycle(t, c)

		wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
		require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

		require.Eventually(t, func() bool { return rec.count() == 1 }, 3*time.Second, 20*time.Millisecond,
			"the subscription should have run")
		// The defect this design removes: the entry would already be acked here,
		// because delivery returned nil at the moment it was queued.
		assert.Never(t, func() bool { return pendingCount(mr.Addr()) != 1 }, 500*time.Millisecond, 50*time.Millisecond,
			"nothing settled the entry, so it must still be pending")
	})
}

// Nacking settles the entry without acknowledging it, so it stays in the
// pending list for reclaim_min_idle and dead_letter_after to act on. A later
// ack finds the delivery already settled and says so.
func TestNackSettlesWithoutAcking(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
bus "main" {}
bus "results" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream         = "events"
        group          = "g"
        block_timeout  = "100ms"
        ack            = "manual"
        settle_timeout = "30s"
        action         = [
            send(ctx, bus.results, "settled", inbound::nack(ctx, "could not handle it")),
            send(ctx, bus.results, "settled", inbound::ack(ctx)),
        ]
    }
}
`, mr.Addr())
	c := buildConfig(t, src)
	rec := &boolRecorder{}
	require.NoError(t, c.Buses["results"].Subscribe(context.Background(), "settled", rec))
	startLifecycle(t, c)

	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
	require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

	require.Eventually(t, func() bool { return rec.count() == 2 }, 3*time.Second, 20*time.Millisecond,
		"both settle calls should have run")
	assert.Equal(t, []bool{true, false}, rec.values(),
		"the nack settles it and the ack that follows finds it already settled")
	assert.EqualValues(t, 1, pendingIn(t, mr.Addr()),
		"a nacked entry stays pending for the receiver's own policy")
}

// Forgetting to settle should be diagnosable rather than a slow stall, so a
// delivery nobody settles within settle_timeout is nacked and logged against
// the receiver that is holding it.
func TestSettleTimeoutNacksAndSaysWhichReceiver(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
bus "results" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream         = "events"
        group          = "g"
        block_timeout  = "100ms"
        ack            = "manual"
        settle_timeout = "150ms"
        action         = send(ctx, bus.results, "ran", true)
    }
}
`, mr.Addr())

	// Built with the observing logger rather than assigned one afterwards: the
	// wrapper takes its logger when the receiver is built, so a later
	// assignment would be watching a logger nothing holds.
	core, logs := observer.New(zap.WarnLevel)
	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.New(core)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	rec := &boolRecorder{}
	require.NoError(t, c.Buses["results"].Subscribe(context.Background(), "ran", rec))
	startLifecycle(t, c)

	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
	require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

	require.Eventually(t, func() bool { return logs.Len() > 0 }, 3*time.Second, 20*time.Millisecond,
		"an unsettled entry should be reported once its bound expires")

	entry := logs.All()[0]
	assert.Contains(t, entry.Message, "settle_timeout")
	assert.Equal(t, "redis_stream/rs/in", entry.ContextMap()["receiver"],
		"the log should name the receiver holding the message")

	// Nacked, not acked: the entry stays pending, where reclaim and
	// dead-lettering can still reach it.
	assert.EqualValues(t, 1, pendingIn(t, mr.Addr()))
}

// A receiver routed into a bus nothing consumes is a config error that would
// otherwise be silent and slow: nothing settles the message, so it sits until
// settle_timeout nacks it, is redelivered, and takes the same path again.
//
// The republish under `$undeliverable` carries the original context, so the
// settler travels with it and the policy becomes ordinary VCL — the message is
// rejected with a real reason at once rather than after the timeout. The proof
// is the receiver's own log line: only a nack that reached the consumer's
// settler could have produced it.
func TestAnUndeliverableMessageCanBeNackedAtOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
bus "main" { undeliverable = true }

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream         = "events"
        group          = "g"
        block_timeout  = "100ms"
        ack            = "manual"
        settle_timeout = "10m"
        subscriber     = bus.main
    }
}

# Nothing subscribes to "events", so every entry lands here instead.
subscription "unroutable" {
    target = bus.main
    topics = ["$undeliverable"]
    action = inbound::nack(ctx, "no matching subscription")
}
`, mr.Addr())

	core, logs := observer.New(zap.InfoLevel)
	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.New(core)).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	startLifecycle(t, c)

	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
	require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

	// settle_timeout is ten minutes, so nothing but the republished message
	// could have settled this within the wait.
	var nacked []observer.LoggedEntry
	require.Eventually(t, func() bool {
		nacked = logs.FilterMessageSnippet("entry nacked").All()
		return len(nacked) == 1
	}, 3*time.Second, 20*time.Millisecond,
		"the $undeliverable handler's nack should have reached the consumer")
	assert.Equal(t, "no matching subscription", nacked[0].ContextMap()["reason"])

	assert.EqualValues(t, 1, pendingIn(t, mr.Addr()),
		"a nacked entry stays pending for the receiver's own policy")
}

// A message that arrived over a transport with no acknowledgement settles
// nothing and reports so. This is what makes the functions safe to call from
// shared subscription code that does not know which receiver produced what it
// is handling.
func TestSettlingSomethingThatNeverArrivedOverABrokerIsANoOp(t *testing.T) {
	src := `
bus "main" {}
bus "results" {}

subscription "s" {
    target = bus.main
    topics = ["anything"]
    action = [
        send(ctx, bus.results, "r", inbound::ack(ctx)),
        send(ctx, bus.results, "r", inbound::nack(ctx, "why not")),
        send(ctx, bus.results, "r", inbound::keepalive(ctx)),
    ]
}
`
	c := buildConfig(t, src)
	rec := &boolRecorder{}
	require.NoError(t, c.Buses["results"].Subscribe(context.Background(), "r", rec))
	startLifecycle(t, c)

	require.NoError(t, c.Buses["main"].Publish(context.Background(), "anything", "hi"))

	require.Eventually(t, func() bool { return rec.count() == 3 }, 3*time.Second, 20*time.Millisecond,
		"all three settle calls should have run")
	assert.Equal(t, []bool{false, false, false}, rec.values())
}

// ack = "auto" is what every one of these receivers did before `ack` existed,
// and reproducing that exactly is the migration's whole claim.
func TestAckAutoIsTodaysBehaviour(t *testing.T) {
	for _, tc := range []struct {
		name string
		ack  string
	}{
		{"stated", `ack = "auto"`},
		{"omitted", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			src := fmt.Sprintf(`
bus "main" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream        = "events"
        group         = "g"
        block_timeout = "100ms"
        subscriber    = bus.main
        %s
    }
}
`, mr.Addr(), tc.ack)
			c := buildConfig(t, src)
			startLifecycle(t, c)

			wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
			require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

			assert.Eventually(t, func() bool { return pendingCount(mr.Addr()) == 0 }, 3*time.Second, 20*time.Millisecond,
				"the entry should be acked once delivery returns without error")
		})
	}
}

// The two halves of the settle_timeout rule, which exists because no one value
// suits both a 50ms enrichment and a five-minute batch.
func TestSettleTimeoutIsRequiredExactlyWhereItApplies(t *testing.T) {
	consumer := func(body string) string {
		return fmt.Sprintf(`
bus "main" {}

client "redis" "base" { address = "127.0.0.1:1" }
client "redis_stream" "rs" {
    connection = client.base

    consumer "in" {
        stream     = "events"
        group      = "g"
        subscriber = bus.main
        %s
    }
}
`, body)
	}

	t.Run("manual without it", func(t *testing.T) {
		_, diags := cfg.NewConfig().
			WithSources([]byte(consumer(`ack = "manual"`))).
			WithLogger(zap.NewNop()).Build()
		require.True(t, diags.HasErrors())
		assert.Contains(t, diags.Error(), "requires settle_timeout")
	})

	// Permitted under auto, because the XACK now follows the work rather than
	// delivery's return — so there is a genuinely unsettled entry for a bound
	// to apply to. Not required: the framework settles at a known point, and a
	// configuration that asks for no bound is no worse off than it was.
	t.Run("auto with it", func(t *testing.T) {
		_, diags := cfg.NewConfig().
			WithSources([]byte(consumer("settle_timeout = \"30s\""))).
			WithLogger(zap.NewNop()).Build()
		require.False(t, diags.HasErrors(), diags.Error())
	})
}

// A queue makes delivery return at the moment the entry is queued, which used
// to mean `auto` XACKed before the handler ran. It no longer does: the entry
// carries a settler that travels with it, so the XACK arrives when the work
// finishes, however far down the chain that is. Every combination here is now
// accepted, and vinculum-redis proves the behaviour end to end against a real
// stream.
func TestQueueSizeIsAllowedWithEitherAckMode(t *testing.T) {
	consumer := func(body string) string {
		return fmt.Sprintf(`
bus "main" {}

client "redis" "base" { address = "127.0.0.1:1" }
client "redis_stream" "rs" {
    connection = client.base

    consumer "in" {
        stream     = "events"
        group      = "g"
        subscriber = bus.main
        queue_size = 16
        %s
    }
}
`, body)
	}

	for _, body := range []string{``, `ack = "auto"`} {
		t.Run("accepted with "+body, func(t *testing.T) {
			_, diags := cfg.NewConfig().
				WithSources([]byte(consumer(body))).
				WithLogger(zap.NewNop()).Build()
			require.False(t, diags.HasErrors(), diags.Error())
		})
	}

	t.Run("kept under manual", func(t *testing.T) {
		_, diags := cfg.NewConfig().
			WithSources([]byte(consumer("ack = \"manual\"\n        settle_timeout = \"30s\""))).
			WithLogger(zap.NewNop()).Build()
		require.False(t, diags.HasErrors(), diags.Error())
	})
}

// The old spelling is met with what it became, rather than with gohcl's
// "argument named auto_ack is not expected here".
func TestRetiredAutoAckSaysWhatItBecame(t *testing.T) {
	src := `
bus "main" {}

client "redis" "base" { address = "127.0.0.1:1" }
client "redis_stream" "rs" {
    connection = client.base

    consumer "in" {
        stream     = "events"
        group      = "g"
        subscriber = bus.main
        auto_ack   = false
    }
}
`
	_, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors(), "auto_ack should no longer be accepted")
	msg := diags.Error()
	assert.Contains(t, msg, `"auto_ack" is now "ack"`)
	assert.Contains(t, msg, "0.46.0")
	assert.Contains(t, msg, `ack = "manual"`)
}
