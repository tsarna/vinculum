package functions

import (
	"github.com/tsarna/functy"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty/function"
)

// functy's standard library provides the host-agnostic lazy builtins (typeof,
// typekind, cond, switch, error, assert) plus the opt-in try/can. These are
// functy's canonical definitions; Vinculum adopts them rather than shipping its
// own overlapping copies (the former `misc` group's typeof/error/cond/switch/try
// were removed in favor of these). StdlibExtras() is opted into here — Vinculum
// does not otherwise expose HCL's stock tryfunc `try`/`can`, so there is nothing
// to silently override.
//
// The rich-object-aware tostring/length dispatch stays in the `generic` group:
// functy deliberately leaves capsule dispatch to rich-cty-types.
func init() {
	cfg.RegisterFunctionPlugin("functy_stdlib", func(_ *cfg.Config) map[string]function.Function {
		fns := make(map[string]function.Function)
		for name, fn := range functy.Stdlib() {
			fns[name] = fn
		}
		for name, fn := range functy.StdlibExtras() {
			fns[name] = fn
		}
		return fns
	})
}
