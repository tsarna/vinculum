package cmd

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"go.uber.org/zap"
)

// recorder appends to a shared log as each teardown phase reaches it, so a test
// can assert the order the phases actually ran in.
type recorder struct {
	name string
	log  *[]string
}

func (r *recorder) Drain(context.Context) error {
	*r.log = append(*r.log, "drain:"+r.name)
	return nil
}

func (r *recorder) PreStop() error {
	*r.log = append(*r.log, "prestop:"+r.name)
	return nil
}

func (r *recorder) Stop() error {
	*r.log = append(*r.log, "stop:"+r.name)
	return nil
}

// inFlight returns a holder that reports itself empty and records the phase
// reaching it, so an ordering assertion can see the quiesce phase alongside
// the other three.
func (r *recorder) inFlight() config.InFlightHolder {
	return config.InFlightHolder{
		Name:       r.name,
		QueueDepth: func() int { return 0 },
		Close: func() error {
			*r.log = append(*r.log, "quiesce:"+r.name)
			return nil
		},
	}
}

// The ordering is the whole point of the drain phase: every listener must stop
// accepting before any client, bus, or subscription is torn down. Reversing
// these two lets a request that arrives during shutdown run its handler against
// a closed SQL pool or a stopped bus.
//
// Quiescing sits between the last two for two reasons that pull in opposite
// directions. It goes after PreStop because a `trigger "shutdown"` publishes
// into the pipeline, and running it afterwards is what gets that message
// delivered rather than left on a channel. It goes before Stop because the
// acknowledgement for a message the pipeline is still carrying travels over the
// receiver's connection, which Stop closes.
func TestShutdownDrainsBeforeStopping(t *testing.T) {
	var log []string
	a := &recorder{name: "a", log: &log}
	b := &recorder{name: "b", log: &log}

	cfg := &config.Config{
		Drainables:    []config.Drainable{a, b},
		InFlight:      []config.InFlightHolder{a.inFlight(), b.inFlight()},
		PreStoppables: []config.PreStoppable{a, b},
		Stoppables:    []config.Stoppable{a, b},
	}

	shutdown(cfg, zap.NewNop())

	require.Equal(t, []string{
		"drain:b", "drain:a",
		"prestop:b", "prestop:a",
		"quiesce:b", "quiesce:a",
		"stop:b", "stop:a",
	}, log)
}

// Reverse registration order is what makes a mounted server drain after the
// server "http" block hosting it: the mounted block is processed first, so it
// registers first and drains last.
func TestDrainRunsInReverseRegistrationOrder(t *testing.T) {
	var log []string
	mounted := &recorder{name: "mounted", log: &log}
	host := &recorder{name: "host", log: &log}

	cfg := &config.Config{Drainables: []config.Drainable{mounted, host}}
	drain(cfg, zap.NewNop())

	assert.Equal(t, []string{"drain:host", "drain:mounted"}, log)
}

// A Drainable that fails must not abort the phase — the remaining listeners
// still need closing, and the phases after this one still need to run.
func TestDrainContinuesPastAFailure(t *testing.T) {
	var log []string
	ok := &recorder{name: "ok", log: &log}

	cfg := &config.Config{
		Drainables: []config.Drainable{ok, failingDrainable{}},
		Stoppables: []config.Stoppable{ok},
	}

	shutdown(cfg, zap.NewNop())

	assert.Equal(t, []string{"drain:ok", "stop:ok"}, log,
		"a failed drain should not stop the teardown sequence")
}

type failingDrainable struct{}

func (failingDrainable) Drain(context.Context) error { return context.DeadlineExceeded }

// gate is a subscriber that holds every delivery until the test releases it,
// then counts it. Nothing here sleeps: the queue is provably still holding
// messages when the phase starts, so the assertion cannot pass by accident.
// started, when supplied, closes as the first delivery arrives — the moment the
// queue is empty and the work is not finished.
type gate struct {
	bus.BaseSubscriber
	open    chan struct{}
	started chan struct{}
	first   sync.Once
	handled atomic.Int64
}

