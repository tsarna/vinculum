package protobuf

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tsarna/go2cty2go"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Well-known-type full names that receive a special VCL representation.
const (
	wktTimestamp = "google.protobuf.Timestamp"
	wktDuration  = "google.protobuf.Duration"
	wktStruct    = "google.protobuf.Struct"
	wktValue     = "google.protobuf.Value"
	wktListValue = "google.protobuf.ListValue"
	wktFieldMask = "google.protobuf.FieldMask"
	wktEmpty     = "google.protobuf.Empty"
	wktAny       = "google.protobuf.Any"
)

// wrapperValueKind maps each wrapper message full name to true. Wrappers carry
// a single "value" field and render as the unwrapped scalar (or null).
var wrapperTypes = map[string]bool{
	"google.protobuf.DoubleValue": true,
	"google.protobuf.FloatValue":  true,
	"google.protobuf.Int64Value":  true,
	"google.protobuf.UInt64Value": true,
	"google.protobuf.Int32Value":  true,
	"google.protobuf.UInt32Value": true,
	"google.protobuf.BoolValue":   true,
	"google.protobuf.StringValue": true,
	"google.protobuf.BytesValue":  true,
}

var specialWKT = map[string]bool{
	wktTimestamp: true, wktDuration: true, wktStruct: true, wktValue: true,
	wktListValue: true, wktFieldMask: true, wktEmpty: true, wktAny: true,
}

// wktName returns the message full name if it is a well-known type this package
// represents specially, or "" otherwise.
func wktName(md protoreflect.MessageDescriptor) string {
	name := string(md.FullName())
	if specialWKT[name] || wrapperTypes[name] {
		return name
	}
	return ""
}

// wktToCty converts a well-known-type message to its native-mode cty value.
func (s *schema) wktToCty(name string, m protoreflect.Message) (cty.Value, error) {
	md := m.Descriptor()
	switch {
	case name == wktTimestamp:
		return timecty.NewTimeCapsule(readTimestamp(m)), nil
	case name == wktDuration:
		return timecty.NewDurationCapsule(readDuration(m)), nil
	case name == wktEmpty:
		return cty.EmptyObjectVal, nil
	case name == wktFieldMask:
		return fieldMaskToCty(m), nil
	case name == wktAny:
		return s.anyToCty(m)
	case name == wktStruct || name == wktValue || name == wktListValue:
		return s.jsonWKTToCty(m)
	case wrapperTypes[name]:
		valFd := md.Fields().ByName("value")
		return s.singularToCty(valFd, m.Get(valFd))
	default:
		return cty.NilVal, fmt.Errorf("unhandled well-known type %s", name)
	}
}

// wktFromNative populates a well-known-type message from a native Go value.
func (s *schema) wktFromNative(name string, m protoreflect.Message, v any, path string) error {
	md := m.Descriptor()
	switch {
	case name == wktTimestamp:
		t, err := toTime(v, path)
		if err != nil {
			return err
		}
		m.Set(md.Fields().ByName("seconds"), protoreflect.ValueOfInt64(t.Unix()))
		m.Set(md.Fields().ByName("nanos"), protoreflect.ValueOfInt32(int32(t.Nanosecond())))
		return nil
	case name == wktDuration:
		d, err := toDuration(v, path)
		if err != nil {
			return err
		}
		m.Set(md.Fields().ByName("seconds"), protoreflect.ValueOfInt64(int64(d/time.Second)))
		m.Set(md.Fields().ByName("nanos"), protoreflect.ValueOfInt32(int32(d%time.Second)))
		return nil
	case name == wktEmpty:
		return nil // no fields
	case name == wktFieldMask:
		return fieldMaskFromNative(m, v, path)
	case name == wktAny:
		return s.anyFromNative(m, v, path)
	case name == wktStruct || name == wktValue || name == wktListValue:
		return s.jsonWKTFromNative(m, v, path)
	case wrapperTypes[name]:
		valFd := md.Fields().ByName("value")
		pv, err := s.scalarToProto(valFd, v, path)
		if err != nil {
			return err
		}
		m.Set(valFd, pv)
		return nil
	default:
		return fmt.Errorf("%s: unhandled well-known type %s", path, name)
	}
}

