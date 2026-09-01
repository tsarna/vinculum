package config

import (
	"context"
	"sync"
	"time"

	bus "github.com/tsarna/vinculum-bus"
	"go.uber.org/zap"
)

// The bound on an unsettled message.
//
// Under ack = "manual" the receiver settles nothing when delivery returns —
// that is the entire point, and settling there is exactly the coupling manual
// settle exists to remove. So an unsettled message is bounded by its lease, and
// the leases are not comparable: SQS's visibility window lapses and the message
// reappears, a Redis entry waits in the pending list until reclaim_min_idle
// lets someone claim it, a RabbitMQ delivery is held indefinitely while
// occupying one of ten prefetch slots, and a Kafka record pins its partition's
// committable offset. Two of those are self-healing, slowly; two are not.
//
// settle_timeout is the uniform bound over all four, and the only thing that
// makes "forgot to call inbound::ack()" diagnosable rather than a slow stall.
// It is a configuration policy rather than a protocol fact, which is why it
// lives here and not in the receiver libraries.

// settleTimeoutSubscriber bounds how long a delivery may go unsettled. It sits
// between the receiver's baggage filter and the async queue, so the clock
// starts when the message arrives rather than when a worker gets to it — with
// queue_size set, those can be a long way apart, and it is precisely the gap
// this is here to survive.
type settleTimeoutSubscriber struct {
	inner    bus.Subscriber
	timeout  time.Duration
	receiver string
	logger   *zap.Logger
}

// NewSettleTimeoutSubscriber wraps inner so that a delivery nobody settles
// within policy.SettleTimeout is nacked and logged against receiver.
//
// It wraps whenever a bound was asked for, whatever the mode. Under manual that
// is always — a settle deadline is required there, because nothing settles the
// message until the configuration does. Under auto it is optional and usually
// absent: the framework settles at a known point, so a configuration that asks
// for no bound is no worse off than it was. What makes the bound meaningful
// under auto at all is that the settle now happens wherever the work finishes,
// which may be several hops from here and may never happen.
func NewSettleTimeoutSubscriber(policy AckPolicy, inner bus.Subscriber, receiver string, logger *zap.Logger) bus.Subscriber {
	if policy.SettleTimeout <= 0 {
		return inner
	}
	return &settleTimeoutSubscriber{
		inner:    inner,
		timeout:  policy.SettleTimeout,
		receiver: receiver,
		logger:   logger,
	}
}

func (s *settleTimeoutSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	settler := bus.SettlerFromContext(ctx)
	if settler == nil {
		// Nothing to bound: this message did not arrive over a transport that
		// acknowledges. That is not a misconfiguration on its own — a receiver
		// may deliver both — so it passes through.
		return s.inner.OnEvent(ctx, topic, message, fields)
	}

	deadline := &deadlineSettler{inner: settler}
	// Armed before delivery, not after. Under queue_size, OnEvent returns at
	// the moment the message is queued, so arming afterwards would start the
	// clock at the wrong end of the hop; and a synchronous action that runs
	// past the bound is the case the bound is about.
	deadline.timer = time.AfterFunc(s.timeout, func() {
		s.expire(ctx, deadline, topic)
	})

	return s.inner.OnEvent(bus.WithSettler(ctx, deadline), topic, message, fields)
}

// expire nacks a delivery nobody settled in time.
func (s *settleTimeoutSubscriber) expire(ctx context.Context, deadline *deadlineSettler, topic string) {
	// The delivery's own context may be cancelled by now — it belongs to a
	// receive that has moved on — and the nack still has to reach the broker.
	settled, err := deadline.inner.Nack(context.WithoutCancel(ctx), "settle_timeout expired")
	if !settled && err == nil {
		// Something settled it between the timer firing and this line. Nothing
		// went wrong, and saying so would be a false alarm.
		return
	}

	// Logged against the configuration rather than the runtime: a message that
	// went unsettled is an expression that did not call inbound::ack(), and the
	// Go stack for the timer says nothing about which one.
	fieldsToLog := []zap.Field{
		zap.String("receiver", s.receiver),
		zap.String("topic", topic),
		zap.Duration("settle_timeout", s.timeout),
	}
	if err != nil {
		s.logger.Error("nothing settled this message within settle_timeout, and nacking it failed",
			append(fieldsToLog, zap.Error(err))...)
		return
	}
	s.logger.Warn("nothing settled this message within settle_timeout; it was nacked",
		fieldsToLog...)
}

func (s *settleTimeoutSubscriber) OnSubscribe(ctx context.Context, topic string) error {
	return s.inner.OnSubscribe(ctx, topic)
}

func (s *settleTimeoutSubscriber) OnUnsubscribe(ctx context.Context, topic string) error {
	return s.inner.OnUnsubscribe(ctx, topic)
}

func (s *settleTimeoutSubscriber) PassThrough(msg bus.EventBusMessage) error {
	return s.inner.PassThrough(msg)
}

// Unwrap reports what this wraps, so a settle point asking what a delivery's
// return will mean sees past the deadline to the queue or subscriber behind it.
//
// Forgetting this is silent and expensive: the receiver would read a chain
// ending in an async queue as ordinary, and acknowledge every message at the
// moment it was enqueued — which is the defect the whole arrangement exists to
// remove, reintroduced by the wrapper that bounds it.
func (s *settleTimeoutSubscriber) Unwrap() bus.Subscriber { return s.inner }

// deadlineSettler is the settler a bounded delivery carries: the receiver's
// own, plus the timer that will nack it if nobody does.
//
// It observes rather than decides. Settle-once, staleness, and every protocol
// verb belong to the settler underneath; all this adds is stopping the clock
// once the delivery is settled, so the timeout is a bound on unsettled messages
// rather than a nack that arrives after a successful ack.
type deadlineSettler struct {
	inner bus.Settler
	timer *time.Timer

	mu      sync.Mutex
	stopped bool
}

func (d *deadlineSettler) Ack(ctx context.Context) (bool, error) {
	settled, err := d.inner.Ack(ctx)
	d.settled(settled)
	return settled, err
}

func (d *deadlineSettler) Nack(ctx context.Context, reason string) (bool, error) {
	settled, err := d.inner.Nack(ctx, reason)
	d.settled(settled)
	return settled, err
}

// Auto forwards, because whether a delivery is settled by the framework or by
// the configuration is the receiver's decision, made when it built the settler
// underneath. Wrapping that delivery in a deadline observes it; it does not
// change whose decision it was.
func (d *deadlineSettler) Auto() bool {
	return d.inner.Auto()
}

// Keepalive does not stop the clock, and that is the whole difference between
// it and a settle. A handler that needs longer than settle_timeout says so by
// extending the lease it actually holds, which ties the extension to observable
// progress rather than to the process merely still being alive.
func (d *deadlineSettler) Keepalive(ctx context.Context) (bool, error) {
	return d.inner.Keepalive(ctx)
}

// settled stops the timer once, if this call was the one that settled the
// delivery. A call that failed at the broker has settled nothing, so the bound
// still applies to it.
func (d *deadlineSettler) settled(ok bool) {
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
	d.timer.Stop()
}
