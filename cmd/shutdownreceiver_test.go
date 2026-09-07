package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"go.uber.org/zap"
)

// receiverConfig boots a redis_stream receiver reading `events` into a queue
// deep enough to hold everything at once, so a shutdown meets a full queue
// rather than an idle one. settle is the second half of the action: what the
// configuration does about the delivery once it has counted it.
func receiverConfig(t *testing.T, addr, ack, settle string) *config.Config {
	t.Helper()
	cfg, diags := config.NewConfig().
		WithSources([]byte(fmt.Sprintf(`
bus "main" {}

var "handled" { value = 0 }

client "redis" "base" { address = %q }

client "redis_stream" "rs" {
    connection = client.base

    consumer "in" {
        stream        = "events"
        group         = "g"
        consumer_name = "c"
        block_timeout = "50ms"
        ack           = %q
        settle_timeout = "5s"
        queue_size    = 1000
        action        = [
            increment(ctx, var.handled),
            %s
        ]
    }
}
`, addr, ack, settle))).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	return cfg
}

// settles is the action of a configuration that acknowledges what it handled;
// holds is one that has not got round to it. Under `ack = "manual"` the
// configuration is what acknowledges, so both are legal and only the first ends
// with Redis satisfied.
const (
	settles = "inbound::ack(ctx)"
	holds   = "true"
)

func produce(t *testing.T, addr string, n int) {
	t.Helper()
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	defer c.Close()
	for i := 0; i < n; i++ {
		require.NoError(t, c.XAdd(context.Background(), &goredis.XAddArgs{
			Stream: "events",
			Values: map[string]any{"n": i},
		}).Err())
	}
}

// pending reports how many entries the group has claimed and nobody has
// acknowledged — the number the broker will redeliver on the next boot.
func pending(t *testing.T, addr string) int64 {
	t.Helper()
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	defer c.Close()
	res, err := c.XPending(context.Background(), "events", "g").Result()
	require.NoError(t, err)
	return res.Count
}

func startAllOrFail(t *testing.T, cfg *config.Config) {
	t.Helper()
	for _, s := range cfg.Startables {
		require.NoError(t, s.Start())
	}
}

// The flagship, in the language a user writes: entries on a stream, a receiver
// feeding a queue, and a shutdown in the middle of it. Every entry the receiver
// took must be acknowledged before the connection closes.
//
// Four things have to hold at once for the pending count to reach zero, and
// each is a different phase: the receiver stops reading, the queue runs its
// backlog, the acknowledgement travels back over a connection nothing has
// closed yet, and only then does the client disconnect. Any one of them out of
// order leaves entries pending — which is not lost work, since Redis would
// redeliver them, but it is the duplicate on the next boot this whole change
// exists to avoid.
func TestShutdownAcknowledgesWhatTheReceiverTook(t *testing.T) {
	// Under auto the framework settles when the work finishes and the action
	// says nothing about it; under manual the action is what settles. Both must
	// end with Redis satisfied, by different routes.
	for ack, action := range map[string]string{"auto": holds, "manual": settles} {
		t.Run(ack, func(t *testing.T) {
			mr := miniredis.RunT(t)
			cfg := receiverConfig(t, mr.Addr(), ack, action)
			startAllOrFail(t, cfg)

			const entries = 50
			produce(t, mr.Addr(), entries)

			// Wait until the receiver has taken some of it, so the shutdown
			// lands mid-flight rather than before anything started. The gate is
			// the action's own count and not the pending list: under auto the
			// acknowledgement follows the work closely enough that a poll of
			// the broker can miss every entry on its way through.
			require.Eventually(t, func() bool { return handledCount(cfg) > 0 },
				3*time.Second, 10*time.Millisecond, "the receiver never read anything")

			shutdown(cfg, zap.NewNop())

			assert.Zero(t, pending(t, mr.Addr()),
				"entries the receiver took were left unacknowledged at exit")
		})
	}
}

// The phase can only wait for what registered, and a receiver is the newest
// thing to register. Both holders it produces are named: the queue in front of
// the action, and the receiver behind it — which is the hop past the queue,
// because the acknowledgement is not owed until the work is done.
//
// They are named *differently*, which is the part worth asserting. One block
// registers both, so the obvious name is the same name twice — and the give-up
// log line is this phase's only operator-facing output, where two identical
// names carrying two different numbers answer nothing.
func TestAReceiverRegistersItselfAsAHolderAndADrainable(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := receiverConfig(t, mr.Addr(), "auto", holds)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	names := make([]string, 0, len(cfg.InFlight))
	for _, holder := range cfg.InFlight {
		names = append(names, holder.Name)
		if strings.HasSuffix(holder.Name, " unsettled") {
			assert.Nil(t, holder.Close,
				"a receiver holds entries belonging to a broker; only a settle finishes one")
		}
		assert.NotNil(t, holder.Pending, "%s registered nothing to wait on", holder.Name)
	}
	assert.Equal(t, []string{"bus.main", "redis_stream/rs/in", "redis_stream/rs/in unsettled"}, names,
		"a receiver should register its queue and its own unsettled deliveries, distinguishably")

	assert.Len(t, cfg.Drainables, 1,
		"a receiver that never drains keeps consuming until the transports close")
}

