package redisstream_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/clients/redisstream"
)

// The headline claim of the whole change, written the way a user writes it.
//
// `queue_size` alongside `ack = "auto"` was refused at load, because delivery
// returned the moment the entry was queued and `auto` settled on that return —
// so the entry was acknowledged before anything had handled it. The combination
// is now correct, and this is what "correct" has to mean: the acknowledgement
// waits for the work, and a failure leaves the entry for redelivery.
//
// Neither half is provable by a unit test. The settle happens two hops away
// from the receiver, through a queue and a bus, and the thing that has to be
// observed is what Redis believes about the entry afterwards.
func TestQueueSizeWithAutoAckSettlesOnTheRealOutcome(t *testing.T) {
	const config = `
bus "main" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream        = "events"
        group         = "g"
        block_timeout = "100ms"
        queue_size    = 16
        subscriber    = bus.main
        %s
    }
}

subscription "worker" {
    target = bus.main
    topics = ["events"]
    action = %s
}
`

	// Both spellings of auto, because the default is the one most configurations
	// will be running and the one the refusal used to catch.
	for _, ack := range []struct{ name, line string }{
		{"stated", `ack = "auto"`},
		{"defaulted", ``},
	} {
		t.Run(ack.name, func(t *testing.T) {
			t.Run("work that succeeds is acknowledged", func(t *testing.T) {
				mr := miniredis.RunT(t)
				c := buildConfig(t, fmt.Sprintf(config, mr.Addr(), ack.line,
					`log::debug("handled", { topic = ctx.topic })`))
				startLifecycle(t, c)

				wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
				require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

				assert.Eventually(t, func() bool { return pendingIn(t, mr.Addr()) == 0 },
					3*time.Second, 20*time.Millisecond,
					"the acknowledgement should follow the action, two hops downstream")
			})

			t.Run("work that fails is left for redelivery", func(t *testing.T) {
				mr := miniredis.RunT(t)
				// jsondecode of something that is not JSON fails the action, which
				// is the ordinary way a handler fails: the expression throws.
				c := buildConfig(t, fmt.Sprintf(config, mr.Addr(), ack.line,
					`jsondecode("{{{ not json")`))
				startLifecycle(t, c)

				wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
				require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

				// This is the defect. Before, the entry was XACKed at the moment it
				// entered the queue, so the failure below had nothing left to
				// redeliver and the message was simply gone.
				assert.Never(t, func() bool { return pendingIn(t, mr.Addr()) == 0 },
					1*time.Second, 50*time.Millisecond,
					"a failed action must leave the entry pending for another consumer")
			})
		})
	}
}

// An entry routed into a bus that nothing subscribes to is acknowledged, not
// left outstanding — and that is the whole of what happens, because a settler
// reaching the end of the bus's routing is settled there rather than abandoned.
//
// Nobody asked to hear about undeliverable messages (`undeliverable` is off by
// default), so a topic no subscription matches is a routing outcome the
// configuration chose. Nacking instead would turn an unsubscribed topic into a
// redelivery loop; the bus's `undelivered` counter is the diagnostic for a
// topic pattern that was *meant* to match.
//
// This is also why `settle_timeout` under `auto` is permitted but has very
// little left to bound: the paths that once looked like "nobody ever settles"
// resolve to an ack or a nack on their own. What remains is shutdown, which is
// specs/SETTLE-ON-SHUTDOWN.md's subject.
func TestAnEntryNothingSubscribesToIsAcknowledged(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
bus "main" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream         = "events"
        group          = "g"
        block_timeout  = "100ms"
        queue_size     = 16
        ack            = "auto"
        settle_timeout = "5s"
        subscriber     = bus.main
    }
}
`, mr.Addr())

	c := buildConfig(t, src)
	startLifecycle(t, c)

	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
	require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi", nil))

	assert.Eventually(t, func() bool { return pendingIn(t, mr.Addr()) == 0 },
		3*time.Second, 20*time.Millisecond,
		"nothing wanted it and nothing asked to be told, so it is acknowledged")

	assert.EqualValues(t, 1, c.Buses["main"].UndeliveredTotal(),
		"and it is counted, which is the diagnostic for a pattern that should have matched")
}
