// Package protobuf implements a "protobuf" wire format for Vinculum, exposed
// through the wire_format block and the github.com/tsarna/vinculum-wire
// WireFormat interface.
//
// Unlike the built-in auto/json/string/bytes formats, protobuf binary is not
// self-describing: the same bytes decode differently depending on the message
// type, and field names are not present on the wire. A protobuf wire format is
// therefore bound to exactly one message type and requires a schema — a
// compiled FileDescriptorSet — supplied ahead of time. A block that names a
// single message produces one wire_format capsule; a block that names a whole
// set produces an object of capsules, one per message (see block.go).
//
// The implementation is pure Go and CGO-clean (no protoc at runtime), so it
// ships in the minimal container image. It reflects messages via protoreflect
// and dynamicpb; the json mode round-trips through protojson.
package protobuf

import (
	cfg "github.com/tsarna/vinculum/config"

	// Blank-import the well-known-type packages so their descriptors are
	// present in protoregistry.GlobalFiles/GlobalTypes. This lets a user's
	// descriptor set reference google/protobuf/* types without bundling them
	// (i.e. without protoc --include_imports): the descriptor loader's overlay
	// resolves the missing imports from the global registry.
	_ "google.golang.org/protobuf/types/known/anypb"
	_ "google.golang.org/protobuf/types/known/durationpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	_ "google.golang.org/protobuf/types/known/fieldmaskpb"
	_ "google.golang.org/protobuf/types/known/structpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
	_ "google.golang.org/protobuf/types/known/wrapperspb"
)

func init() {
	cfg.RegisterWireFormatType("protobuf", Process, cfg.WithSchema(protobufSchema))
}

var protobufSchema = cfg.TypeSchema{
	Sample:  &protobufBody{},
	Summary: "Protocol Buffers binary, decoded and encoded against a supplied schema.",
	DocPage: "wire-format-protobuf.md",
	Doc: `Protobuf binary is not self-describing: the same bytes decode differently
depending on the message type, and field names never appear on the wire. So a
protobuf wire format is bound to exactly one message type, and a schema — a
compiled ` + "`FileDescriptorSet`" + ` — is mandatory.

Naming a ` + "`message`" + ` makes ` + "`wire_format.<name>`" + ` a single format,
interchangeable with a built-in one. Omitting it makes the block an object of
formats, one per message in the set, keyed by full name plus a bare short-name
alias wherever that short name is unique: ` + "`wire_format.<name>.acme.v1.Order`" + `
or just ` + "`wire_format.<name>.Order`" + `.

Every Vinculum transport delivers a discrete payload — one MQTT message, one
Kafka record, one HTTP body — so each payload is exactly one message.
Length-delimited stream framing is out of scope.

The implementation is pure Go, with no ` + "`protoc`" + ` at run time, so it ships in the
minimal container image.`,
	Attrs: map[string]cfg.AttrMeta{
		"descriptor_set": {
			Summary: "Path to a compiled `FileDescriptorSet`.",
			Doc:     "Produce one with `protoc --include_imports --descriptor_set_out=x.binpb x.proto` or `buf build -o x.binpb`. Relative paths resolve against the config directory, so a `git`-materialized schema tree works. `--include_imports` is recommended but not required: the `google/protobuf/*` well-known types are bundled and resolved automatically.",
		},
		"message": {
			Summary: "Fully-qualified name of the message to bind to.",
			Doc:     "For example `\"acme.orders.v1.Order\"`. Omit it to expose every message in the set.",
		},
		"mode": {
			Summary: "How messages are represented as VCL values.",
			Doc:     "Native maps protobuf types to their natural VCL counterparts; json round-trips through protojson's canonical JSON mapping. Applies to every message the block exposes.",
			Enum:    []string{"native", "json"},
			Default: `"native"`,
		},
	},
}