// A client with producers and no consumers has nothing to drain, so it does not
// register as a Drainable and owes the broker nothing to wait for. The other
// three receivers pin this; redis did not, and its `len(consumers) > 0` guard
// was the only one of the four unasserted.
func TestASendOnlyRedisStreamClientDoesNotRegisterADrain(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg, diags := config.NewConfig().
		WithSources([]byte(fmt.Sprintf(`
client "redis" "base" { address = %q }

client "redis_stream" "rs" {
    connection = client.base
    producer "out" { stream = "events" }
}
`, mr.Addr()))).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	assert.Empty(t, cfg.Drainables)
	for _, holder := range cfg.InFlight {
		assert.NotContains(t, holder.Name, "redis_stream/",
			"a client with no consumers owes the broker nothing")
	}
}

// The invariant behind the assertion above, stated once for every holder a
// configuration can produce — so the three receivers still to grow a drain
// inherit it rather than rediscovering it.
func TestEveryInFlightHolderIsNamedDistinctly(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := receiverConfig(t, mr.Addr(), "auto", holds)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	seen := make(map[string]int, len(cfg.InFlight))
	for _, holder := range cfg.InFlight {
		seen[holder.Name]++
	}
	for name, count := range seen {
		assert.Equal(t, 1, count,
			"%d holders share the name %q, so the shutdown log cannot tell them apart", count, name)
	}
}

// Draining is the phase that has to come first, and the receiver is the only
// thing left that can still be fed once the listeners have closed. A receiver
// that stops reading only at Stop spends the whole quiesce budget racing a
// producer it could have stopped listening to.
func TestDrainingStopsTheReceiverReading(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := receiverConfig(t, mr.Addr(), "auto", holds)
	startAllOrFail(t, cfg)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	produce(t, mr.Addr(), 1)
	require.Eventually(t, func() bool { return handledCount(cfg) == 1 },
		3*time.Second, 10*time.Millisecond)

	drain(cfg, zap.NewNop(), config.DefaultShutdownTimeout)

	produce(t, mr.Addr(), 5)
	time.Sleep(300 * time.Millisecond) // several block timeouts

	assert.Equal(t, int64(1), handledCount(cfg),
		"the receiver kept consuming after the drain phase")
}

// Why the receiver registers a holder of its own, when the queue in front of it
// already registers one. The queue models where a message *waits*; it says
// nothing about whether anyone answered for it. Under `ack = "manual"` those
// come apart by however long the configuration takes to settle — the pipeline
// is empty and the process still owes Redis an acknowledgement — and the fourth
// phase would close the connection that acknowledgement has to travel over.
func TestTheReceiverIsWaitedForAfterThePipelineIsEmpty(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := receiverConfig(t, mr.Addr(), "manual", holds)
	startAllOrFail(t, cfg)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	produce(t, mr.Addr(), 1)
	require.Eventually(t, func() bool { return handledCount(cfg) == 1 },
		3*time.Second, 10*time.Millisecond, "the action never ran")

	// Give the queue's goroutine the moment it needs to go idle after the
	// action returned, so "the pipeline is empty" is a settled fact rather than
	// a race the assertion below would win either way.
	require.Eventually(t, func() bool { return queuedWork(cfg) == 0 },
		time.Second, 10*time.Millisecond, "the pipeline never emptied")

	assert.Positive(t, pendingWork(cfg),
		"the pipeline is empty and nothing acknowledged the entry, so a shutdown "+
			"that only watches the pipeline would disconnect before it could")
}

// queuedWork totals every holder except the receivers' own — the pipeline half
// of the count, which is all teardown used to be able to see.
func queuedWork(cfg *config.Config) int {
	total := 0
	for _, holder := range cfg.InFlight {
		if holder.Close != nil || strings.HasPrefix(holder.Name, "bus.") {
			total += holder.Pending()
		}
	}
	return total
}

// handledCount reports how many messages the action has run, and reports -1
// rather than failing if it cannot say.
//
// The distinction matters because this is read from inside `Eventually`
// conditions, which poll on their own goroutine and can outlive the test that
// started them. A `require` there fails a test that has already finished, which
// panics the whole run instead of failing one case.
func handledCount(cfg *config.Config) int64 {
	handled, err := types.GetVariableFromCapsule(cfg.CtyVarMap["handled"])
	if err != nil {
		return -1
	}
	val, err := handled.Get(context.Background(), nil)
	if err != nil || val.IsNull() {
		return -1
	}
	count, _ := val.AsBigFloat().Int64()
	return count
}
