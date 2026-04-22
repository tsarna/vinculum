package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
)

// WireFormatProcessor processes a wire_format block of a given type and
// returns a cty.Value to expose in the evaluation context. The value is
// typically a capsule wrapping a wire.WireFormat, but may also be an
// object with multiple capsule attributes (e.g. a protobuf plugin
// returning one capsule per message type).
type WireFormatProcessor func(config *Config, block *hcl.Block, body hcl.Body) (cty.Value, hcl.Diagnostics)

var wireFormatRegistry = map[string]WireFormatProcessor{}

// RegisterWireFormatType registers a processor for a named wire format type.
// Sub-packages call this from their init() function.
func RegisterWireFormatType(typeName string, p WireFormatProcessor) {
	recordPlugin("wire_format." + typeName)
	wireFormatRegistry[typeName] = p
}

// WireFormatDefinition is the common HCL structure for wire_format blocks.
type WireFormatDefinition struct {
	Type          string    `hcl:",label"`
	Name          string    `hcl:",label"`
	DefRange      hcl.Range `hcl:",def_range"`
	RemainingBody hcl.Body  `hcl:",remain"`
}

// WireFormatBlockHandler implements BlockHandler for wire_format blocks.
type WireFormatBlockHandler struct {
	BlockHandlerBase
}

func NewWireFormatBlockHandler() *WireFormatBlockHandler {
	return &WireFormatBlockHandler{}
}

func (h *WireFormatBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "wire_format." + block.Labels[1], nil
}

func (h *WireFormatBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	def := WireFormatDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &def)
	if diags.HasErrors() {
		return diags
	}
	def.DefRange = block.DefRange

	if _, exists := config.CtyWireFormatMap[def.Name]; exists {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate wire_format name",
			Detail:   fmt.Sprintf("wire_format %q is already defined", def.Name),
			Subject:  block.DefRange.Ptr(),
		}}
	}

	processor, ok := wireFormatRegistry[def.Type]
	if !ok {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unknown wire_format type",
			Detail:   fmt.Sprintf("wire_format type %q is not registered", def.Type),
			Subject:  &block.DefRange,
		}}
	}

	val, diags := processor(config, block, def.RemainingBody)
	if diags.HasErrors() {
		return diags
	}

	config.CtyWireFormatMap[def.Name] = val
	config.evalCtx.Variables["wire_format"] = cty.ObjectVal(config.CtyWireFormatMap)

	return nil
}
