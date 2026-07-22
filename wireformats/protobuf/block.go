package protobuf

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// blockSchema is the closed schema for a wire_format "protobuf" body. Content()
// against it rejects unknown attributes for free.
var blockSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "descriptor_set", Required: true},
		{Name: "message", Required: false},
		{Name: "mode", Required: false},
	},
}

// Process is the WireFormatProcessor for the "protobuf" type. It decodes the
// block body, loads the descriptor set once, and returns either a single
// wire_format capsule (when message is set) or an object of capsules, one per
// message in the set (when message is omitted).
func Process(config *cfg.Config, block *hcl.Block, body hcl.Body) (cty.Value, hcl.Diagnostics) {
	content, diags := body.Content(blockSchema)
	if diags.HasErrors() {
		return cty.NilVal, diags
	}

	descriptorSet, dsRange, diags := stringAttr(config, content, "descriptor_set")
	if diags.HasErrors() {
		return cty.NilVal, diags
	}

	message, messageRange, diags := optionalStringAttr(config, content, "message")
	if diags.HasErrors() {
		return cty.NilVal, diags
	}

	mode, diags := decodeMode(config, content)
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

func stringAttr(config *cfg.Config, content *hcl.BodyContent, name string) (string, hcl.Range, hcl.Diagnostics) {
	attr := content.Attributes[name]
	val, diags := attr.Expr.Value(config.EvalCtx())
	if diags.HasErrors() {
		return "", attr.Range, diags
	}
	if val.IsNull() || val.Type() != cty.String {
		return "", attr.Range, diagAt(fmt.Sprintf("Invalid %s", name),
			fmt.Sprintf("%s must be a string", name), attr.Range)
	}
	return val.AsString(), attr.Range, nil
}

func optionalStringAttr(config *cfg.Config, content *hcl.BodyContent, name string) (string, hcl.Range, hcl.Diagnostics) {
	attr, ok := content.Attributes[name]
	if !ok {
		return "", hcl.Range{}, nil
	}
	val, diags := attr.Expr.Value(config.EvalCtx())
	if diags.HasErrors() {
		return "", attr.Range, diags
	}
	if val.IsNull() {
		return "", attr.Range, nil
	}
	if val.Type() != cty.String {
		return "", attr.Range, diagAt(fmt.Sprintf("Invalid %s", name),
			fmt.Sprintf("%s must be a string", name), attr.Range)
	}
	return val.AsString(), attr.Range, nil
}

func diagAt(summary, detail string, r hcl.Range) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  r.Ptr(),
	}}
}
