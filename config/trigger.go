package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/trace"
)

type TriggerDefinition struct {
	Type          string         `hcl:",label"`
	Name          string         `hcl:",label"`
	Disabled      bool           `hcl:"disabled,optional"`
	Tracing       hcl.Expression `hcl:"tracing,optional"`
	DefRange      hcl.Range      `hcl:",def_range"`
	RemainingBody hcl.Body       `hcl:",remain"`

	// TracerProvider is resolved from Tracing (or auto-wired) during Process()
	// and is available to trigger-type processors via triggerDef.TracerProvider.
	TracerProvider trace.TracerProvider
}

// TriggerBlockHandler processes trigger blocks. A fresh instance is created per
// Build() call; its registry is populated during FinishPreprocessing.
type TriggerBlockHandler struct {
	BlockHandlerBase
	registry map[string]TriggerRegistration // built in FinishPreprocessing; nil until then
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

// triggerRegistry holds unconditionally available trigger types, registered at
// init() time via RegisterTriggerType.
var triggerRegistry = map[string]TriggerRegistration{}

// RegisterTriggerType registers an unconditional trigger type.
// Sub-packages call this from their init() function.
func RegisterTriggerType(typeName string, reg TriggerRegistration) {
	recordPlugin("trigger." + typeName)
	triggerRegistry[typeName] = reg
}

// ConditionalTriggerTypePlugin is evaluated once per Build(). It returns a map
// of trigger type names to registrations when the required feature is enabled,
// or nil when the type should not be available for this config.
type ConditionalTriggerTypePlugin func(cfg *Config) map[string]TriggerRegistration

var conditionalTriggerPlugins []ConditionalTriggerTypePlugin

// RegisterConditionalTriggerType registers a trigger type whose availability
// depends on config state (e.g. a feature flag). Unlike RegisterTriggerType,
// it does NOT call recordPlugin at init time; the plugin name is recorded only
// when the factory returns a non-nil result during Build().
func RegisterConditionalTriggerType(factory ConditionalTriggerTypePlugin) {
	conditionalTriggerPlugins = append(conditionalTriggerPlugins, factory)
}

// FinishPreprocessing builds the per-build trigger registry by merging
// unconditional types with any conditional types whose features are enabled.
func (h *TriggerBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	h.registry = make(map[string]TriggerRegistration, len(triggerRegistry))
	for k, v := range triggerRegistry {
		h.registry[k] = v
	}
	for _, plugin := range conditionalTriggerPlugins {
		if types := plugin(config); types != nil {
			for typeName, reg := range types {
				recordPlugin("trigger." + typeName)
				h.registry[typeName] = reg
			}
		}
	}
	return nil
}

func (h *TriggerBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	// Exclude runtime-only attributes: action is evaluated when the trigger
	// fires, and stop_when is evaluated after each run. Neither should create
	// config-time dependencies (e.g. a trigger whose action sends to an FSM
	// that references the trigger in on_entry hooks).
	return ExtractBlockDependencies(block, "action", "stop_when"), nil
}

// GetBlockDependencyId returns "trigger.<name>" for trigger types that produce a
// cty value, enabling correct dependency ordering for blocks that reference
// trigger.<name>. Other trigger types return "" (no ordering needed).
// Falls back to the global (unconditional) registry when called before
// FinishPreprocessing.
func (h *TriggerBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	if len(block.Labels) == 2 {
		reg, ok := h.registry[block.Labels[0]]
		if !ok {
			// Fall back to unconditional registry (e.g. when called before FinishPreprocessing).
			reg, ok = triggerRegistry[block.Labels[0]]
		}
		if ok && reg.HasDependencyId {
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
	triggerDef.DefRange = block.DefRange

	if triggerDef.Disabled {
		return nil
	}

	// Resolve tracing provider (explicit tracing = or auto-wire).
	tp, tracingDiags := config.ResolveTracerProvider(triggerDef.Tracing)
	if tracingDiags.HasErrors() {
		return tracingDiags
	}
	triggerDef.TracerProvider = tp

	name := block.Labels[1]

	// Enforce name uniqueness across all non-disabled trigger blocks
	if existingRange, ok := config.TriggerDefRanges[name]; ok {
		return hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Trigger already defined",
			Detail:   fmt.Sprintf("Trigger %q already defined at %s", name, existingRange),
			Subject:  block.DefRange.Ptr(),
		}}
	}
	config.TriggerDefRanges[name] = block.DefRange

	reg, ok := h.registry[block.Labels[0]]
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
