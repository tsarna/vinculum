package http

// Test-only stub client type used to exercise duplicate-name coexistence
// across client types: two clients sharing a name (one of type "http",
// one of type "httpfake") must be allowed when at most one is enabled.
// This file registers a minimal "httpfake" client type that does just
// enough to participate in the dependency-resolution and registration
// paths without pulling in a real second client implementation.
//
// "httpfake" implements config.Client with no behavior — it is never
// invoked at runtime; the tests only verify that the config layer
// accepts the configuration and routes registration correctly.

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
)

func init() {
	cfg.RegisterClientType("httpfake", processFake)
}

type fakeClientDefinition struct {
	Label    string    `hcl:"label,optional"`
	DefRange hcl.Range `hcl:",def_range"`
}

type fakeClient struct {
	cfg.BaseClient
	label string
}

func processFake(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := fakeClientDefinition{}
	diags := gohcl.DecodeBody(body, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	return &fakeClient{
		BaseClient: cfg.BaseClient{
			Name:     block.Labels[1],
			DefRange: def.DefRange,
		},
		label: def.Label,
	}, nil
}
