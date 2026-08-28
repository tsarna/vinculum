package config

import (
	"context"
	"fmt"

	"github.com/tsarna/go2cty2go"
	"github.com/tsarna/vinculum/hclutil"
)

// The `ctx` a receiver's `vinculum_topic` expression is evaluated against.
//
// It is deliberately not the `message` shape. A `message` ctx carries a bus
// topic; here there is no bus topic yet — producing one is the expression's
// whole purpose — so the only identifier in scope is the transport's own, and
// it is named after the transport. That is the same rule `on_decode_error`
// follows, and the two hooks on one receiver now agree about what a value is
// called: an mqtt receiver reads `ctx.mqtt_topic` in both, a redis stream one
// reads `ctx.stream` and `ctx.entry_id` in both.
//
// Only `msg` and `fields` are fixed, because only those are true of every
// transport. The identity fields differ — a routing key is not a partition key
// is not a queue name — so each receiver names its own with
// AttrMeta.ContextFields, exactly as it already does for the decode hook.
func init() {
	RegisterContextSchema("inbound-message", ContextSchema{
		Summary: "Evaluated once per arriving message, to derive a bus topic for it.",
		Doc: `There is no bus topic in scope: computing one is what the expression is for.
What the message arrived on is named after the transport that delivered it —
` + "`mqtt_topic`" + `, ` + "`routing_key`" + `, ` + "`channel`" + ` — and is listed with that
receiver's own ` + "`vinculum_topic`" + ` alongside the two fields below, which every
receiver carries.`,
		OpenFields: true,
		Fields: []ContextField{
			{
				Name: "msg", Type: CtxTypeDynamic,
				Summary: "The message payload.",
				Doc: "Already decoded by the client's `wire_format`, so its type follows the " +
					"data rather than the transport — except on `client \"sqs_receiver\"`, " +
					"which picks a topic before decoding and so passes the raw body here.",
			},
			{
				Name: "fields", Type: CtxTypeObject,
				Summary: "String metadata attached to the message.",
				Doc: "Always present; an empty object when the message carries no metadata. " +
					"What lands here is the transport's own metadata plus whatever the " +
					"subscription's pattern captured.",
			},
		},
	})
}

// NewInboundContext seeds the eval context for a receiver's `vinculum_topic`
// expression with the two fields every receiver has — the payload and its
// metadata — against the `inbound-message` shape registered above.
//
// The caller adds its transport's identity and builds:
//
//	b, err := cfg.NewInboundContext(msg, fields)
//	if err != nil {
//	    return "", fmt.Errorf("mqtt receiver: %w", err)
//	}
//	evalCtx, err := b.WithStringAttribute("mqtt_topic", mqttTopic).
//	    BuildEvalContext(config.EvalCtx())
//
// A []byte payload is converted to a string first: an expression interpolating
// ctx.msg wants text, and a wire format that does not decode hands back the
// raw bytes.
func NewInboundContext(msg any, fields map[string]string) (*hclutil.EvalContextBuilder, error) {
	if b, ok := msg.([]byte); ok {
		msg = string(b)
	}
	ctyMsg, err := go2cty2go.AnyToCty(msg)
	if err != nil {
		return nil, fmt.Errorf("convert msg: %w", err)
	}

	return hclutil.NewEvalContext(context.Background()).
		WithAttribute("msg", ctyMsg).
		WithStringMapAttribute("fields", fields), nil
}
