package protobuf

import (
	"fmt"
	"strconv"

	bytescty "github.com/tsarna/bytes-cty-type"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// deserializeNative decodes protobuf binary and builds the VCL value tree
// directly as a cty.Value. cfg.CtyWireFormat runs go2cty2go.AnyToCty over our
// return, which passes a cty.Value through unchanged — so the rich leaves
// (Time/Duration capsules, bytes objects) survive.
func (f *protoFormat) deserializeNative(b []byte) (any, error) {
	msg := dynamicpb.NewMessage(f.md)
	if err := proto.Unmarshal(b, msg); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", f.md.FullName(), err)
	}
	return f.schema.messageToCty(msg)
}

// messageToCty converts a protoreflect message to a cty value. Well-known types
// are handled specially (wkt.go, anypb.go); everything else becomes a cty
// object keyed by snake_case proto field name.
func (s *schema) messageToCty(m protoreflect.Message) (cty.Value, error) {
	md := m.Descriptor()
	if wkt := wktName(md); wkt != "" {
		return s.wktToCty(wkt, m)
	}

	fields := md.Fields()
	attrs := make(map[string]cty.Value, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		name := string(fd.Name())

		// Presence fields (proto2 optional, proto3 optional, message-typed,
		// oneof members) that are unset decode to null. No-presence scalars
		// decode to their proto default (zero).
		if fd.HasPresence() && !m.Has(fd) {
			attrs[name] = cty.NullVal(cty.DynamicPseudoType)
			continue
		}

		val, err := s.fieldToCty(fd, m.Get(fd))
		if err != nil {
			return cty.NilVal, fmt.Errorf("%s.%s: %w", md.FullName(), name, err)
		}
		attrs[name] = val
	}
	return cty.ObjectVal(attrs), nil
}

// fieldToCty converts a field value honoring cardinality (repeated, map, or
// singular).
func (s *schema) fieldToCty(fd protoreflect.FieldDescriptor, v protoreflect.Value) (cty.Value, error) {
	switch {
	case fd.IsList():
		return s.listToCty(fd, v.List())
	case fd.IsMap():
		return s.mapToCty(fd, v.Map())
	default:
		return s.singularToCty(fd, v)
	}
}

func (s *schema) listToCty(fd protoreflect.FieldDescriptor, list protoreflect.List) (cty.Value, error) {
	n := list.Len()

	// Repeated messages can produce cty objects of differing shape
	// (per-element presence), so use a tuple to avoid the list homogeneity
	// constraint. Repeated scalars are homogeneous and use a typed list.
	if isMessageKind(fd.Kind()) {
		if n == 0 {
			return cty.EmptyTupleVal, nil
		}
		elems := make([]cty.Value, n)
		for i := 0; i < n; i++ {
			ev, err := s.singularToCty(fd, list.Get(i))
			if err != nil {
				return cty.NilVal, fmt.Errorf("[%d]: %w", i, err)
			}
			elems[i] = ev
		}
		return cty.TupleVal(elems), nil
	}

	if n == 0 {
		return cty.ListValEmpty(scalarCtyType(fd)), nil
	}
	elems := make([]cty.Value, n)
	for i := 0; i < n; i++ {
		ev, err := s.singularToCty(fd, list.Get(i))
		if err != nil {
			return cty.NilVal, fmt.Errorf("[%d]: %w", i, err)
		}
		elems[i] = ev
	}
	return cty.ListVal(elems), nil
}

func (s *schema) mapToCty(fd protoreflect.FieldDescriptor, m protoreflect.Map) (cty.Value, error) {
	if m.Len() == 0 {
		return cty.EmptyObjectVal, nil
	}
	valFd := fd.MapValue()
	attrs := make(map[string]cty.Value, m.Len())
	var rangeErr error
	m.Range(func(mk protoreflect.MapKey, v protoreflect.Value) bool {
		cv, err := s.singularToCty(valFd, v)
		if err != nil {
			rangeErr = fmt.Errorf("[%s]: %w", mk.String(), err)
			return false
		}
		attrs[mapKeyString(fd.MapKey(), mk)] = cv
		return true
	})
	if rangeErr != nil {
		return cty.NilVal, rangeErr
	}
	return cty.ObjectVal(attrs), nil
}

// singularToCty converts one non-repeated, non-map value.
func (s *schema) singularToCty(fd protoreflect.FieldDescriptor, v protoreflect.Value) (cty.Value, error) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return cty.BoolVal(v.Bool()), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return cty.NumberIntVal(v.Int()), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return cty.NumberUIntVal(v.Uint()), nil
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return cty.NumberFloatVal(v.Float()), nil
	case protoreflect.StringKind:
		return cty.StringVal(v.String()), nil
	case protoreflect.BytesKind:
		return bytescty.BuildBytesObject(v.Bytes(), cfg.DefaultBytesContentType), nil
	case protoreflect.EnumKind:
		num := v.Enum()
		if ev := fd.Enum().Values().ByNumber(num); ev != nil {
			return cty.StringVal(string(ev.Name())), nil
		}
		return cty.NumberIntVal(int64(num)), nil // unknown value passes through as int
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return s.messageToCty(v.Message())
	default:
		return cty.NilVal, fmt.Errorf("unsupported field kind %s", fd.Kind())
	}
}

// scalarCtyType returns the cty element type for a repeated scalar field, so an
// empty repeated field can produce a correctly typed empty list.
func scalarCtyType(fd protoreflect.FieldDescriptor) cty.Type {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return cty.Bool
	case protoreflect.StringKind, protoreflect.EnumKind:
		return cty.String
	case protoreflect.BytesKind:
		return bytescty.BytesObjectType
	default:
		return cty.Number
	}
}

// mapKeyString renders a protobuf map key as the string attribute name used in
// the cty object. Keys are always integral, bool, or string.
func mapKeyString(keyFd protoreflect.FieldDescriptor, mk protoreflect.MapKey) string {
	switch keyFd.Kind() {
	case protoreflect.BoolKind:
		return strconv.FormatBool(mk.Value().Bool())
	case protoreflect.StringKind:
		return mk.Value().String()
	default:
		// int32/int64/uint32/uint64/fixed/sfixed/sint — MapKey.String()
		// already renders the integer.
		return mk.String()
	}
}

func isMessageKind(k protoreflect.Kind) bool {
	return k == protoreflect.MessageKind || k == protoreflect.GroupKind
}
