package protobuf

import (
	"fmt"
	"math"
	"strconv"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// serializeNative encodes a native Go value (already reduced from cty by
// cfg.CtyWireFormat) into protobuf binary.
func (f *protoFormat) serializeNative(v any) ([]byte, error) {
	msg := dynamicpb.NewMessage(f.md)
	if err := f.schema.nativeToMessage(f.md, msg, v, string(f.md.Name())); err != nil {
		return nil, err
	}
	return proto.Marshal(msg)
}

// nativeToMessage populates a protoreflect message from a native Go value.
// path carries the field path for error messages.
func (s *schema) nativeToMessage(md protoreflect.MessageDescriptor, m protoreflect.Message, v any, path string) error {
	if wkt := wktName(md); wkt != "" {
		return s.wktFromNative(wkt, m, v, path)
	}

	obj, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: expected an object for message %s, got %T", path, md.FullName(), v)
	}

	fields := md.Fields()
	for key, val := range obj {
		fd := fields.ByName(protoreflect.Name(key))
		if fd == nil {
			return fmt.Errorf("%s: unknown field %q in message %s", path, key, md.FullName())
		}
		// A null / absent value leaves the field at its proto default.
		if val == nil {
			continue
		}
		fpath := path + "." + key
		if err := s.setField(m, fd, val, fpath); err != nil {
			return err
		}
	}

	// proto2 required fields must be present.
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Cardinality() == protoreflect.Required && !m.Has(fd) {
			return fmt.Errorf("%s: missing required field %q in message %s", path, fd.Name(), md.FullName())
		}
	}
	return nil
}

func (s *schema) setField(m protoreflect.Message, fd protoreflect.FieldDescriptor, val any, path string) error {
	switch {
	case fd.IsList():
		slice, ok := val.([]any)
		if !ok {
			return fmt.Errorf("%s: expected a list, got %T", path, val)
		}
		list := m.Mutable(fd).List()
		for i, elem := range slice {
			epath := fmt.Sprintf("%s[%d]", path, i)
			if isMessageKind(fd.Kind()) {
				ev := list.NewElement()
				if err := s.nativeToMessage(fd.Message(), ev.Message(), elem, epath); err != nil {
					return err
				}
				list.Append(ev)
			} else {
				pv, err := s.scalarToProto(fd, elem, epath)
				if err != nil {
					return err
				}
				list.Append(pv)
			}
		}
		return nil

	case fd.IsMap():
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected an object for map, got %T", path, val)
		}
		mp := m.Mutable(fd).Map()
		valFd := fd.MapValue()
		for k, elem := range obj {
			mk, err := mapKeyFromString(fd.MapKey(), k, path)
			if err != nil {
				return err
			}
			epath := fmt.Sprintf("%s[%q]", path, k)
			if isMessageKind(valFd.Kind()) {
				ev := mp.NewValue()
				if err := s.nativeToMessage(valFd.Message(), ev.Message(), elem, epath); err != nil {
					return err
				}
				mp.Set(mk, ev)
			} else {
				pv, err := s.scalarToProto(valFd, elem, epath)
				if err != nil {
					return err
				}
				mp.Set(mk, pv)
			}
		}
		return nil

	default:
		if isMessageKind(fd.Kind()) {
			sub := m.Mutable(fd).Message()
			return s.nativeToMessage(fd.Message(), sub, val, path)
		}
		pv, err := s.scalarToProto(fd, val, path)
		if err != nil {
			return err
		}
		m.Set(fd, pv)
		return nil
	}
}

// scalarToProto coerces a native Go leaf to a protoreflect.Value of the field's
// kind, with range and type checks.
func (s *schema) scalarToProto(fd protoreflect.FieldDescriptor, val any, path string) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		b, ok := val.(bool)
		if !ok {
			return protoreflect.Value{}, typeErr(path, "bool", val)
		}
		return protoreflect.ValueOfBool(b), nil

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := toInt64(val, path)
		if err != nil {
			return protoreflect.Value{}, err
		}
		if n < math.MinInt32 || n > math.MaxInt32 {
			return protoreflect.Value{}, fmt.Errorf("%s: %d out of range for int32", path, n)
		}
		return protoreflect.ValueOfInt32(int32(n)), nil

	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := toInt64(val, path)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt64(n), nil

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := toUint64(val, path)
		if err != nil {
			return protoreflect.Value{}, err
		}
		if n > math.MaxUint32 {
			return protoreflect.Value{}, fmt.Errorf("%s: %d out of range for uint32", path, n)
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil

	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := toUint64(val, path)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint64(n), nil

	case protoreflect.FloatKind:
		fl, err := toFloat64(val, path)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat32(float32(fl)), nil

	case protoreflect.DoubleKind:
		fl, err := toFloat64(val, path)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat64(fl), nil

	case protoreflect.StringKind:
		str, ok := val.(string)
		if !ok {
			return protoreflect.Value{}, typeErr(path, "string", val)
		}
		return protoreflect.ValueOfString(str), nil

	case protoreflect.BytesKind:
		b, err := toBytes(val, path)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBytes(b), nil

	case protoreflect.EnumKind:
		return enumToProto(fd, val, path)

	default:
		return protoreflect.Value{}, fmt.Errorf("%s: unsupported field kind %s", path, fd.Kind())
	}
}

