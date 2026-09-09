//go:build integration

// Integration tests for the drain phase against a REAL RabbitMQ broker.
//
// Same gating as rabbitmq_integration_test.go: the `integration` build tag plus
// RABBITMQ_HOST. See that file's header for how to run these.
//
// What these are for. The unit tests in vinculum-rabbitmq drive a fake channel
// whose Cancel closes the delivery channel behind whatever is buffered on it —
// which is what amqp091-go does, and what the whole drain design rests on. But
// that fake encodes a *belief* about the broker and the library, and a belief
// is exactly what a fake cannot check. Three things only a real broker answers:
//
//   - that basic.cancel stops deliveries arriving at all;
//   - that what the broker had already sent is still delivered afterwards,
//     rather than dropped, which is the reason for cancelling rather than
//     cancelling a context;
//   - that a delivery tag issued before the cancel still acknowledges after it,
//     which is the property that makes draining and stopping different
//     operations rather than two names for one.
package rabbitmq_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/config"
)

// drainCfg finds the client's Drainable, which is what teardown's first phase
// would call. Going through the registered interface rather than the concrete
// type is deliberate: a receiver that stopped registering would fail here.
func drainCfg(t *testing.T, c *config.Config, within time.Duration) error {
	t.Helper()
	require.Len(t, c.Drainables, 1, "the rabbitmq client did not register a drain")

	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	return c.Drainables[0].Drain(ctx)
}

// Draining stops deliveries arriving, and the connection stays up. This is the
// half a fake cannot check: that basic.cancel is what a broker honours.
func TestRMQ_Drain_StopsDeliveriesArriving(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	work, _ := deadLetteredQueue(t, e, admin)

	vcl := vclConfig(e, e.brokerURL(), `
  receiver "in" {
    queue      = "`+work+`"
    subscriber = bus.main
  }`, "")
	c := buildCfg(t, vcl)

	seen := newCountingSubscriber()
	require.NoError(t, c.Buses["main"].Subscribe(context.Background(), "settle/#", seen))
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "settle.before", "before", nil)
	require.Eventually(t, func() bool { return seen.count() == 1 },
		10*time.Second, 100*time.Millisecond, "the receiver never consumed anything")

	require.NoError(t, drainCfg(t, c, 10*time.Second))

	// Published after the consumer was withdrawn. It must stay on the queue.
	publishRaw(t, admin, exInbound, "settle.after", "after", nil)

	assert.Eventually(t, func() bool { return readyCount(e, work) == 1 },
		10*time.Second, 200*time.Millisecond,
		"a message published after the drain should still be on the queue")
	assert.Equal(t, 1, seen.count(), "the receiver kept consuming after it was drained")
}

// What the broker had already sent is delivered anyway. This is the reason the
// drain withdraws the consumer rather than cancelling a context: with the
// default prefetch of ten, cancelling would abandon up to ten messages that the
// broker considers outstanding, and every one of them would be redelivered on
// the next boot.
func TestRMQ_Drain_DeliversWhatTheBrokerHadAlreadySent(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	work, _ := deadLetteredQueue(t, e, admin)

	// A gate the receiver's first delivery blocks on, so the rest of the batch
	// is certainly prefetched and unhandled when the drain begins.
	gate := newGatedCounter()
	defer gate.Release()

	vcl := vclConfig(e, e.brokerURL(), `
  receiver "in" {
    queue      = "`+work+`"
    prefetch   = 10
    subscriber = bus.main
  }`, "")
	c := buildCfg(t, vcl)
	require.NoError(t, c.Buses["main"].Subscribe(context.Background(), "settle/#", gate))
	startCfg(t, c)

	const messages = 5
	for i := 0; i < messages; i++ {
		publishRaw(t, admin, exInbound, "settle.batch", fmt.Sprintf("m%d", i), nil)
	}

	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the subscriber never ran")
	}

	drained := make(chan error, 1)
	go func() { drained <- drainCfg(t, c, 30*time.Second) }()

	// Let the batch through only once the drain is under way, so the messages
	// finish *after* the consumer was withdrawn.
	time.Sleep(500 * time.Millisecond)
	gate.Release()

	require.NoError(t, <-drained)

	// Drain is only the first phase. It withdraws the consumer and returns; the
	// phase that waits for a delivery still travelling through a bus is the
	// InFlight quiesce, which drainCfg does not run and which the gate does not
	// move into Drain. So the drain here returned while all five handlers were
	// still blocked on the gate, and the count below would be read before any of
	// them had run — the same barrier the settle suite needs, for the same
	// reason, in the one place TEST-CONFIDENCE §1b's third repair did not reach.
	//
	// Like those two, this fails on the barrier rather than the assertion when
	// the drain abandons the backlog: the receiver's unsettled count stays up
	// and awaitSettled times out first. Acceptable for the same reason — *the
	// configuration never finished settling what it took* is an accurate report
	// of that defect, not a confusing one.
	awaitSettled(t, c, 10*time.Second)

	// Counted, not measured off the queue. An abandoned delivery is
	// unacknowledged rather than ready, so a queue depth of zero says nothing
	// about whether it was handled — and the drain deliberately does not close
	// the channel that would return it to ready. The count is the only thing
	// that distinguishes "delivered the backlog" from "dropped it".
	assert.Equal(t, messages, gate.count(),
		"the drain abandoned messages the broker had already sent")

	// Never, not Eventually. The ready count reached zero when the broker handed
	// these five out, well before the drain, so an Eventually here resolves on
	// the first poll having observed nothing — the count says the same thing
	// whether the backlog was acknowledged or abandoned, which is why the count
	// above is the assertion that carries this test. What the ready count *can*
	// still answer is whether anything came back, and nothing should.
	assert.Never(t, func() bool { return readyCount(e, work) != 0 },
		3*time.Second, 200*time.Millisecond, "and none should be put back")
}

