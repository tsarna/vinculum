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
// function the test exercises through a .vcl expression, and a client type
// the test then looks for in `vinculum schema --plugin-path` output.
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

	config.RegisterClientType("plugin_sample", processSampleClient,
		config.WithSchema(sampleClientSchema))

	return nil
}

type sampleClientBody struct {
	Greeting string `hcl:"greeting"`
	Loud     *bool  `hcl:"loud,optional"`
}

// sampleClientSchema is the point of the fixture: a plugin describes its own
// block exactly as in-tree types do, and `vinculum schema --plugin-path`
// should reproduce it.
var sampleClientSchema = config.TypeSchema{
	Sample:  &sampleClientBody{},
	Summary: "Integration-test fixture client contributed by a plugin.",
	Attrs: map[string]config.AttrMeta{
		"greeting": {Summary: "What to say."},
		"loud":     {Summary: "Say it in capitals.", Hint: config.HintBool},
	},
}

type sampleClient struct {
	config.BaseClient
}

func processSampleClient(cfg *config.Config, block *hcl.Block, body hcl.Body) (config.Client, hcl.Diagnostics) {
	def := sampleClientBody{}
	if diags := config.DecodeBody(body, cfg.EvalCtx(), &def); diags.HasErrors() {
		return nil, diags
	}
	return &sampleClient{
		BaseClient: config.BaseClient{Name: block.Labels[1], DefRange: block.DefRange},
	}, nil
}
