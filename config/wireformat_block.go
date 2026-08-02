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
// Sub-packages call this from their init() function, optionally passing
// WithSchema to describe the block for `vinculum schema`.
func RegisterWireFormatType(typeName string, p WireFormatProcessor, opts ...RegisterOption) {
	recordPlugin("wire_format." + typeName)
	wireFormatRegistry[typeName] = p
	registerTypeSchema("wire_format", typeName, opts)
}

// WireFormatDefinition is the common HCL structure for wire_format blocks.
type WireFormatDefinition struct {
	Type          string    `hcl:"type,label"`
	Name          string    `hcl:"name,label"`
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

func (h *WireFormatBlockHandler) Schema() TypeSchema {
	return TypeSchema{
		Summary: "A named encoder/decoder for message payloads.",
		Doc: `The first label selects the format type and the second names it, making it
available in expressions as ` + "`wire_format.<name>`" + `. Assign it to a client's
` + "`wire_format`" + ` attribute to control how that client's payloads are decoded on
the way in and encoded on the way out.

Declared formats sit alongside the built-in ` + "`auto`" + `, ` + "`json`" + `, ` + "`string`" + `, and
` + "`bytes`" + ` values and are used interchangeably with them. A block is needed only
for formats that cannot decode blind — ones that require a schema.`,
	}
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
	// gohcl.DecodeBody does not populate ,label fields from a bare body, so
	// take the type and name from the block labels directly (the schema in
	// blocks.go guarantees exactly two: type, name).
	def.Type = block.Labels[0]
	def.Name = block.Labels[1]
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
