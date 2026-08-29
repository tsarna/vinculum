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
	SetImplicitBackendDeps(ids BackendBlockIDs)
}

// BackendBlockIDs names the blocks a configuration can auto-wire to, split by
// what each one provides.
//
// The split exists because the backends are not peers. A `client "otlp"`
// provides both metrics and tracing and consumes neither, which makes it the
// root: every implicit edge in the graph can point at it without closing a
// loop. A `server "metrics"` provides metrics but resolves an OTLP client of
// its own for tracing, so it may wait for the root and for nothing else.
type BackendBlockIDs struct {
	All     []string // every backend block
	Tracing []string // the client "otlp" blocks, the only source of a tracer
}

// BackendDeps is the storage half of BackendDependent. Embed it in a handler
// alongside BlockHandlerBase and add its dependencies from
// GetBlockDependencies, naming the attributes with which the block could have
// selected a backend itself.
type BackendDeps struct {
	backends BackendBlockIDs
}

// SetImplicitBackendDeps implements BackendDependent.
func (b *BackendDeps) SetImplicitBackendDeps(ids BackendBlockIDs) {
	b.backends = ids
}

// AddBackendDeps appends a dependency on every backend block.
func (b *BackendDeps) AddBackendDeps(deps []string, block *hcl.Block, attrs ...string) []string {
	return b.add(deps, b.backends.All, block, attrs)
}

// AddTracingBackendDeps appends a dependency on the `client "otlp"` blocks
// alone, for a block that consumes a tracer but not a metrics backend. Only the
// backends themselves need this precision: for anything else the extra edges
// are harmless, and a rule with no exceptions is worth more than a tight one.
func (b *BackendDeps) AddTracingBackendDeps(deps []string, block *hcl.Block, attrs ...string) []string {
	return b.add(deps, b.backends.Tracing, block, attrs)
}

// add appends ids to deps unless the block names a backend in every one of
// attrs — every attribute with which it could have selected one. A block that
// names its backend explicitly already depends on it, through the ordinary
// reference extraction, so the implicit edge would only order it after backends
// it has nothing to do with.
//
// Naming *some* of them is not enough: a bus with `metrics` and no `tracing`
// still auto-wires a tracer, and skipping there would leave exactly the bug
// this exists to fix. The cost of the other direction is one redundant edge —
// a block waiting for a backend of a kind it already named — which constrains
// the sort slightly and changes nothing else.
func (b *BackendDeps) add(deps, ids []string, block *hcl.Block, attrs []string) []string {
	if syntaxBody, ok := block.Body.(*hclsyntax.Body); ok {
		named := 0
		for _, attr := range attrs {
			if _, set := syntaxBody.Attributes[attr]; set {
				named++
			}
		}
		if named == len(attrs) {
			return deps
		}
	}
	return append(deps, ids...)
}

// IsBackendBlock reports whether the block is itself a metrics or tracing
// backend: server "metrics" or client "otlp". A backend must never be given the
// blanket dependency every other server and client block takes — the set it
// would wait for includes itself, which is a cycle rather than an ordering.
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

// IsTracingBackendBlock reports whether the block provides a tracer, which only
// a client "otlp" does.
func IsTracingBackendBlock(block *hcl.Block) bool {
	return IsBackendBlock(block) && block.Type == "client"
}

// backendBlockIDs returns the dependency IDs of every backend block, in the
// form GetBlockDependencyId gives them.
func backendBlockIDs(blocks hcl.Blocks) BackendBlockIDs {
	var ids BackendBlockIDs
	for _, block := range blocks {
		if !IsBackendBlock(block) {
			continue
		}
		id := block.Type + "." + block.Labels[1]
		ids.All = append(ids.All, id)
		if IsTracingBackendBlock(block) {
			ids.Tracing = append(ids.Tracing, id)
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
