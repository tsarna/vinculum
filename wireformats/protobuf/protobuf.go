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
	cfg.RegisterWireFormatType("protobuf", Process)
}
