// Package main is a self-contained fixture plugin used by the release
// smoke gate (.github/workflows/docker.yml). It is built in the freshly
// published vinculum-build image and loaded by the freshly published
// runtime image to prove the container plugin workflow actually works for
// the release before it is considered good.
//
// It is intentionally tiny: it registers one function the smoke config
// asserts on. It is NOT built by `go build ./...` (it is a separate module
// under testdata/).
package main

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func VinculumPluginInit(_ *config.PluginContext) hcl.Diagnostics {
	config.RegisterFunctionPlugin("plugin_smoke", func(_ *config.Config) map[string]function.Function {
		return map[string]function.Function{
			"plugin_smoke_ok": function.New(&function.Spec{
				Params: []function.Parameter{},
				Type:   function.StaticReturnType(cty.Bool),
				Impl: func(_ []cty.Value, _ cty.Type) (cty.Value, error) {
					return cty.True, nil
				},
			}),
		}
	})
	return nil
}