func readTimestamp(m protoreflect.Message) time.Time {
	md := m.Descriptor()
	secs := m.Get(md.Fields().ByName("seconds")).Int()
	nanos := m.Get(md.Fields().ByName("nanos")).Int()
	return time.Unix(secs, nanos).UTC()
}

func readDuration(m protoreflect.Message) time.Duration {
	md := m.Descriptor()
	secs := m.Get(md.Fields().ByName("seconds")).Int()
	nanos := m.Get(md.Fields().ByName("nanos")).Int()
	return time.Duration(secs)*time.Second + time.Duration(nanos)
}

func fieldMaskToCty(m protoreflect.Message) cty.Value {
	paths := m.Get(m.Descriptor().Fields().ByName("paths")).List()
	if paths.Len() == 0 {
		return cty.ListValEmpty(cty.String)
	}
	elems := make([]cty.Value, paths.Len())
	for i := 0; i < paths.Len(); i++ {
		elems[i] = cty.StringVal(paths.Get(i).String())
	}
	return cty.ListVal(elems)
}

func fieldMaskFromNative(m protoreflect.Message, v any, path string) error {
	list := m.Mutable(m.Descriptor().Fields().ByName("paths")).List()
	switch t := v.(type) {
	case string:
		for _, p := range strings.Split(t, ",") {
			list.Append(protoreflect.ValueOfString(strings.TrimSpace(p)))
		}
		return nil
	case []any:
		for i, e := range t {
			str, ok := e.(string)
			if !ok {
				return fmt.Errorf("%s[%d]: expected a path string, got %T", path, i, e)
			}
			list.Append(protoreflect.ValueOfString(str))
		}
		return nil
	default:
		return fmt.Errorf("%s: FieldMask expects a list of strings or a comma-joined string, got %T", path, v)
	}
}

// jsonWKTToCty converts Struct/Value/ListValue via protojson (which renders
// them as their natural JSON), then builds cty from the parsed native.
func (s *schema) jsonWKTToCty(m protoreflect.Message) (cty.Value, error) {
	jsonBytes, err := protojson.MarshalOptions{Resolver: s.resolver}.Marshal(m.Interface())
	if err != nil {
		return cty.NilVal, err
	}
	var native any
	if err := json.Unmarshal(jsonBytes, &native); err != nil {
		return cty.NilVal, err
	}
	return go2cty2go.AnyToCty(native)
}

func (s *schema) jsonWKTFromNative(m protoreflect.Message, v any, path string) error {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := (protojson.UnmarshalOptions{Resolver: s.resolver}).Unmarshal(jsonBytes, m.Interface()); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// toTime accepts a reduced Time capsule (*timecty.Timestamp), a time.Time, an
// RFC 3339 string, or an epoch-seconds number.
func toTime(v any, path string) (time.Time, error) {
	switch t := v.(type) {
	case *timecty.Timestamp:
		return t.Time, nil
	case timecty.Timestamp:
		return t.Time, nil
	case time.Time:
		return t, nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s: invalid RFC3339 timestamp %q: %w", path, t, err)
		}
		return parsed, nil
	default:
		secs, err := toFloat64(v, path)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s: Timestamp expects a time, RFC3339 string, or epoch number: %w", path, err)
		}
		whole, frac := splitFloat(secs)
		return time.Unix(whole, frac).UTC(), nil
	}
}

// toDuration accepts a reduced Duration capsule (*timecty.Duration), a
// time.Duration, a duration string, or a seconds number.
func toDuration(v any, path string) (time.Duration, error) {
	switch d := v.(type) {
	case *timecty.Duration:
		return d.Duration, nil
	case timecty.Duration:
		return d.Duration, nil
	case time.Duration:
		return d, nil
	case string:
		parsed, err := time.ParseDuration(d)
		if err != nil {
			return 0, fmt.Errorf("%s: invalid duration %q: %w", path, d, err)
		}
		return parsed, nil
	default:
		secs, err := toFloat64(v, path)
		if err != nil {
			return 0, fmt.Errorf("%s: Duration expects a duration, duration string, or seconds number: %w", path, err)
		}
		return time.Duration(secs * float64(time.Second)), nil
	}
}

func splitFloat(f float64) (whole int64, frac int64) {
	whole = int64(f)
	frac = int64((f - float64(whole)) * 1e9)
	return whole, frac
}
