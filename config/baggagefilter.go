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
