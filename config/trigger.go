package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
)

type TriggerDefinition struct {
	Type          string    `hcl:",label"`
	Name          string    `hcl:",label"`
	Disabled      bool      `hcl:"disabled,optional"`
	DefRange      hcl.Range `hcl:",def_range"`
	RemainingBody hcl.Body  `hcl:",remain"`
}

type TriggerBlockHandler struct {
	BlockHandlerBase
}

func NewTriggerBlockHandler() *TriggerBlockHandler {
	return &TriggerBlockHandler{}
}

// TriggerProcessor is a function that processes a trigger block.
type TriggerProcessor func(config *Config, block *hcl.Block, def *TriggerDefinition) hcl.Diagnostics

// TriggerRegistration holds the processor and metadata for a trigger type.
type TriggerRegistration struct {
	Process         TriggerProcessor
	HasDependencyId bool // true if this trigger type produces a cty value at trigger.<name>
}

var triggerRegistry = map[string]TriggerRegistration{}

// RegisterTriggerType registers a trigger type by name.
// Sub-packages call this from their init() function.
func RegisterTriggerType(typeName string, reg TriggerRegistration) {
	recordPlugin("trigger." + typeName)
	triggerRegistry[typeName] = reg
}

// GetBlockDependencyId returns "trigger.<name>" for trigger types that produce a
// cty value (like "start"), enabling correct dependency ordering for blocks that
// reference trigger.<name>. Other trigger types return "" (no ordering needed).
func (h *TriggerBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	if len(block.Labels) == 2 {
		if reg, ok := triggerRegistry[block.Labels[0]]; ok && reg.HasDependencyId {
			return "trigger." + block.Labels[1], nil
		}
	}
	return "", nil
}

func (h *TriggerBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	triggerDef := TriggerDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &triggerDef)
	if diags.HasErrors() {
		return diags
	}

	if triggerDef.Disabled {
		return nil
	}

	name := block.Labels[1]

	// Enforce name uniqueness across all non-disabled trigger blocks
	if existingRange, ok := config.TriggerDefRanges[name]; ok {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Trigger already defined",
			Detail:   fmt.Sprintf("Trigger %q already defined at %s", name, existingRange),
			Subject:  &triggerDef.DefRange,
		}}
	}
	config.TriggerDefRanges[name] = triggerDef.DefRange

	reg, ok := triggerRegistry[block.Labels[0]]
	if !ok {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid trigger type",
			Detail:   fmt.Sprintf("Invalid trigger type: %q", block.Labels[0]),
			Subject:  &block.DefRange,
		}}
	}
	return reg.Process(config, block, &triggerDef)
}

// CtyObjectOrEmpty returns cty.ObjectVal(m) when m is non-empty, or
// cty.EmptyObjectVal when m has no entries (cty.ObjectVal panics on empty maps).
func CtyObjectOrEmpty(m map[string]cty.Value) cty.Value {
	if len(m) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(m)
}
