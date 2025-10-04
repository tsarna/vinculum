package config

import (
	"github.com/hashicorp/hcl/v2"
)

var blockSchema = []hcl.BlockHeaderSchema{
	{
		Type:       "assert",
		LabelNames: []string{"name"},
	},
	{
		Type:       "bus",
		LabelNames: []string{"name"},
	},
	{
		Type:       "const",
		LabelNames: []string{},
	},
	{
		Type:       "cron",
		LabelNames: []string{"name"},
	},
	{
		Type:       "function",
		LabelNames: []string{"name"},
	},
	{
		Type:       "jq",
		LabelNames: []string{"name"},
	},
	{
		Type:       "server",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "signals",
		LabelNames: []string{},
	},
	{
		Type:       "subscription",
		LabelNames: []string{"name"},
	},
}

var configSchema = &hcl.BodySchema{
	Blocks: blockSchema,
}