func (g *gate) OnEvent(context.Context, string, any, map[string]string) error {
	if g.started != nil {
		g.first.Do(func() { close(g.started) })
	}
	<-g.open
	g.handled.Add(1)
	return nil
}

// The defect this phase exists to remove, at the level of the real types: a
// queue holding work when the process exits runs none of it, and on a path
// where nothing acknowledged there is no broker to redeliver.
func TestQuiesceRunsTheBacklogAQueueIsHolding(t *testing.T) {
	g := &gate{open: make(chan struct{})}
	queue := subutils.NewAsyncQueueingSubscriber(g, 100).Start()

	const messages = 20
	for i := 0; i < messages; i++ {
		require.NoError(t, queue.OnEvent(context.Background(), "work", i, nil))
	}
	require.Positive(t, queue.QueueDepth(), "the queue should still be holding the backlog")

	cfg := &config.Config{InFlight: []config.InFlightHolder{{
		Name: "subscription/slow", QueueDepth: queue.QueueDepth, Close: queue.Close,
	}}}

	// Released once the phase is under way, so the wait is what the messages
	// are waiting on rather than the other way round.
	go func() {
		time.Sleep(quiesceInterval)
		close(g.open)
	}()
	quiesce(cfg, zap.NewNop(), 5*time.Second)

	assert.Equal(t, int64(messages), g.handled.Load(),
		"every queued message should have run before shutdown continued")
}

// A bus is the holder that only waiting can empty: its Stop abandons what is in
// the channel rather than dispatching it, so there is nothing to close and the
// wait is the whole mechanism. This is also the shape a queue cannot stand in
// for — a subscriber with no queue of its own runs on the dispatch goroutine,
// so the messages behind it are the bus's own backlog.
func TestQuiesceRunsTheBacklogABusIsHolding(t *testing.T) {
	g := &gate{open: make(chan struct{})}

	events, err := bus.NewEventBus().WithLogger(zap.NewNop()).WithBufferSize(100).Build()
	require.NoError(t, err)
	require.NoError(t, events.Start())
	t.Cleanup(func() { events.Stop() }) //nolint:errcheck
	require.NoError(t, events.Subscribe(context.Background(), "work", g))

	const messages = 20
	for i := 0; i < messages; i++ {
		require.NoError(t, events.Publish(context.Background(), "work", i))
	}

	cfg := &config.Config{InFlight: []config.InFlightHolder{{
		Name: "bus.events", QueueDepth: events.QueueDepth,
	}}}

	go func() {
		time.Sleep(quiesceInterval)
		close(g.open)
	}()
	quiesce(cfg, zap.NewNop(), 5*time.Second)

	assert.Equal(t, int64(messages), g.handled.Load(),
		"every published message should have been dispatched before shutdown continued")
}

// Closing is what makes the last message exact. A depth of zero means the queue
// handed the message on, not that the work finished — and the work is what an
// acknowledgement waits for.
func TestQuiesceWaitsForWorkTheQueueHasAlreadyDequeued(t *testing.T) {
	g := &gate{open: make(chan struct{}), started: make(chan struct{})}
	queue := subutils.NewAsyncQueueingSubscriber(g, 10).Start()
	require.NoError(t, queue.OnEvent(context.Background(), "work", nil, nil))

	<-g.started // the queue is empty now, and the work is not finished
	require.Zero(t, queue.QueueDepth())

	go func() {
		time.Sleep(2 * quiesceInterval)
		close(g.open)
	}()
	quiesce(&config.Config{InFlight: []config.InFlightHolder{{
		Name: "subscription/slow", QueueDepth: queue.QueueDepth, Close: queue.Close,
	}}}, zap.NewNop(), 5*time.Second)

	assert.Equal(t, int64(1), g.handled.Load(),
		"shutdown continued while the action was still running")
}