// A delivery tag issued before the drain still acknowledges after it. This is
// the property that makes draining a different operation from stopping: an AMQP
// tag means nothing except on the channel that issued it, so a receiver that
// stopped consuming by giving up its channel would invalidate every outstanding
// acknowledgement in the act of stopping.
//
// Asked with `ack = "manual"`, where the acknowledgement is the configuration's
// to make and can be made deliberately late.
func TestRMQ_Drain_KeepsAnOutstandingTagAcknowledgeable(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	work, sink := deadLetteredQueue(t, e, admin)

	vcl := vclConfig(e, e.brokerURL(), `
  receiver "in" {
    queue          = "`+work+`"
    ack            = "manual"
    settle_timeout = "60s"
    subscriber     = bus.main
  }`, "")
	c := buildCfg(t, vcl)

	holder := newSettlerHolder()
	require.NoError(t, c.Buses["main"].Subscribe(context.Background(), "settle/#", holder))
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "settle.manual", "payload", nil)
	require.Eventually(t, func() bool { return holder.settler() != nil },
		10*time.Second, 100*time.Millisecond, "the subscriber never ran")

	require.NoError(t, drainCfg(t, c, 10*time.Second))

	// The acknowledgement is made after the consumer was withdrawn, on the tag
	// handed out before it.
	settled, err := holder.settler().Ack(context.Background())
	require.NoError(t, err, "the delivery tag went stale during the drain")
	assert.True(t, settled)

	// The Ack above returning nil is what says the tag was still good; the ready
	// count cannot corroborate it, because it reached zero at delivery and stays
	// there whether or not anything settled. What it can say is that the delivery
	// was not handed back, which a nack with requeue — or a channel the drain had
	// no business closing — would have done.
	assert.Never(t, func() bool { return readyCount(e, work) != 0 },
		3*time.Second, 200*time.Millisecond,
		"an acknowledged message must not come back to the queue")
	_, dead := getWithin(t, admin, sink, 1*time.Second)
	assert.False(t, dead, "an acknowledged message must not be dead-lettered")
}

// gatedCounter holds the first delivery until released and counts every
// delivery it sees. Both halves are needed together: the gate is what makes the
// rest of the batch certainly prefetched-and-unhandled when the drain starts,
// and the count is the only thing that can tell whether they were then handled
// — an abandoned delivery is unacknowledged rather than ready, so it shows up
// in no queue depth.
type gatedCounter struct {
	bus.BaseSubscriber
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	seen    chan struct{}
}

func newGatedCounter() *gatedCounter {
	return &gatedCounter{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		seen:    make(chan struct{}, 1024),
	}
}

func (g *gatedCounter) Release() { g.once.Do(func() { close(g.release) }) }

func (g *gatedCounter) OnEvent(context.Context, string, any, map[string]string) error {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	g.seen <- struct{}{}
	return nil
}

func (g *gatedCounter) count() int { return len(g.seen) }

// countingSubscriber counts deliveries and settles nothing itself.
type countingSubscriber struct {
	bus.BaseSubscriber
	ch chan struct{}
}

func newCountingSubscriber() *countingSubscriber {
	return &countingSubscriber{ch: make(chan struct{}, 1024)}
}

func (s *countingSubscriber) OnEvent(context.Context, string, any, map[string]string) error {
	s.ch <- struct{}{}
	return nil
}

func (s *countingSubscriber) count() int { return len(s.ch) }

// settlerHolder keeps the settler off the delivery context so a test can settle
// deliberately late, as a configuration under `ack = "manual"` does.
// The mutex is not about OnEvent, which hands the settler over a channel.
// settler() is called from inside an Eventually condition, which testify runs
// on its own goroutine, and again from the test goroutine afterwards — so the
// memo it writes is shared between the two.
type settlerHolder struct {
	bus.BaseSubscriber
	got chan bus.Settler
	mu  sync.Mutex
	s   bus.Settler
}

func newSettlerHolder() *settlerHolder {
	return &settlerHolder{got: make(chan bus.Settler, 1)}
}

func (h *settlerHolder) OnEvent(ctx context.Context, _ string, _ any, _ map[string]string) error {
	select {
	case h.got <- bus.SettlerFromContext(ctx):
	default:
	}
	return nil
}

func (h *settlerHolder) settler() bus.Settler {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.s != nil {
		return h.s
	}
	select {
	case s := <-h.got:
		h.s = s
		return s
	default:
		return nil
	}
}
