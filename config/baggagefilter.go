package config

import (
	"context"

	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/hclutil"
	"go.uber.org/zap"
)

// The baggage block's schema is registered here rather than beside its struct
// in hclutil, because hclutil is a dependency of this package and cannot import
// it back.
func init() {
	RegisterSharedBlockSchema(&hclutil.BaggageFilterConfig{}, baggageSchema)
}

var baggageSchema = TypeSchema{
	Summary: "Which inbound baggage keys to trust.",
	Doc: `Inbound baggage is untrusted input, so the default — no block, or an empty
one — strips it entirely. Trace propagation is unaffected, and baggage this
configuration sets itself via ` + "`set(ctx.baggage, …)`" + ` always propagates outbound
regardless of this policy.

	baggage { passthrough = true }      # trust all inbound baggage
	baggage { allow = ["tenant_id"] }   # trust only these keys
	baggage { deny  = ["internal."] }   # trust everything but these key prefixes`,
	Attrs: map[string]AttrMeta{
		"passthrough": {
			Summary: "Trust all inbound baggage.",
			Hint:    HintBool,
		},
		"allow": {
			Summary: "Keys to keep; everything else is dropped.",
		},
		"deny": {
			Summary: "Key prefixes to drop; everything else is kept.",
		},
		"max_entries": {
			Summary: "Cap on the number of baggage entries.",
			Doc:     "Applied within `allow`/`deny`; `passthrough` skips it. Surplus entries are dropped with a debug log.",
			Default: "64",
		},
		"max_bytes": {
			Summary: "Cap on the total serialized size, in bytes.",
			Doc:     "Applied within `allow`/`deny`; `passthrough` skips it. Surplus entries are dropped with a debug log.",
			Default: "8192",
		},
	},
	Constraints: []Constraint{
		MutuallyExclusive("passthrough", "allow", "deny"),
	},
}

// baggageFilterSubscriber wraps a bus.Subscriber, applying a baggage trust
// filter to each event's context before delivery. It is installed by
// message-consuming clients (Kafka, MQTT, RabbitMQ, SQS, …) at the external
// inbound boundary so untrusted inbound baggage is stripped by default, exactly
// as the baggage{} block does for server "http"/"mcp". It must NOT be used on
// internal bus-to-bus subscriptions, whose baggage is already trusted.
type baggageFilterSubscriber struct {
	inner  bus.Subscriber
	filter *hclutil.BaggageFilterConfig
	logger *zap.Logger
}

// NewBaggageFilterSubscriber wraps inner so that each delivered event's context
// has its baggage filtered per filter. A nil filter applies the secure default
// (strip all inbound baggage), so this should be installed unconditionally on
// external inbound consumers.
func NewBaggageFilterSubscriber(filter *hclutil.BaggageFilterConfig, inner bus.Subscriber, logger *zap.Logger) bus.Subscriber {
	return &baggageFilterSubscriber{inner: inner, filter: filter, logger: logger}
}

func (s *baggageFilterSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	return s.inner.OnEvent(s.filter.FilterContext(ctx, s.logger), topic, message, fields)
}

func (s *baggageFilterSubscriber) OnSubscribe(ctx context.Context, topic string) error {
	return s.inner.OnSubscribe(ctx, topic)
}

func (s *baggageFilterSubscriber) OnUnsubscribe(ctx context.Context, topic string) error {
	return s.inner.OnUnsubscribe(ctx, topic)
}

func (s *baggageFilterSubscriber) PassThrough(msg bus.EventBusMessage) error {
	return s.inner.PassThrough(msg)
}

// Unwrap reports what this wraps, so a settle point asking what a delivery's
// return will mean sees past the filter to what is actually handling the
// message. This is the outermost wrapper on every receiver, so forgetting it
// would hide every other wrapper's answer too — silently, since a chain that
// reports itself ordinary produces no error and no log line.
func (s *baggageFilterSubscriber) Unwrap() bus.Subscriber { return s.inner }

// baggageFilterEventBus is the same trust boundary for a surface that *publishes*
// rather than one that is delivered to.
//
// A client receiver hands each message to a subscriber, so filtering it means
// wrapping the subscriber. A `server "vws"` connection does the opposite: it
// derives a context from the frame's own headers and calls Publish on the bus.
// There is no subscriber in the middle to wrap — the untrusted context is
// created inside the connection and handed straight to the bus — so the filter
// has to sit on the bus handle the server was given.
//
// It filters Publish and PublishSync and nothing else. Subscribe carries the
// connection's own context rather than a frame's, and the promoted OnEvent is
// the bus-to-bus path, whose baggage is already trusted.
type baggageFilterEventBus struct {
	bus.EventBus
	filter *hclutil.BaggageFilterConfig
	logger *zap.Logger
}

// NewBaggageFilterEventBus wraps inner so that the context of each published
// message has its baggage filtered per filter. A nil filter applies the secure
// default (strip all inbound baggage), so this should be installed
// unconditionally on a surface that publishes on behalf of an untrusted peer.
func NewBaggageFilterEventBus(filter *hclutil.BaggageFilterConfig, inner bus.EventBus, logger *zap.Logger) bus.EventBus {
	return &baggageFilterEventBus{EventBus: inner, filter: filter, logger: logger}
}

func (b *baggageFilterEventBus) Publish(ctx context.Context, topic string, payload any) error {
	return b.EventBus.Publish(b.filter.FilterContext(ctx, b.logger), topic, payload)
}

func (b *baggageFilterEventBus) PublishSync(ctx context.Context, topic string, payload any) error {
	return b.EventBus.PublishSync(b.filter.FilterContext(ctx, b.logger), topic, payload)
}

// Unwrap answers for the bus behind the filter. Embedding the bus.EventBus
// interface promotes OnEvent without promoting DeliveryDisposition, so without
// this the wrapper would report itself an ordinary synchronous subscriber and
// hide whatever the real bus is — which is the defect TestForwardingSubscribers-
// ReportWhatTheyForwardTo exists to prevent, and the reason it flags an embedded
// interface rather than waiting for a hand-written OnEvent.
func (b *baggageFilterEventBus) Unwrap() bus.Subscriber { return b.EventBus }