// A pipeline that never empties must not hang the process. The bound is the
// phase's, so a holder that reports a depth forever costs one budget and not
// one per holder.
func TestQuiesceGivesUpAtItsDeadline(t *testing.T) {
	stuck := config.InFlightHolder{Name: "bus.jammed", QueueDepth: func() int { return 7 }}

	start := time.Now()
	quiesce(&config.Config{InFlight: []config.InFlightHolder{stuck, stuck}}, zap.NewNop(),
		4*quiesceInterval)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, time.Second, "quiesce did not respect its budget")
	assert.GreaterOrEqual(t, elapsed, 4*quiesceInterval, "quiesce gave up before its budget")
}

// An action that never returns must not be able to hang shutdown either. Close
// waits for the queue's goroutine, and that goroutine is running user code.
func TestQuiesceGivesUpOnAQueueThatWillNotClose(t *testing.T) {
	g := &gate{open: make(chan struct{})} // never released
	queue := subutils.NewAsyncQueueingSubscriber(g, 10).Start()
	require.NoError(t, queue.OnEvent(context.Background(), "work", nil, nil))

	start := time.Now()
	quiesce(&config.Config{InFlight: []config.InFlightHolder{{
		Name: "subscription/stuck", QueueDepth: queue.QueueDepth, Close: queue.Close,
	}}}, zap.NewNop(), 4*quiesceInterval)

	assert.Less(t, time.Since(start), time.Second, "a hung action hung the shutdown")
}

// End to end, in the language a user writes: a subscription with a queue, a
// backlog on it, and a shutdown that runs the backlog instead of exiting past
// it. Publishing is orders of magnitude cheaper than evaluating an action, so
// the queue is deep when shutdown begins — which is what makes the count exact
// rather than lucky.
func TestShutdownRunsAQueuedSubscriptionsBacklog(t *testing.T) {
	cfg, diags := config.NewConfig().
		WithSources([]byte(`
bus "main" {}

var "handled" { value = 0 }

subscription "counter" {
    target     = bus.main
    topics     = ["work"]
    queue_size = 1000
    action     = increment(ctx, var.handled)
}
`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	const messages = 200
	for i := 0; i < messages; i++ {
		require.NoError(t, cfg.Buses["main"].Publish(context.Background(), "work", i))
	}

	shutdown(cfg, zap.NewNop())

	handled, err := types.GetVariableFromCapsule(cfg.CtyVarMap["handled"])
	require.NoError(t, err)
	val, err := handled.Get(context.Background(), nil)
	require.NoError(t, err)

	count, _ := val.AsBigFloat().Int64()
	assert.Equal(t, int64(messages), count,
		"shutdown exited before the subscription's queue had run its backlog")
}

// The phase can only wait for what registered, so registration is the part that
// silently rots. Both holders a configuration produces are named here: the bus,
// whose channel holds a published message until the dispatch loop reaches it,
// and the queue, which holds it again before the action runs.
func TestAQueuedSubscriptionRegistersBothHolders(t *testing.T) {
	cfg, diags := config.NewConfig().
		WithSources([]byte(`
bus "events" {}

subscription "audit" {
    target     = bus.events
    topics     = ["#"]
    queue_size = 10
    action     = ctx.topic
}
`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	names := make([]string, 0, len(cfg.InFlight))
	for _, holder := range cfg.InFlight {
		names = append(names, holder.Name)
		assert.NotNil(t, holder.QueueDepth, "%s registered no depth to wait on", holder.Name)
	}
	assert.Equal(t, []string{"bus.events", "subscription/audit"}, names)

	// Only the queue has something to close: a bus's Stop abandons what is in
	// its channel rather than dispatching it, so waiting is the only way to
	// empty one.
	assert.Nil(t, cfg.InFlight[0].Close)
	assert.NotNil(t, cfg.InFlight[1].Close)
}

// A subscription without a queue introduces no holder of its own — the bus is
// the only place its messages wait, and the action runs on the dispatch loop.
func TestASubscriptionWithoutAQueueRegistersOnlyItsBus(t *testing.T) {
	cfg, diags := config.NewConfig().
		WithSources([]byte(`
bus "events" {}

subscription "audit" {
    target = bus.events
    topics = ["#"]
    action = ctx.topic
}
`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	require.Len(t, cfg.InFlight, 1)
	assert.Equal(t, "bus.events", cfg.InFlight[0].Name)
}
