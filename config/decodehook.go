package config

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	bytescty "github.com/tsarna/bytes-cty-type"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/tsarna/vinculum/hclutil"
	"go.uber.org/zap"
)

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
		// client can't accidentally shadow them.
		for k, v := range e.Attrs {
			switch k {
			case "raw", "error", "wire_format", "topic", "fields":
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
