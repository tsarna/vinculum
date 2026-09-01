package rabbitmq

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// recordingOps counts what would have reached the broker.
type recordingOps struct {
	mu    sync.Mutex
	acks  int
	nacks int
}

func (o *recordingOps) Ack(context.Context) error { o.mu.Lock(); o.acks++; o.mu.Unlock(); return nil }

func (o *recordingOps) Nack(context.Context, string) error {
	o.mu.Lock()
	o.nacks++
	o.mu.Unlock()
	return nil
}

func (o *recordingOps) Keepalive(context.Context) (bool, error) { return false, nil }
func (o *recordingOps) Valid() (bool, string)                   { return true, "" }

func (o *recordingOps) counts() (acks, nacks int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.acks, o.nacks
}

// gatedSub holds the bus's delivery goroutine until released.
type gatedSub struct {
	bus.BaseSubscriber
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (g *gatedSub) Release() { g.once.Do(func() { close(g.release) }) }

func (g *gatedSub) OnEvent(context.Context, string, any, map[string]string) error {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	return g.err
}

// The composed chain a `client "rabbitmq"` receiver hands its deliveries to,
// driven the way the receiver drives it — with a settler on the context — but
// without a broker.
//
// The library's own tests prove the receiver settles on what its subscriber did.
// The config package's tests prove the wrappers report what they wrap. This is
// the join: the chain this client actually builds, ending in the bus handle a
// configuration really names, with a queue in the middle.
//
// It matters most here of the four. An unsettled delivery holds one of
// `prefetch` slots and nothing on AMQP self-heals, so a message acknowledged
// early and then lost had no second chance at all.
func TestTheReceiverChainDefersUntilTheWorkIsDone(t *testing.T) {
	src := `
bus "work" {}

client "rabbitmq" "mq" {
    brokers = ["amqp://localhost:5672/"]

    receiver "in" {
        queue      = "q"
        queue_size = 16
        subscriber = bus.work
    }
}
`
	config, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	client := config.Clients["rabbitmq"]["mq"].(*RMQClientWrapper)
	require.Len(t, client.receiverSpecs, 1)
	target := client.receiverSpecs[0].subscriber

	// What the receiver asks before it settles anything. Getting this wrong is
	// silent, and is exactly how a bus handle once made a queue look ordinary.
	require.Equal(t, bus.Deferred, bus.DispositionOf(target),
		"a queue in front of a bus is not a subscriber whose return means the work is done")

	gate := &gatedSub{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer gate.Release()
	require.NoError(t, config.Buses["work"].Subscribe(context.Background(), "q", gate))

	ops := &recordingOps{}
	ctx := bus.WithSettler(context.Background(), bus.NewSettler(ops, bus.AutoSettle()))

	require.NoError(t, target.OnEvent(ctx, "q", "payload", nil))

	// The receiver would settle here, on that nil return. It must not.
	bus.SettleOnReturn(ctx, target, nil)

	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the subscriber never ran")
	}
	acks, nacks := ops.counts()
	assert.Equal(t, 0, acks, "the subscriber is still working; nothing has been handled yet")
	assert.Equal(t, 0, nacks)

	gate.Release()
	assert.Eventually(t, func() bool { acks, _ := ops.counts(); return acks == 1 },
		3*time.Second, 20*time.Millisecond,
		"the acknowledgement should follow the work through the queue and the bus")
}

// And the half that makes it at-least-once: work that fails behind the queue
// nacks, so the broker redelivers or dead-letters it.
func TestTheReceiverChainNacksWorkThatFails(t *testing.T) {
	src := fmt.Sprintf(`
bus "work" {}

client "rabbitmq" "mq" {
    brokers = ["amqp://localhost:5672/"]

    receiver "in" {
        queue      = "q"
        queue_size = %d
        subscriber = bus.work
    }
}
`, 16)
	config, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	client := config.Clients["rabbitmq"]["mq"].(*RMQClientWrapper)
	target := client.receiverSpecs[0].subscriber

	gate := &gatedSub{entered: make(chan struct{}, 1), release: make(chan struct{}), err: assert.AnError}
	defer gate.Release()
	require.NoError(t, config.Buses["work"].Subscribe(context.Background(), "q", gate))

	ops := &recordingOps{}
	ctx := bus.WithSettler(context.Background(), bus.NewSettler(ops, bus.AutoSettle()))

	require.NoError(t, target.OnEvent(ctx, "q", "payload", nil))
	bus.SettleOnReturn(ctx, target, nil)

	<-gate.entered
	gate.Release()

	assert.Eventually(t, func() bool { _, nacks := ops.counts(); return nacks == 1 },
		3*time.Second, 20*time.Millisecond,
		"a failed handler must return the delivery to the broker")

	acks, _ := ops.counts()
	assert.Equal(t, 0, acks, "and must never have acknowledged it")
}
