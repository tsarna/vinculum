package config

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	bytescty "github.com/tsarna/bytes-cty-type"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/tsarna/vinculum/hclutil"
	"go.uber.org/zap"
)

func init() {
	// MakeDecodeErrorHook below builds this shape.
	RegisterContextSchema("decode-error", ContextSchema{
		Summary: "Evaluated when an inbound message cannot be decoded.",
		Doc: `The message is dropped whatever this expression does, and whether it
succeeds — the hook is an observer, not a recovery path.

The fields below are carried by every receiver. Each also adds its own
transport identity — ` + "`routing_key`" + ` on rabbitmq, ` + "`stream`" + ` and ` + "`entry_id`" + ` on a
redis stream — listed with that receiver's own ` + "`on_decode_error`" + `.`,
		OpenFields: true,
		Fields: []ContextField{
			{Name: "raw", Type: CtxTypeObject, Summary: "The undecoded body, as a bytes object."},
			{Name: "error", Type: attrTypeString, Summary: "The deserialize error message."},
			{Name: "wire_format", Type: attrTypeString, Summary: "Name of the configured wire format that rejected it."},
			{Name: "topic", Type: attrTypeString, Summary: "Best-effort vinculum topic for the message."},
			{Name: "fields", Type: CtxTypeObject, Summary: "Metadata extracted before the failure."},
		},
	})

	// Every client with a connection lifecycle evaluates on_connect and
	// on_disconnect with a bare context: there is no message in flight, so
	// nothing but the universal fields is in scope.
	RegisterContextSchema("connection", ContextSchema{
		Summary: "Evaluated on a connection lifecycle change.",
		Doc: `No message is in flight, so no message fields are in scope — only the
universal ones below.`,
	})
}

// tolerantWireFormats are the built-in formats whose Deserialize never
// fails, so a malformed body can't poison a receiver that uses them.
var tolerantWireFormats = map[string]bool{
	"auto":       true,
	"auto_bytes": true,
	"string":     true,
	"bytes":      true,
}

// IsStrictWireFormat reports whether a decode failure is possible for the
// named wire format — that is, whether the format is anything other than
// one of the always-succeeds built-ins.
//
// It is a heuristic used only for config-time warnings. Custom formats
// registered via a `wire_format` block or a plugin are treated as strict,
// which is the safe direction: it warns rather than staying silent.
func IsStrictWireFormat(name string) bool {
	return !tolerantWireFormats[name]
}

// MakeDecodeErrorHook builds the runtime hook for a receiver's
// on_decode_error expression, or returns nil when the attribute is absent.
// A nil return tells the receiver there is no observer to invoke.
//
// The hook is an observer only. Whatever it does — including failing to
// evaluate — the receiver still treats the message as failed, so every
// error here is logged to UserLogger and swallowed. label identifies the
// receiver in those log messages (e.g. `rabbitmq receiver "in"`).
//
// The eval context follows the usual convention that ctx is the only
// top-level variable:
//
//	ctx.raw          the undecoded body, as a bytes object
//	ctx.error        the deserialize error message
//	ctx.wire_format  the configured format name
//	ctx.topic        best-effort vinculum topic
//	ctx.fields       fields extracted before the failure
//	ctx.<attr>       per-client identity fields (routing_key, offset, ...)
func MakeDecodeErrorHook(config *Config, expr hcl.Expression, label string) wire.DecodeErrorHook {
	if !IsExpressionProvided(expr) {
		return nil
	}

	return func(ctx context.Context, e wire.DecodeError) {
		// A hook is never allowed to take down the receiver, so contain
		// panics from user expressions the same way eval errors are.
		defer func() {
			if r := recover(); r != nil {
				config.UserLogger.Error(label+": on_decode_error panicked",
					zap.Any("panic", r))
			}
		}()

		errMsg := ""
		if e.Err != nil {
			errMsg = e.Err.Error()
		}

		builder := hclutil.NewEvalContext(ctx).
			WithAttribute("raw", bytescty.BuildBytesObject(e.Raw, "application/octet-stream")).
			WithStringAttribute("error", errMsg).
			WithStringAttribute("wire_format", e.Format).
			WithStringAttribute("topic", e.Topic).
			WithStringMapAttribute("fields", e.Fields)

		// Per-client identity fields. Set after the fixed attributes so a
		// client can't accidentally shadow them. wire.IsReservedAttr is the
		// one definition of that set, shared with the receivers that choose
		// these keys.
		//
		// A dropped key is a bug in the receiver, not in the user's config —
		// nothing they can write makes it appear or go away — so it is logged
		// operationally rather than to UserLogger.
		for k, v := range e.Attrs {
			if wire.IsReservedAttr(k) {
				config.Logger.Warn(label+": decode error attribute dropped; it collides with a fixed hook field",
					zap.String("attribute", k))
				continue
			}
			builder = builder.WithStringAttribute(k, v)
		}

		evalCtx, err := builder.BuildEvalContext(config.EvalCtx())
		if err != nil {
			config.UserLogger.Error(label+": on_decode_error build eval context",
				zap.Error(err))
			return
		}

		if _, diags := expr.Value(evalCtx); diags.HasErrors() {
			config.UserLogger.Error(label+": on_decode_error eval failed",
				config.ActionError(diags))
		}
	}
}
