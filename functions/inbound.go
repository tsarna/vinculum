package functions

import (
	"context"
	_ "embed"
	"fmt"

	richcty "github.com/tsarna/rich-cty-types"
	bus "github.com/tsarna/vinculum-bus"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

// inboundExterns declares the real signature of inbound::nack, whose trailing
// `reason` is optional. cty can only approximate an optional trailing parameter
// with a variadic, which erases the name, the type, and the default, and
// reflects as `*reason` — reading as "any number of reasons" when at most one
// is accepted.
//
//go:embed externs/inbound.cty
var inboundExterns []byte

func init() {
	cfg.RegisterFunctionPlugin("inbound", func(c *cfg.Config) map[string]function.Function {
		return GetInboundFunctions(c)
	})
	cfg.RegisterFunctyExterns("vinculum/inbound.cty", inboundExterns)
}

// GetInboundFunctions returns the inbound:: settle functions.
//
// Acknowledgement is a property of the inbound delivery, not of the payload and
// not of the subscriber that happens to handle it, so these take nothing but a
// context: no handle, no capsule naming the client, no protocol in the name.
// Each is a no-op returning false when the message did not arrive over a
// transport that acknowledges, which is what lets shared subscription code call
// them without knowing what is underneath.
//
// The namespace is inbound:: rather than a bare ack()/nack()/keepalive(),
// matching vocabulary the codebase already commits to — the `inbound-message`
// ctx shape, "strip untrusted inbound baggage" — and reading correctly from a
// subscription three buses downstream, where it usefully names what is being
// settled. It is a place rather than an operation, so the next per-message
// transport fact has somewhere to go.
func GetInboundFunctions(config *cfg.Config) map[string]function.Function {
	return map[string]function.Function{
		"inbound::ack":       inboundAckFunc(config),
		"inbound::nack":      inboundNackFunc(config),
		"inbound::keepalive": inboundKeepaliveFunc(config),
	}
}

// inboundCtxParam is the leading context every inbound:: function requires. It
// is the whole argument list: the settler rides on it, so naming a client or
// passing a token would be describing something the context already knows.
//
// AllowDynamicType keeps the static cty.Bool return visible in reflected
// metadata — a dynamic argument without it poisons the return type, as the
// comment on createSendFunction explains.
var inboundCtxParam = function.Parameter{
	Name:             "ctx",
	Type:             cty.DynamicPseudoType,
	AllowDynamicType: true,
	Description:      "The handler context, which carries the delivery being settled",
}

// nackReasonVarParam is the optional trailing reason. cty can only make a
// trailing parameter optional by making it variadic, so arity is checked in the
// implementation and the real signature is declared in externs/inbound.cty.
//
// It is a string rather than an `any`, because every destination it can reach
// is a header, a log field, or a stream field, and metadata in this system is
// map[string]string end to end. Accepting a value would force an encoding
// decision into a function whose job is not encoding, and would collide with
// the receiver's own wire_format, which is where payload encoding is decided.
var nackReasonVarParam = function.Parameter{
	Name:        "reason",
	Type:        cty.String,
	Description: "Why the message was not handled; advisory, and where it goes depends on the receiver",
}

func inboundAckFunc(config *cfg.Config) function.Function {
	return function.New(&function.Spec{
		Description: "Settles the inbound delivery as handled, acknowledging it with whichever broker it came from; returns whether this call was the one that settled it",
		Params:      []function.Parameter{inboundCtxParam},
		Type:        function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			settler, ctx, err := settlerArg(args[0], "inbound::ack")
			if err != nil {
				return cty.False, err
			}
			if settler == nil {
				return cty.False, nil
			}
			settled, err := settler.Ack(ctx)
			return settleResult(config, "inbound::ack", settled, err)
		},
	})
}

func inboundNackFunc(config *cfg.Config) function.Function {
	return function.New(&function.Spec{
		Description: "Settles the inbound delivery as not handled, leaving the broker to requeue or dead-letter it per the receiver's own policy; returns whether this call was the one that settled it",
		Params:      []function.Parameter{inboundCtxParam},
		VarParam:    &nackReasonVarParam,
		Type:        function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			settler, ctx, err := settlerArg(args[0], "inbound::nack")
			if err != nil {
				return cty.False, err
			}
			reason, err := nackReasonArg(args[1:])
			if err != nil {
				return cty.False, err
			}
			if settler == nil {
				return cty.False, nil
			}
			settled, err := settler.Nack(ctx, reason)
			return settleResult(config, "inbound::nack", settled, err)
		},
	})
}

func inboundKeepaliveFunc(config *cfg.Config) function.Function {
	return function.New(&function.Spec{
		Description: "Extends the inbound delivery's lease, for handling that will take longer than the broker allows by default; returns whether a lease was extended",
		Params:      []function.Parameter{inboundCtxParam},
		Type:        function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			settler, ctx, err := settlerArg(args[0], "inbound::keepalive")
			if err != nil {
				return cty.False, err
			}
			if settler == nil {
				return cty.False, nil
			}
			extended, err := settler.Keepalive(ctx)
			return settleResult(config, "inbound::keepalive", extended, err)
		},
	})
}

// settlerArg unwraps the ctx argument into its Go context and the settler
// riding on it. A nil settler is the ordinary case for a message that did not
// arrive over an acknowledging transport, and is not an error.
func settlerArg(val cty.Value, name string) (bus.Settler, context.Context, error) {
	ctx, err := richcty.GetContextFromValue(val)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: invalid ctx: %w", name, err)
	}
	return bus.SettlerFromContext(ctx), ctx, nil
}

// settleResult turns one settle attempt into what the config sees.
//
// The two failure modes are answered differently on purpose. A stale token —
// a visibility window that lapsed, an entry another consumer reclaimed — means
// handling took longer than the configuration allows for, which is a fact about
// the configuration rather than an error in it: the call reports that it did not
// settle the delivery, and the reason is logged where a VCL author will look for
// it. Anything else is a genuine failure to reach the broker, and returning it
// fails the action, because a message that was not acknowledged and whose
// configuration thinks it was is exactly the silence this whole design exists to
// remove.
func settleResult(config *cfg.Config, name string, settled bool, err error) (cty.Value, error) {
	if bus.IsStale(err) {
		config.UserLogger.Warn(name+": the delivery could no longer be settled",
			zap.Error(err))
		return cty.False, nil
	}
	if err != nil {
		return cty.False, fmt.Errorf("%s: %w", name, err)
	}
	return cty.BoolVal(settled), nil
}

// nackReasonArg reads the optional trailing reason.
func nackReasonArg(rest []cty.Value) (string, error) {
	switch len(rest) {
	case 0:
		return "", nil
	case 1:
		if rest[0].IsNull() {
			return "", nil
		}
		return rest[0].AsString(), nil
	default:
		return "", fmt.Errorf("inbound::nack: expected at most one reason argument, got %d", len(rest))
	}
}
