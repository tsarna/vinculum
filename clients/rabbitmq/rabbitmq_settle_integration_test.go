//go:build integration

// Integration tests for settle-on-completion against a REAL RabbitMQ broker.
//
// Same gating as rabbitmq_integration_test.go: the `integration` build tag plus
// RABBITMQ_HOST. See that file's header for how to run these.
//
// What these are for, and why a real broker is the only place to run them: the
// unit and miniredis tests prove that the settle *decision* is made at the right
// moment, but "acknowledged" and "nacked" are claims about what a broker
// believes. Here the broker is asked. A nacked delivery is rejected without
// requeue, so it lands on the queue's dead-letter exchange — which makes the
// difference between an acknowledgement and a rejection directly observable,
// rather than inferred from a counter this process maintains about itself.
package rabbitmq_test

import (
	"context"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
)

// gatedFailer holds the delivery inside OnEvent until released, then returns
// err. Holding it is the point: it is what makes "did the acknowledgement wait
// for the work?" a question the broker can answer while the work is still going.
type gatedFailer struct {
	bus.BaseSubscriber
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func newGatedFailer(err error) *gatedFailer {
	return &gatedFailer{entered: make(chan struct{}, 1), release: make(chan struct{}), err: err}
}

func (g *gatedFailer) Release() { g.once.Do(func() { close(g.release) }) }

func (g *gatedFailer) OnEvent(context.Context, string, any, map[string]string) error {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	return g.err
}

// deadLetteredQueue declares a work queue whose rejected messages go to a
// dead-letter exchange, plus a sink on that exchange to observe them. It
// returns the work queue name and the sink queue name.
//
// Vinculum's own `declare` block deliberately does not expose queue arguments
// — the documentation says to use a RabbitMQ policy — so the queue is declared
// here and the receiver simply consumes it.
func deadLetteredQueue(t *testing.T, e brokerEnv, ch *amqp.Channel) (work, sink string) {
	t.Helper()

	dlx := uniqueName("vinculum.test.dlx")
	require.NoError(t, ch.ExchangeDeclare(dlx, "fanout", false, true, false, false, nil),
		"declare dead-letter exchange")

	work = uniqueName("vinculum.test.settle")
	_, err := ch.QueueDeclare(work, false, false, false, false, amqp.Table{
		"x-dead-letter-exchange": dlx,
	})
	require.NoError(t, err, "declare work queue with a dead-letter exchange")
	t.Cleanup(func() { _, _ = ch.QueueDelete(work, false, false, false) })

	require.NoError(t, ch.QueueBind(work, "settle.#", exInbound, false, nil), "bind work queue")

	sink = declareSink(t, ch, dlx, "")
	return work, sink
}

// The acknowledgement waits for the work, and reflects what the work did.
//
// This is the whole of `specs/old/SETTLE-ON-COMPLETION.md`, asked of a broker.
// `queue_size` makes delivery return the moment the message is queued, so under
// the old behaviour the message was acknowledged there — before the subscriber
// had run, and with no way to redeliver or dead-letter it if the subscriber
// then failed. Both halves are checked here because only the pair distinguishes
// "settles correctly" from "never settles".
func TestRMQ_Settle_AutoAckFollowsTheWorkBehindAQueue(t *testing.T) {
	t.Run("work that fails is dead-lettered, not acknowledged", func(t *testing.T) {
		e := loadEnv(t)
		admin := dialAdmin(t, e)
		work, sink := deadLetteredQueue(t, e, admin)

		vcl := vclConfig(e, e.brokerURL(), `
  receiver "in" {
    queue      = "`+work+`"
    queue_size = 16
    subscriber = bus.main
  }`, "")
		c := buildCfg(t, vcl)

		gate := newGatedFailer(assert.AnError)
		defer gate.Release()
		require.NoError(t, c.Buses["main"].Subscribe(context.Background(), "settle/#", gate))
		startCfg(t, c)

		publishRaw(t, admin, exInbound, "settle.one", "payload", nil)

		select {
		case <-gate.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the subscriber never ran")
		}

		// Still working. Under the old behaviour the broker had already been
		// told this was handled, so nothing would ever reach the dead-letter
		// exchange no matter what happened next.
		_, early := getWithin(t, admin, sink, 750*time.Millisecond)
		assert.False(t, early, "nothing should be settled while the subscriber is still working")

		gate.Release()

		d, ok := getWithin(t, admin, sink, 10*time.Second)
		require.True(t, ok, "a failed handler must reject the message so the broker can dead-letter it")
		assert.Equal(t, "payload", string(d.Body))
		assert.Equal(t, 0, queueDepth(t, e, work), "and it must not be requeued")
	})

	t.Run("work that succeeds is acknowledged", func(t *testing.T) {
		e := loadEnv(t)
		admin := dialAdmin(t, e)
		work, sink := deadLetteredQueue(t, e, admin)

		vcl := vclConfig(e, e.brokerURL(), `
  receiver "in" {
    queue      = "`+work+`"
    queue_size = 16
    subscriber = bus.main
  }`, "")
		c := buildCfg(t, vcl)

		gate := newGatedFailer(nil)
		defer gate.Release()
		require.NoError(t, c.Buses["main"].Subscribe(context.Background(), "settle/#", gate))
		startCfg(t, c)

		publishRaw(t, admin, exInbound, "settle.two", "payload", nil)

		select {
		case <-gate.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the subscriber never ran")
		}
		gate.Release()

		// Acknowledged: gone from the work queue, and never dead-lettered.
		assert.Eventually(t, func() bool { return queueDepth(t, e, work) == 0 },
			10*time.Second, 200*time.Millisecond, "the message should be acknowledged")
		_, dead := getWithin(t, admin, sink, 1*time.Second)
		assert.False(t, dead, "a handler that succeeded must not dead-letter the message")
	})
}

// The same question one hop further out, which is the arrangement a
// configuration most often actually has: the receiver hands the message to a
// bus, and a `subscription` on that bus does the work.
//
// A bus accepts a message onto its own channel and returns, so this is the hop
// that has to defer as well — and the one where a handle standing in for the
// bus silently reported that the work was done.
func TestRMQ_Settle_AutoAckFollowsAnActionOnTheBus(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	work, sink := deadLetteredQueue(t, e, admin)

	// jsondecode of something that is not JSON fails the action, which is the
	// ordinary way a handler fails: the expression throws.
	vcl := vclConfig(e, e.brokerURL(), `
  receiver "in" {
    queue      = "`+work+`"
    queue_size = 16
    subscriber = bus.main
  }`, `
subscription "worker" {
  target = bus.main
  topics = ["settle/#"]
  action = jsondecode("{{{ not json")
}
`)
	c := buildCfg(t, vcl)
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "settle.three", "payload", nil)

	d, ok := getWithin(t, admin, sink, 10*time.Second)
	require.True(t, ok,
		"a failing action two hops from the receiver must still reject the message")
	assert.Equal(t, "payload", string(d.Body))
}

// Manual settle over the same path, which is what the receiver did before any
// of this and still does: nothing is settled until the configuration says so.
func TestRMQ_Settle_ManualIsUnaffected(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	work, sink := deadLetteredQueue(t, e, admin)

	vcl := vclConfig(e, e.brokerURL(), `
  receiver "in" {
    queue          = "`+work+`"
    queue_size     = 16
    ack            = "manual"
    settle_timeout = "30s"
    subscriber     = bus.main
  }`, `
subscription "worker" {
  target = bus.main
  topics = ["settle/#"]
  action = inbound::nack(ctx, "not for me")
}
`)
	c := buildCfg(t, vcl)
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "settle.four", "payload", nil)

	d, ok := getWithin(t, admin, sink, 10*time.Second)
	require.True(t, ok, "inbound::nack() should reject the message to the dead-letter exchange")
	assert.Equal(t, "payload", string(d.Body))
}
