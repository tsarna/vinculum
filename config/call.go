package config

import (
	"context"
	"fmt"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Callable is a pure request/response capability. It is not specific to LLM
// clients and is designed to cover future client types (HTTP, JSON-RPC, etc.).
type Callable interface {
	Call(ctx context.Context, request cty.Value) (cty.Value, error)
}

// CallableClient is a registered Client that also supports synchronous call().
type CallableClient interface {
	Client
	Callable
}

// CallFunction returns a cty function that invokes call() on a Callable client.
// Signature: call(ctx, client, request) -> response
func CallFunction(config *Config) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "ctx",
				Type: cty.DynamicPseudoType,
			},
			{
				Name: "client",
				Type: cty.DynamicPseudoType,
			},
			{
				Name: "request",
				Type: cty.DynamicPseudoType,
			},
		},
		Type: function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, diags := GetContextFromObject(args[0])
			if diags.HasErrors() {
				return cty.NilVal, fmt.Errorf("call: context error: %s", diags.Error())
			}

			client, err := GetClientFromCapsule(args[1])
			if err != nil {
				return cty.NilVal, fmt.Errorf("call: %w", err)
			}

			callable, ok := client.(Callable)
			if !ok {
				return cty.NilVal, fmt.Errorf("call: client %q does not support call()", client.GetName())
			}

			return callable.Call(ctx, args[2])
		},
	})
}
