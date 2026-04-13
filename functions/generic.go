package functions

import (
	"context"
	"fmt"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/ctyutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
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
	enc, err := ctyutil.GetCapsuleFromValue(val)
	if err != nil {
		return nil, fmt.Errorf("argument is not callable: %w", err)
	}
	c, ok := enc.(types.Callable)
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
		"decrement": makeDecrementFunction(),
		"observe":   makeObserveFunction(),
		"tostring":  makeToStringFunction(),
		"length":    makeLengthFunction(),
		"state":     makeStateFunction(),
		"clear":     makeClearFunction(),
		"reset":     makeResetFunction(),
		"count":     makeCountFunction(),
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

// makeToStringFunction returns an enhanced tostring() that supports Stringable
// capsules (and objects with _capsule), falling back to stdlib conversion.
func makeToStringFunction() function.Function {
	fallback := stdlib.MakeToFunc(cty.String)
	return function.New(&function.Spec{
		Description: "Converts a value to string; supports Stringable capsules and objects with _capsule",
		Params:      []function.Parameter{{Name: "v", Type: cty.DynamicPseudoType}},
		Type:        function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			enc, err := ctyutil.GetCapsuleFromValue(args[0])
			if err == nil {
				if s, ok := enc.(types.Stringable); ok {
					str, err := s.ToString(context.Background())
					if err != nil {
						return cty.NilVal, fmt.Errorf("tostring: %w", err)
					}
					return cty.StringVal(str), nil
				}
			}
			return fallback.Call(args)
		},
	})
}

// makeLengthFunction returns an enhanced length() that supports Lengthable
// capsules (and objects with _capsule), falling back to stdlib length.
func makeLengthFunction() function.Function {
	fallback := stdlib.LengthFunc
	return function.New(&function.Spec{
		Description: "Returns the length of a value; supports Lengthable capsules and objects with _capsule",
		Params:      []function.Parameter{{Name: "v", Type: cty.DynamicPseudoType}},
		Type:        function.StaticReturnType(cty.Number),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			enc, err := ctyutil.GetCapsuleFromValue(args[0])
			if err == nil {
				if l, ok := enc.(types.Lengthable); ok {
					n, err := l.Length(context.Background())
					if err != nil {
						return cty.NilVal, fmt.Errorf("length: %w", err)
					}
					return cty.NumberIntVal(n), nil
				}
			}
			return fallback.Call(args)
		},
	})
}

func extractGettable(val cty.Value) (types.Gettable, error) {
	enc, err := ctyutil.GetCapsuleFromValue(val)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	g, ok := enc.(types.Gettable)
	if !ok {
		return nil, fmt.Errorf("get: %s does not support get()", val.Type().FriendlyName())
	}
	return g, nil
}

func extractSettable(val cty.Value) (types.Settable, error) {
	enc, err := ctyutil.GetCapsuleFromValue(val)
	if err != nil {
		return nil, fmt.Errorf("set: %w", err)
	}
	s, ok := enc.(types.Settable)
	if !ok {
		return nil, fmt.Errorf("set: %s does not support set()", val.Type().FriendlyName())
	}
	return s, nil
}

func extractIncrementable(val cty.Value) (types.Incrementable, error) {
	enc, err := ctyutil.GetCapsuleFromValue(val)
	if err != nil {
		return nil, fmt.Errorf("increment: %w", err)
	}
	i, ok := enc.(types.Incrementable)
	if !ok {
		return nil, fmt.Errorf("increment: %s does not support increment()", val.Type().FriendlyName())
	}
	return i, nil
}

// makeDecrementFunction returns a cty function for decrement([ctx,] thing [, delta]).
// Delta defaults to 1. Implemented as increment(thing, -delta).
func makeDecrementFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, rest := contextAndThing(args)
			i, err := extractIncrementable(thing)
			if err != nil {
				return cty.NilVal, fmt.Errorf("decrement: %w", err)
			}
			delta := cty.NumberIntVal(1)
			if len(rest) > 0 {
				delta = rest[0]
				rest = rest[1:]
			}
			neg, err := stdlib.NegateFunc.Call([]cty.Value{delta})
			if err != nil {
				return cty.NilVal, fmt.Errorf("decrement: %w", err)
			}
			return i.Increment(ctx, append([]cty.Value{neg}, rest...))
		},
	})
}

// makeStateFunction returns a cty function for state([ctx,] thing) → string.
func makeStateFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, _ := contextAndThing(args)
			enc, err := ctyutil.GetCapsuleFromValue(thing)
			if err != nil {
				return cty.NilVal, fmt.Errorf("state: %w", err)
			}
			s, ok := enc.(types.Stateful)
			if !ok {
				return cty.NilVal, fmt.Errorf("state: %s does not support state()", thing.Type().FriendlyName())
			}
			str, err := s.State(ctx)
			if err != nil {
				return cty.NilVal, fmt.Errorf("state: %w", err)
			}
			return cty.StringVal(str), nil
		},
	})
}

// makeClearFunction returns a cty function for clear([ctx,] thing).
func makeClearFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, _ := contextAndThing(args)
			enc, err := ctyutil.GetCapsuleFromValue(thing)
			if err != nil {
				return cty.NilVal, fmt.Errorf("clear: %w", err)
			}
			c, ok := enc.(types.Clearable)
			if !ok {
				return cty.NilVal, fmt.Errorf("clear: %s does not support clear()", thing.Type().FriendlyName())
			}
			if err := c.Clear(ctx); err != nil {
				return cty.NilVal, fmt.Errorf("clear: %w", err)
			}
			return cty.NullVal(cty.DynamicPseudoType), nil
		},
	})
}

// makeResetFunction returns a cty function for reset([ctx,] thing).
func makeResetFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, _ := contextAndThing(args)
			enc, err := ctyutil.GetCapsuleFromValue(thing)
			if err != nil {
				return cty.NilVal, fmt.Errorf("reset: %w", err)
			}
			r, ok := enc.(types.Resettable)
			if !ok {
				return cty.NilVal, fmt.Errorf("reset: %s does not support reset()", thing.Type().FriendlyName())
			}
			if err := r.Reset(ctx); err != nil {
				return cty.NilVal, fmt.Errorf("reset: %w", err)
			}
			return cty.NullVal(cty.DynamicPseudoType), nil
		},
	})
}

// makeCountFunction returns a cty function for count([ctx,] thing) → number.
func makeCountFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.Number),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, thing, _ := contextAndThing(args)
			enc, err := ctyutil.GetCapsuleFromValue(thing)
			if err != nil {
				return cty.NilVal, fmt.Errorf("count: %w", err)
			}
			c, ok := enc.(types.Countable)
			if !ok {
				return cty.NilVal, fmt.Errorf("count: %s does not support count()", thing.Type().FriendlyName())
			}
			n, err := c.Count(ctx)
			if err != nil {
				return cty.NilVal, fmt.Errorf("count: %w", err)
			}
			return cty.NumberIntVal(n), nil
		},
	})
}

func extractObservable(val cty.Value) (types.Observable, error) {
	enc, err := ctyutil.GetCapsuleFromValue(val)
	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}
	o, ok := enc.(types.Observable)
	if !ok {
		return nil, fmt.Errorf("observe: %s does not support observe()", val.Type().FriendlyName())
	}
	return o, nil
}
