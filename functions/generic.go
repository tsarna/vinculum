package functions

import (
	"context"
	"fmt"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/ctyutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("generic", func(_ *cfg.Config) map[string]function.Function {
		return GetGenericFunctions()
	})
}

// contextAndThing extracts an optional leading context and the "thing" value from
// args. If args[0] is a context capsule/object it is used as the context and
// args[1] is the thing; otherwise context.Background() is used and args[0] is
// the thing. Returns (ctx, thing, remaining args after thing).
func contextAndThing(args []cty.Value) (context.Context, cty.Value, []cty.Value) {
	ctx, err := ctyutil.GetContextFromValue(args[0])
	if err == nil {
		if len(args) < 2 {
			return ctx, cty.NilVal, nil
		}
		return ctx, args[1], args[2:]
	}
	return context.Background(), args[0], args[1:]
}

// makeCallFunction returns a cty function that invokes call() on any Callable thing.
// Signature: call(ctx, thing, args...) -> response
func makeCallFunction() function.Function {
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
			ctx, err := ctyutil.GetContextFromValue(args[0])
			if err != nil {
				return cty.NilVal, fmt.Errorf("call: context error: %w", err)
			}

			callable, err := extractCallable(args[1])
			if err != nil {
				return cty.NilVal, fmt.Errorf("call: %w", err)
			}

			return callable.Call(ctx, args[2:])
		},
	})
}

func extractCallable(val cty.Value) (types.Callable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("argument is not callable (got %s)", val.Type().FriendlyName())
	}
	c, ok := val.EncapsulatedValue().(types.Callable)
	if !ok {
		return nil, fmt.Errorf("%s does not support call()", val.Type().FriendlyName())
	}
	return c, nil
}

func GetGenericFunctions() map[string]function.Function {
	return map[string]function.Function{
		"call":      makeCallFunction(),
		"get":       makeGetFunction(),
		"set":       makeSetFunction(),
		"increment": makeIncrementFunction(),
		"observe":   makeObserveFunction(),
	}
}

// makeGetFunction returns a cty function for get([ctx,] thing [, default, ...]).
func makeGetFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{
			Name: "args",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, rest := contextAndThing(args)
			g, err := extractGettable(thing)
			if err != nil {
				return cty.NilVal, err
			}
			return g.Get(ctx, rest)
		},
	})
}

// makeSetFunction returns a cty function for set([ctx,] thing [, value, ...]).
func makeSetFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, rest := contextAndThing(args)
			s, err := extractSettable(thing)
			if err != nil {
				return cty.NilVal, err
			}
			return s.Set(ctx, rest)
		},
	})
}

// makeIncrementFunction returns a cty function for increment([ctx,] thing, delta [, ...]).
func makeIncrementFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, rest := contextAndThing(args)
			if len(rest) == 0 {
				return cty.NilVal, fmt.Errorf("increment: missing delta argument")
			}
			i, err := extractIncrementable(thing)
			if err != nil {
				return cty.NilVal, err
			}
			return i.Increment(ctx, rest)
		},
	})
}

// makeObserveFunction returns a cty function for observe([ctx,] thing, value [, ...]).
func makeObserveFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, rest := contextAndThing(args)
			if len(rest) == 0 {
				return cty.NilVal, fmt.Errorf("observe: missing value argument")
			}
			o, err := extractObservable(thing)
			if err != nil {
				return cty.NilVal, err
			}
			return o.Observe(ctx, rest)
		},
	})
}

func extractGettable(val cty.Value) (types.Gettable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("get: argument is not gettable (got %s)", val.Type().FriendlyName())
	}
	g, ok := val.EncapsulatedValue().(types.Gettable)
	if !ok {
		return nil, fmt.Errorf("get: %s does not support get()", val.Type().FriendlyName())
	}
	return g, nil
}

func extractSettable(val cty.Value) (types.Settable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("set: argument is not settable (got %s)", val.Type().FriendlyName())
	}
	s, ok := val.EncapsulatedValue().(types.Settable)
	if !ok {
		return nil, fmt.Errorf("set: %s does not support set()", val.Type().FriendlyName())
	}
	return s, nil
}

func extractIncrementable(val cty.Value) (types.Incrementable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("increment: argument is not incrementable (got %s)", val.Type().FriendlyName())
	}
	i, ok := val.EncapsulatedValue().(types.Incrementable)
	if !ok {
		return nil, fmt.Errorf("increment: %s does not support increment()", val.Type().FriendlyName())
	}
	return i, nil
}

func extractObservable(val cty.Value) (types.Observable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("observe: argument is not observable (got %s)", val.Type().FriendlyName())
	}
	o, ok := val.EncapsulatedValue().(types.Observable)
	if !ok {
		return nil, fmt.Errorf("observe: %s does not support observe()", val.Type().FriendlyName())
	}
	return o, nil
}
