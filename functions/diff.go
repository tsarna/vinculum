package functions

import (
	"fmt"

	"github.com/tsarna/go-structdiff"
	"github.com/tsarna/go2cty2go"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

var DiffFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "a", Type: cty.DynamicPseudoType},
		{Name: "b", Type: cty.DynamicPseudoType},
	},
	Type: function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		a, err := go2cty2go.CtyToAny(args[0])
		if err != nil {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("unable to convert first argument: %s", err)
		}
		b, err := go2cty2go.CtyToAny(args[1])
		if err != nil {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("unable to convert second argument: %s", err)
		}

		diff, err := structdiff.Diff(a, b)
		if err != nil {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("unable to diff values: %s", err)
		}

		return go2cty2go.AnyToCty(diff)
	},
})

var PatchFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "target", Type: cty.DynamicPseudoType},
		{Name: "patch", Type: cty.DynamicPseudoType},
	},
	Type: function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		target, err := go2cty2go.CtyToAny(args[0])
		if err != nil {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("unable to convert target argument: %s", err)
		}
		patch, err := go2cty2go.CtyToAny(args[1])
		if err != nil {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("unable to convert patch argument: %s", err)
		}

		patchMap, ok := patch.(map[string]any)
		if !ok {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("patch must be a map")
		}

		targetMap, ok := target.(map[string]any)
		if !ok {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("target must be a map")
		}

		err = structdiff.Apply(&targetMap, patchMap)
		if err != nil {
			return cty.UnknownVal(cty.NilType), fmt.Errorf("unable to apply patch: %s", err)
		}

		return go2cty2go.AnyToCty(targetMap)
	},
})
