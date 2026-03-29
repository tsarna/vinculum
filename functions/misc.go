package functions

import (
	"errors"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("misc", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"typeof": TypeOfFunc,
			"error":  ErrorFunc,
		}
	})
}

// TypeOfFunc returns the friendly name of the type of a given value
var TypeOfFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "value", Type: cty.DynamicPseudoType},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return cty.StringVal(args[0].Type().FriendlyName()), nil
	},
})

var ErrorFunc = function.New(&function.Spec{
	Description: "Returns an error with the given message",
	Params: []function.Parameter{
		{Name: "message", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return args[0], errors.New(args[0].AsString())
	},
})