// enumToProto accepts either an enum value's string name or its integer number.
func enumToProto(fd protoreflect.FieldDescriptor, val any, path string) (protoreflect.Value, error) {
	switch t := val.(type) {
	case string:
		ev := fd.Enum().Values().ByName(protoreflect.Name(t))
		if ev == nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %q is not a value of enum %s", path, t, fd.Enum().FullName())
		}
		return protoreflect.ValueOfEnum(ev.Number()), nil
	default:
		n, err := toInt64(val, path)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: enum must be a name or integer: %w", path, err)
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), nil
	}
}

func mapKeyFromString(keyFd protoreflect.FieldDescriptor, k, path string) (protoreflect.MapKey, error) {
	switch keyFd.Kind() {
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(k)
		if err != nil {
			return protoreflect.MapKey{}, fmt.Errorf("%s: invalid bool map key %q", path, k)
		}
		return protoreflect.ValueOfBool(b).MapKey(), nil
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(k).MapKey(), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(k, 10, 64)
		if err != nil {
			return protoreflect.MapKey{}, fmt.Errorf("%s: invalid unsigned map key %q", path, k)
		}
		if keyFd.Kind() == protoreflect.Uint32Kind || keyFd.Kind() == protoreflect.Fixed32Kind {
			return protoreflect.ValueOfUint32(uint32(n)).MapKey(), nil
		}
		return protoreflect.ValueOfUint64(n).MapKey(), nil
	default:
		n, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			return protoreflect.MapKey{}, fmt.Errorf("%s: invalid integer map key %q", path, k)
		}
		switch keyFd.Kind() {
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			return protoreflect.ValueOfInt32(int32(n)).MapKey(), nil
		default:
			return protoreflect.ValueOfInt64(n).MapKey(), nil
		}
	}
}

// --- numeric coercion from reduced-cty native Go ---

func toInt64(val any, path string) (int64, error) {
	switch n := val.(type) {
	case int:
		return int64(n), nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		return int64(n), nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		if n > math.MaxInt64 {
			return 0, fmt.Errorf("%s: %d out of range for int64", path, n)
		}
		return int64(n), nil
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("%s: %v is not an integer", path, n)
		}
		return int64(n), nil
	case float32:
		return toInt64(float64(n), path)
	default:
		return 0, typeErr(path, "integer", val)
	}
}

func toUint64(val any, path string) (uint64, error) {
	switch n := val.(type) {
	case int, int8, int16, int32, int64:
		i, _ := toInt64(val, path)
		if i < 0 {
			return 0, fmt.Errorf("%s: %d is negative for an unsigned field", path, i)
		}
		return uint64(i), nil
	case uint:
		return uint64(n), nil
	case uint8:
		return uint64(n), nil
	case uint16:
		return uint64(n), nil
	case uint32:
		return uint64(n), nil
	case uint64:
		return n, nil
	case float64:
		if n != math.Trunc(n) || n < 0 {
			return 0, fmt.Errorf("%s: %v is not a non-negative integer", path, n)
		}
		return uint64(n), nil
	case float32:
		return toUint64(float64(n), path)
	default:
		return 0, typeErr(path, "unsigned integer", val)
	}
}

func toFloat64(val any, path string) (float64, error) {
	switch n := val.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int8:
		return float64(n), nil
	case int16:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint:
		return float64(n), nil
	case uint8:
		return float64(n), nil
	case uint16:
		return float64(n), nil
	case uint32:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	default:
		return 0, typeErr(path, "number", val)
	}
}

// toBytes accepts a reduced bytes object ([]byte) or a string (raw bytes).
func toBytes(val any, path string) ([]byte, error) {
	switch b := val.(type) {
	case []byte:
		return b, nil
	case string:
		return []byte(b), nil
	default:
		return nil, typeErr(path, "bytes or string", val)
	}
}

func typeErr(path, want string, got any) error {
	return fmt.Errorf("%s: expected %s, got %T", path, want, got)
}
