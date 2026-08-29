package config

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"go.opentelemetry.io/otel/trace"
)

// ConditionDefinition is the common envelope for every condition subtype.
// Subtype-specific attributes live in RemainingBody and are decoded by the
// registered ConditionProcessor.
type ConditionDefinition struct {
	Type          string         `hcl:"type,label"`
	Name          string         `hcl:"name,label"`
	Disabled      bool           `hcl:"disabled,optional"`
	Tracing       hcl.Expression `hcl:"tracing,optional"`
	DefRange      hcl.Range      `hcl:",def_range"`
	RemainingBody hcl.Body       `hcl:",remain"`

	// TracerProvider is resolved from Tracing (or auto-wired) during Process()
	// and is available to subtype processors via def.TracerProvider. Resolving
	// it here rather than in each subtype is what makes an explicit backend
	// possible at all: the four of them used to pass a nil expression and take
	// whatever the default was.
	TracerProvider trace.TracerProvider
}

// ConditionProcessor processes a single condition block of a given subtype.
type ConditionProcessor func(config *Config, block *hcl.Block, def *ConditionDefinition) hcl.Diagnostics

// ConditionRegistration holds the processor and metadata for a condition
// subtype. HasDependencyId is true when the subtype publishes a cty value at
// condition.<name> (all current subtypes do; the flag exists for symmetry with
// TriggerRegistration).
type ConditionRegistration struct {
	Process         ConditionProcessor
	HasDependencyId bool
}

var conditionRegistry = map[string]ConditionRegistration{}

// RegisterConditionSubtype registers a condition subtype
// ("timer", "threshold", "counter"). Sub-packages call this from init(),
// optionally passing WithSchema to describe the block for `vinculum schema`.
func RegisterConditionSubtype(typeName string, reg ConditionRegistration, opts ...RegisterOption) {
	recordPlugin("condition." + typeName)
	conditionRegistry[typeName] = reg
	registerTypeSchema("condition", typeName, opts)
}

type ConditionBlockHandler struct {
	BlockHandlerBase
	BackendDeps
}

func NewConditionBlockHandler() *ConditionBlockHandler {
	return &ConditionBlockHandler{}
}

func (h *ConditionBlockHandler) Schema() TypeSchema {
	return TypeSchema{
		Summary: "A named boolean output derived from an input and behavioral rules.",
		Doc: `The first label selects the subtype and the second names it, making it
available in expressions as ` + "`condition.<name>`" + `. Conditions encode *when*
something should count as true — "has the temperature been above 80° for
30 seconds?", "have five errors arrived within a minute?".

Every subtype shares a four-state model (` + "`inactive`" + `, ` + "`pending_activation`" + `,
` + "`active`" + `, ` + "`pending_deactivation`" + `); ` + "`get()`" + ` returns the stable output and
` + "`state()`" + ` the full internal state. Conditions are watchable, so they compose:
one condition's ` + "`input`" + ` can read another, and ` + "`trigger \"watch\"`" + ` can observe
any of them.`,
	}
}

// GetBlockDependencyId returns "condition.<name>" so blocks referencing the
// condition are ordered after it.
func (h *ConditionBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	if len(block.Labels) == 2 {
		if reg, ok := conditionRegistry[block.Labels[0]]; ok && reg.HasDependencyId {
			return "condition." + block.Labels[1], nil
		}
	}
	return "", nil
}

// GetBlockDependencies adds the implicit backend dependency for a condition
// that does not name its tracing backend, which auto-wires to the default and
// so has to be processed after every block that could be one.
func (h *ConditionBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	return h.AddBackendDepsUnless(ExtractBlockDependencies(block), block, "tracing"), nil
}

func (h *ConditionBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	if len(block.Labels) != 2 {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid condition block",
			Detail:   "condition blocks require two labels: type and name",
			Subject:  &block.DefRange,
		}}
	}

	subtype := block.Labels[0]
	reg, ok := conditionRegistry[subtype]
	if !ok {
		return hcl.Diagnostics{
			unknownTypeDiag("condition", subtype, sortedKeys(conditionRegistry), block.DefRange),
		}
	}

	// Decode the attributes every condition accepts, whatever its subtype,
	// leaving the rest of the body to the subtype's own decode struct.
	def := &ConditionDefinition{}
	if diags := gohcl.DecodeBody(block.Body, config.evalCtx, def); diags.HasErrors() {
		return diags
	}
	// gohcl.DecodeBody does not populate label fields from a bare body.
	def.Type = subtype
	def.Name = block.Labels[1]
	def.DefRange = block.DefRange

	if def.Disabled {
		// Nothing is registered, so condition.<name> stays undefined and any
		// reference to it fails — the same as a disabled fsm.
		return nil
	}

	// Resolve tracing once for every subtype (explicit tracing = or auto-wire).
	tp, tracingDiags := config.ResolveTracerProvider(def.Tracing)
	if tracingDiags.HasErrors() {
		return tracingDiags
	}
	def.TracerProvider = tp

	return reg.Process(config, block, def)
}
