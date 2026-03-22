package config

import "github.com/hashicorp/hcl/v2"

type BlockHandler interface {
	Preprocess(block *hcl.Block) hcl.Diagnostics
	FinishPreprocessing(config *Config) hcl.Diagnostics
	GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics)
	GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics)
	Process(config *Config, block *hcl.Block) hcl.Diagnostics
	FinishProcessing(config *Config) hcl.Diagnostics
}

type BlockHandlerBase struct {
}

func (b *BlockHandlerBase) Preprocess(block *hcl.Block) hcl.Diagnostics {
	return nil
}

func (b *BlockHandlerBase) FinishPreprocessing(config *Config) hcl.Diagnostics {
	return nil
}

func (b *BlockHandlerBase) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "", nil
}

func (b *BlockHandlerBase) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	return nil, nil
}

func (b *BlockHandlerBase) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	return nil
}

func (b *BlockHandlerBase) FinishProcessing(config *Config) hcl.Diagnostics {
	return nil
}

func GetBlockHandlers() map[string]BlockHandler {
	return map[string]BlockHandler{
		"assert":       NewAssertBlockHandler(),
		"bus":          NewBusBlockHandler(),
		"client":       NewClientBlockHandler(),
		"const":        NewConstBlockHandler(),
		"cron":         NewCronBlockHandler(),
		"server":       NewServerBlockHandler(),
		"signals":      NewSignalsBlockHandler(),
		"subscription": NewSubscriptionBlockHandler(),
		"var":          NewVariableBlockHandler(),
	}
}

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
		Type:       "client",
		LabelNames: []string{"type", "name"},
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
	{
		Type:       "var",
		LabelNames: []string{"name"},
	},
}

var configSchema = &hcl.BodySchema{
	Blocks: blockSchema,
}
