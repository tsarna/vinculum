package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

// ConditionDefinition is the common envelope for every condition subtype.
// Subtype-specific attributes live in RemainingBody and are decoded by the
// registered ConditionProcessor.
type ConditionDefinition struct {
	Type          string
	Name          string
	DefRange      hcl.Range
	RemainingBody hcl.Body
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
// ("timer", "threshold", "counter"). Sub-packages call this from init().
func RegisterConditionSubtype(typeName string, reg ConditionRegistration) {
	recordPlugin("condition." + typeName)
	conditionRegistry[typeName] = reg
}

type ConditionBlockHandler struct {
	BlockHandlerBase
}

func NewConditionBlockHandler() *ConditionBlockHandler {
	return &ConditionBlockHandler{}
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
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unknown condition subtype",
			Detail:   fmt.Sprintf("Unknown condition subtype: %q (expected timer, threshold, or counter)", subtype),
			Subject:  &block.DefRange,
		}}
	}

	def := &ConditionDefinition{
		Type:          subtype,
		Name:          block.Labels[1],
		RemainingBody: block.Body,
		DefRange:      block.DefRange,
	}
	return reg.Process(config, block, def)
}
