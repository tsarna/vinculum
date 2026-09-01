package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// receiverChain composes the wrappers a receiver builds around its target, in
// the order a receiver builds them: the baggage filter outermost, then the
// settle deadline, then the async queue, then transforms, then the leaf.
func receiverChain(t *testing.T, policy AckPolicy, queued bool) bus.Subscriber {
	t.Helper()

	var inner bus.Subscriber = &recordingSubscriber{}
	inner = subutils.NewTransformingSubscriber(inner)
	if queued {
		queue := subutils.NewAsyncQueueingSubscriber(inner, 4).Start()
		t.Cleanup(func() { queue.Close() })
		inner = queue
	}
	inner = NewSettleTimeoutSubscriber(policy, inner, "receiver", zap.NewNop())
	return NewBaggageFilterSubscriber(nil, inner, zap.NewNop())
}

// The silent failure, guarded on this side of the line.
//
// Every wrapper between a receiver and its leaf has to report what it wraps, or
// the receiver reads a chain ending in a queue as ordinary and acknowledges
// every message at the moment it is enqueued. Nothing errors and nothing logs;
// the only symptom is messages lost on a path that promised at-least-once. The
// bus tests cover its own wrappers — this covers the two that live here, over
// the chain a receiver actually builds.
func TestTheComposedReceiverChainReportsWhatIsBehindIt(t *testing.T) {
	manual := AckPolicy{Mode: AckManual, SettleTimeout: time.Second}
	auto := AckPolicy{Mode: AckAuto}

	assert.Equal(t, bus.Deferred, bus.DispositionOf(receiverChain(t, manual, true)),
		"baggage filter -> settle deadline -> queue: the queue at the end is what decides")

	assert.Equal(t, bus.Deferred, bus.DispositionOf(receiverChain(t, auto, true)),
		"and the deadline being absent under auto must not change the answer")

	assert.Equal(t, bus.Handled, bus.DispositionOf(receiverChain(t, manual, false)),
		"the same chain with no queue is synchronous to the leaf")

	assert.Equal(t, bus.Handled, bus.DispositionOf(receiverChain(t, auto, false)))
}

// The same question with a real bus at the end of the chain, which is what
// `subscriber = bus.<name>` actually produces and what every receiver in a
// configuration is really handed.
//
// This is not the same test as the one above with a different leaf. A bus does
// not reach a configuration as a bus: it reaches it as a *BusHandle, which
// embeds the bus.EventBus interface — and an embedded interface promotes only
// the methods that interface declares. DeliveryDisposition is not one of them,
// so the handle answered for itself and reported that its return meant the work
// was done. A queue in front of a bus then acknowledged every message at the
// moment it was enqueued.
//
// Nothing failed to compile and no wrapper was missing its Unwrap; the chain
// above reports Deferred correctly and told us nothing. Only a leaf that is the
// real thing catches this.
func TestARealBusAtTheEndOfTheChainStillDefers(t *testing.T) {
	config, diags := NewConfig().
		WithSources([]byte(`bus "work" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	busHandle := config.Buses["work"]
	require.NotNil(t, busHandle)

	assert.Equal(t, bus.Deferred, bus.DispositionOf(busHandle),
		"a bus accepts a message onto its own queue and returns; the subscribers "+
			"that will handle it have not run")

	// And through the wrappers a receiver builds around it, which is the shape
	// that was actually broken: the queue asks what it wraps, and what it wraps
	// is the handle.
	queued := subutils.NewAsyncQueueingSubscriber(busHandle, 4).Start()
	t.Cleanup(func() { queued.Close() })
	chain := NewBaggageFilterSubscriber(nil,
		NewSettleTimeoutSubscriber(AckPolicy{Mode: AckAuto}, queued, "receiver", zap.NewNop()),
		zap.NewNop())

	assert.Equal(t, bus.Deferred, bus.DispositionOf(chain))
	assert.Equal(t, bus.Deferred, bus.DispositionOf(queued.Unwrap()),
		"and the queue's own settle point asks the handle directly, which is "+
			"where the acknowledgement was being made too early")
}

// send() derives a new message rather than handing the current one on, so the
// inbound delivery's settler stays with the original. Carrying it would make an
// author's fan-out a race for a single settle, and would give two spellings of
// one topology different guarantees.
//
// This is deliberately the opposite of the rule origin takes through the same
// call site, so it is worth a test that fails loudly if someone unifies them.
func TestSendDoesNotCarryTheSettlerToTheDerivedMessage(t *testing.T) {
	config, diags := NewConfig().WithLogger(zap.NewNop()).WithSources([]byte(`bus "main" {}`)).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	target := &recordingSubscriber{}
	ctx := bus.WithSettler(context.Background(), bus.NewSettler(noopSettleOps{}, bus.AutoSettle()))
	require.NotNil(t, bus.SettlerFromContext(ctx), "the delivery starts out settleable")

	ctxValue, err := richcty.NewContextObject(ctx).Build()
	require.NoError(t, err)

	send := SendFunction(config)
	_, err = send.Call([]cty.Value{
		ctxValue,
		NewSubscriberCapsule(target),
		cty.StringVal("derived/topic"),
		cty.StringVal("payload"),
	})
	require.NoError(t, err)

	require.Equal(t, 1, target.onEventCalls)
	assert.Nil(t, bus.SettlerFromContext(target.gotCtx),
		"work on a message send() derived must not be able to settle the delivery that caused it")
}

type noopSettleOps struct{}

func (noopSettleOps) Ack(context.Context) error               { return nil }
func (noopSettleOps) Nack(context.Context, string) error      { return nil }
func (noopSettleOps) Keepalive(context.Context) (bool, error) { return false, nil }
func (noopSettleOps) Valid() (bool, string)                   { return true, "" }
