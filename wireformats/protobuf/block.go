package protobuf

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// protobufBody is the decode struct for a wire_format "protobuf" body. It has
// no `,remain` field, so gohcl rejects any attribute not listed here. The
// *Range fields carry each attribute's source location for diagnostics that
// point at the offending attribute rather than the whole block.
type protobufBody struct {
	DescriptorSet      string    `hcl:"descriptor_set"`
	DescriptorSetRange hcl.Range `hcl:"descriptor_set,attr_range"`
	Message            string    `hcl:"message,optional"`
	MessageRange       hcl.Range `hcl:"message,attr_range"`
	Mode               string    `hcl:"mode,optional"`
	ModeRange          hcl.Range `hcl:"mode,attr_range"`
}

// Process is the WireFormatProcessor for the "protobuf" type. It decodes the
// block body, loads the descriptor set once, and returns either a single
// wire_format capsule (when message is set) or an object of capsules, one per
// message in the set (when message is omitted).
func Process(config *cfg.Config, block *hcl.Block, body hcl.Body) (cty.Value, hcl.Diagnostics) {
	var decoded protobufBody
	diags := gohcl.DecodeBody(body, config.EvalCtx(), &decoded)
	if diags.HasErrors() {
		return cty.NilVal, diags
	}

	descriptorSet, dsRange := decoded.DescriptorSet, decoded.DescriptorSetRange
	message, messageRange := decoded.Message, decoded.MessageRange

	mode, diags := decodeMode(decoded.Mode, decoded.ModeRange)
	if diags.HasErrors() {
		return cty.NilVal, diags
	}

	// Resolve and read the descriptor set relative to the config directory.
	path, err := cfg.SafeResolvePath(config.BaseDir, descriptorSet)
	if err != nil {
		return cty.NilVal, diagAt("Invalid descriptor_set path", err.Error(), dsRange)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cty.NilVal, diagAt("Cannot read descriptor_set",
			fmt.Sprintf("reading %q: %s", path, err), dsRange)
	}

	sch, err := loadSchema(raw)
	if err != nil {
		return cty.NilVal, diagAt("Invalid descriptor_set", err.Error(), dsRange)
	}

	if message != "" {
		md, ok := sch.messages[protoreflect.FullName(message)]
		if !ok {
			return cty.NilVal, diagAt("Unknown message",
				fmt.Sprintf("message %q is not present in the descriptor set", message), messageRange)
		}
		return cfg.NewWireFormatCapsule(newProtoFormat(sch, md, mode)), nil
	}

	if len(sch.messages) == 0 {
		return cty.NilVal, diagAt("Empty descriptor set",
			"the descriptor set contains no messages", dsRange)
	}

	return buildMessageObject(sch, mode), nil
}

// buildMessageObject builds the multi-message object: a full-name index key for
// every message, plus a bare short-name alias for every message whose short
// name is unique across the set.
func buildMessageObject(sch *schema, mode mode) cty.Value {
	unique := uniqueShortNames(sch.messages)
	attrs := make(map[string]cty.Value, len(sch.messages)*2)

	for full, md := range sch.messages {
		capsule := cfg.NewWireFormatCapsule(newProtoFormat(sch, md, mode))
		attrs[string(full)] = capsule
		if short := string(md.Name()); short != string(full) {
			if _, ok := unique[short]; ok {
				attrs[short] = capsule
			}
		}
	}

	return cty.ObjectVal(attrs)
}

// newProtoFormat constructs an immutable protoFormat bound to one message type.
func newProtoFormat(sch *schema, md protoreflect.MessageDescriptor, mode mode) *protoFormat {
	return &protoFormat{
		schema: sch,
		md:     md,
		mt:     dynamicpb.NewMessageType(md),
		mode:   mode,
	}
}

func diagAt(summary, detail string, r hcl.Range) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  r.Ptr(),
	}}
}
