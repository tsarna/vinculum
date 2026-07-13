package config

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty/function"
)

// The reflection built-ins, help() and doc().
//
// They are registered as a function plugin rather than merged into the eval context
// after the fact, for two reasons: the plugin getter runs inside GetFunctions, by
// which point both of their dependencies exist (the eval context, and the parsed
// functy Result); and going through the plugin registry means they inherit its
// reserved-name check, so a user `function "help"` block is rejected outright rather
// than silently displacing the built-in.
//
// help() is the reason externs exist. A cty function can only make its *trailing*
// parameters optional, so the rich-cty-types get/set/count family — which takes an
// optional *leading* context, sniffed from the first argument — has to fake it with
// a variadic, and reflects from cty metadata as the useless `get(thing, ...args)`.
// The extern declarations those packages register (RegisterFunctyExterns) carry the
// real signatures, and help() prefers them over the cty fallback.
func init() {
	RegisterFunctionPlugin("reflect", func(c *Config) map[string]function.Function {
		evalCtxFn := func() *hcl.EvalContext { return c.evalCtx }

		// The Result carries both the user's own .cty declarations and the externs
		// the host registered; nil before Build, which HelpFunc tolerates.
		var result *functy.Result
		if c.functyState != nil {
			result = c.functyState.result
		}

		return map[string]function.Function{
			"help": functy.HelpFunc(result, evalCtxFn),
			"doc":  functy.DocFunc(evalCtxFn),
		}
	})
}
