package protobuf

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

// deserializeJSON decodes protobuf binary and renders it in protojson form,
// then parses that JSON into native Go so cfg.CtyWireFormat's AnyToCty produces
// the VCL value. protojson IS the specification for the json representation, so
// camelCase names, RFC3339 timestamps, base64 bytes, string-encoded 64-bit
// ints, and Any/@type all come for free.
func (f *protoFormat) deserializeJSON(b []byte) (any, error) {
	msg := dynamicpb.NewMessage(f.md)
	if err := proto.Unmarshal(b, msg); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", f.md.FullName(), err)
	}

	jsonBytes, err := protojson.MarshalOptions{Resolver: f.schema.resolver}.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encoding %s as json: %w", f.md.FullName(), err)
	}

	var out any
	if err := json.Unmarshal(jsonBytes, &out); err != nil {
		return nil, fmt.Errorf("parsing json for %s: %w", f.md.FullName(), err)
	}
	return out, nil
}

// serializeJSON is the inverse: native Go -> JSON bytes -> protojson.Unmarshal
// into a dynamic message -> binary. Unknown fields are rejected
// (DiscardUnknown: false), matching the strict-serialize contract.
func (f *protoFormat) serializeJSON(v any) ([]byte, error) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding value as json for %s: %w", f.md.FullName(), err)
	}

	msg := dynamicpb.NewMessage(f.md)
	if err := (protojson.UnmarshalOptions{Resolver: f.schema.resolver}).Unmarshal(jsonBytes, msg); err != nil {
		return nil, fmt.Errorf("encoding %s: %w", f.md.FullName(), err)
	}
	return proto.Marshal(msg)
}
