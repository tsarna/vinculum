package types

import (
	"context"

	"github.com/zclconf/go-cty/cty"
)

// Callable is a pure request/response capability. It is not specific to LLM
// clients and is designed to cover future types (HTTP, JSON-RPC, MCP, etc.).
// args contains all arguments after the "thing": the implementor fully controls
// what they mean.
type Callable interface {
	Call(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Gettable is implemented by types whose current value can be read via get().
// args contains all arguments after the "thing": typically args[0] is the
// default value, but implementors may interpret additional args freely.
type Gettable interface {
	Get(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Settable is implemented by types whose value can be updated via set().
// args contains all arguments after the "thing": args[0] is the value to set;
// implementors may interpret additional args (e.g. labels) freely.
type Settable interface {
	Set(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Incrementable is implemented by types that support numeric increment via increment().
// args contains all arguments after the "thing": args[0] is the delta;
// implementors may interpret additional args freely.
type Incrementable interface {
	Increment(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Observable is implemented by types that support observe() (histograms).
// args contains all arguments after the "thing": args[0] is the observed value;
// implementors may interpret additional args (e.g. labels) freely.
type Observable interface {
	Observe(ctx context.Context, args []cty.Value) (cty.Value, error)
}
