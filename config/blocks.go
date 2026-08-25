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
	return ExtractBlockDependencies(block), nil
}

func (b *BlockHandlerBase) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	return nil
}

func (b *BlockHandlerBase) FinishProcessing(config *Config) hcl.Diagnostics {
	return nil
}

// findAcrossTypes locates name in a two-level type→name→value registry.
//
// `server` and `client` are stored keyed by type, but the namespace expressions
// see is flat — `server.x` is one server whatever its type. So a duplicate name
// is not necessarily in the same bucket as the block that collides with it, and
// looking only in that bucket finds nothing and reports it as if there were no
// conflict at all.
func findAcrossTypes[V any](byType map[string]map[string]V, name string) (V, bool) {
	for _, byName := range byType {
		if v, ok := byName[name]; ok {
			return v, true
		}
	}
	var zero V
	return zero, false
}

func GetBlockHandlers() map[string]BlockHandler {
	return map[string]BlockHandler{
		"assert":       NewAssertBlockHandler(),
		"auth":         NewAuthBlockHandler(),
		"bus":          NewBusBlockHandler(),
		"check":        NewCheckBlockHandler(),
		"client":       NewClientBlockHandler(),
		"condition":    NewConditionBlockHandler(),
		"const":        NewConstBlockHandler(),
		"fsm":          NewFsmBlockHandler(),
		"metric":       NewMetricBlockHandler(),
		"server":       NewServerBlockHandler(),
		"subscription": NewSubscriptionBlockHandler(),
		"trigger":      NewTriggerBlockHandler(),
		"var":          NewVariableBlockHandler(),
		"wire_format":  NewWireFormatBlockHandler(),
	}
}

var blockSchema = []hcl.BlockHeaderSchema{
	{
		Type:       "assert",
		LabelNames: []string{"name"},
	},
	{
		Type:       "auth",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "editor",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "fsm",
		LabelNames: []string{"name"},
	},
	{
		Type:       "bus",
		LabelNames: []string{"name"},
	},
	{
		Type:       "check",
		LabelNames: []string{"name"},
	},
	{
		Type:       "client",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "condition",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "const",
		LabelNames: []string{},
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
		Type:       "metric",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "server",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "subscription",
		LabelNames: []string{"name"},
	},
	{
		Type:       "trigger",
		LabelNames: []string{"type", "name"},
	},
	{
		Type:       "var",
		LabelNames: []string{"name"},
	},
	{
		Type:       "wire_format",
		LabelNames: []string{"type", "name"},
	},
}

var configSchema = &hcl.BodySchema{
	Blocks: blockSchema,
}
