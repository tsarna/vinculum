// Package main is the integration-test fixture plugin. It is built with
// -buildmode=plugin from inside the integration test's TestMain. It is
// kept under testdata/ so the standard `go build ./...` skips it.
package main

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// VinculumPluginInit is the required plugin entry point. It registers a
// single function the test then exercises through a .vcl expression.
func VinculumPluginInit(ctx *config.PluginContext) hcl.Diagnostics {
	config.RegisterFunctionPlugin("vinculum_plugin_integration_test", func(_ *config.Config) map[string]function.Function {
		return map[string]function.Function{
			"vinculum_plugin_integration_test_hello": function.New(&function.Spec{
				Params: []function.Parameter{},
				Type:   function.StaticReturnType(cty.String),
				Impl: func(_ []cty.Value, _ cty.Type) (cty.Value, error) {
					return cty.StringVal("hello from plugin"), nil
				},
			}),
		}
	})
	return nil
}
