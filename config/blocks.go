package config

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

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

// BackendDependent is implemented by a block handler whose blocks auto-wire to
// the default metrics or tracing backend when the attribute naming one is
// omitted. Such a block has to be processed after every block that could
// provide a backend: resolution happens during Process, so a block processed
// first resolves a default that does not exist yet and is silently left
// uninstrumented, which makes instrumentation depend on where the backend block
// happens to appear in the file.
//
// Build() hands the IDs of every backend block to each handler that implements
// this, before the dependency sort. When the dependency applies is the
// handler's own rule — see BackendDeps.
type BackendDependent interface {
	SetImplicitBackendDeps(ids []string)
}

// BackendDeps is the storage half of BackendDependent. Embed it in a handler
// alongside BlockHandlerBase and add its dependencies from
// GetBlockDependencies, naming the attributes with which the block could have
// selected a backend itself.
type BackendDeps struct {
	backendDeps []string
}

// SetImplicitBackendDeps implements BackendDependent.
func (b *BackendDeps) SetImplicitBackendDeps(ids []string) {
	b.backendDeps = ids
}

// AddBackendDepsUnless appends the backend dependencies unless the block sets
// one of attrs. A block that names its backend explicitly already depends on
// it, through the ordinary reference extraction — and adding the implicit edge
// anyway would order it after backends it has nothing to do with.
func (b *BackendDeps) AddBackendDepsUnless(deps []string, block *hcl.Block, attrs ...string) []string {
	if syntaxBody, ok := block.Body.(*hclsyntax.Body); ok {
		for _, attr := range attrs {
			if _, set := syntaxBody.Attributes[attr]; set {
				return deps
			}
		}
	}
	return append(deps, b.backendDeps...)
}

// IsBackendBlock reports whether the block is itself a metrics or tracing
// backend: server "metrics" or client "otlp". These provide the default that
// everything else auto-wires to, so they must never be given a dependency on
// one — a backend does not wait for a backend, and under a blanket rule it
// would end up waiting for itself.
func IsBackendBlock(block *hcl.Block) bool {
	if len(block.Labels) != 2 {
		return false
	}
	switch block.Type {
	case "server":
		return block.Labels[0] == "metrics"
	case "client":
		return block.Labels[0] == "otlp"
	}
	return false
}

// backendBlockIDs returns the dependency IDs of every backend block, in the
// form GetBlockDependencyId gives them.
func backendBlockIDs(blocks hcl.Blocks) []string {
	var ids []string
	for _, block := range blocks {
		if IsBackendBlock(block) {
			ids = append(ids, block.Type+"."+block.Labels[1])
		}
	}
	return ids
}

// setImplicitBackendDeps hands those IDs to every handler that asks for them.
func setImplicitBackendDeps(handlers map[string]BlockHandler, blocks hcl.Blocks) {
	ids := backendBlockIDs(blocks)
	for _, handler := range handlers {
		if bd, ok := handler.(BackendDependent); ok {
			bd.SetImplicitBackendDeps(ids)
		}
	}
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
