package config

import (
	"context"
	"fmt"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Callable is a pure request/response capability. It is not specific to LLM
// clients and is designed to cover future types (HTTP, JSON-RPC, MCP, etc.).
// args contains all arguments after the "thing": the implementor fully controls
// what they mean.
type Callable interface {
	Call(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// CallFunction returns a cty function that invokes call() on any Callable thing.
// Signature: call(ctx, thing, args...) -> response
func CallFunction(config *Config) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "ctx",
				Type: cty.DynamicPseudoType,
			},
			{
				Name: "thing",
				Type: cty.DynamicPseudoType,
			},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, diags := GetContextFromObject(args[0])
			if diags.HasErrors() {
				return cty.NilVal, fmt.Errorf("call: context error: %s", diags.Error())
			}

			callable, err := extractCallable(args[1])
			if err != nil {
				return cty.NilVal, fmt.Errorf("call: %w", err)
			}

			return callable.Call(ctx, args[2:])
		},
	})
}

// extractCallable type-asserts any capsule's encapsulated value to Callable.
// Since all capsule types store interface{}, this works generically for any
// capsule whose underlying value implements Callable.
func extractCallable(val cty.Value) (Callable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("argument is not callable (got %s)", val.Type().FriendlyName())
	}
	c, ok := val.EncapsulatedValue().(Callable)
	if !ok {
		return nil, fmt.Errorf("%s does not support call()", val.Type().FriendlyName())
	}
	return c, nil
}
