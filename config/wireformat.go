package config

import (
	"fmt"
	"reflect"

	bytescty "github.com/tsarna/bytes-cty-type"
	"github.com/tsarna/go2cty2go"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
)

// CtyWireFormat wraps a WireFormat with cty conversion.
//
// On the serialize side, if the input is a cty.Value it is converted
// to native Go types via go2cty2go.CtyToAny before delegating; any
// other type passes straight through to the inner WireFormat.
//
// On the deserialize side, the inner WireFormat's native Go output is
// converted to a cty.Value via go2cty2go.AnyToCty — except for []byte,
// which becomes a rich bytes object (see below).
//
// CtyWireFormat itself satisfies the WireFormat interface (Serialize
// takes any, Deserialize returns any), so it composes transparently.
type CtyWireFormat struct {
	Inner wire.WireFormat
}

// DefaultBytesContentType labels bytes values produced by a wire format
// that decoded an opaque payload. The wire formats carry no content-type
// information, so the generic binary type is the honest answer.
const DefaultBytesContentType = "application/octet-stream"

func (f *CtyWireFormat) Serialize(v any) ([]byte, error) {
	if cv, ok := v.(cty.Value); ok {
		if raw, ok := rawBytesFromCty(cv); ok {
			return f.Inner.Serialize(raw)
		}
		native, err := go2cty2go.CtyToAny(cv)
		if err != nil {
			return nil, err
		}
		return f.Inner.Serialize(native)
	}
	return f.Inner.Serialize(v)
}

func (f *CtyWireFormat) SerializeString(v any) (string, error) {
	if cv, ok := v.(cty.Value); ok {
		if raw, ok := rawBytesFromCty(cv); ok {
			return f.Inner.SerializeString(raw)
		}
		native, err := go2cty2go.CtyToAny(cv)
		if err != nil {
			return "", err
		}
		return f.Inner.SerializeString(native)
	}
	return f.Inner.SerializeString(v)
}

func (f *CtyWireFormat) Deserialize(b []byte) (any, error) {
	native, err := f.Inner.Deserialize(b)
	if err != nil {
		return nil, err
	}
	// go2cty2go has no []byte case, so it would render binary as a cty
	// string. That round-trips without loss but is the wrong type: VCL
	// would see text where the format deliberately produced bytes. Map it
	// to the rich bytes object instead, so tostring(), length(), and the
	// bytes functions dispatch correctly.
	if raw, ok := native.([]byte); ok {
		return bytescty.BuildBytesObject(raw, DefaultBytesContentType), nil
	}
	return go2cty2go.AnyToCty(native)
}

// rawBytesFromCty extracts the underlying bytes from a bytes object or
// capsule, reporting false for any other value. It lets a payload decoded
// as bytes be sent back out unchanged rather than being flattened into
// the object's attributes by CtyToAny.
func rawBytesFromCty(cv cty.Value) ([]byte, bool) {
	if cv.IsNull() || !cv.IsKnown() {
		return nil, false
	}
	b, err := bytescty.GetBytesFromValue(cv)
	if err != nil {
		return nil, false
	}
	return b.Data, true
}

func (f *CtyWireFormat) Name() string {
	return f.Inner.Name()
}

// wireFormatHolder wraps a wire.WireFormat for use as a cty capsule value.
// cty capsules require a concrete struct type, not an interface.
type wireFormatHolder struct {
	WireFormat wire.WireFormat
}

// WireFormatCapsuleType is the cty capsule type for wire.WireFormat values.
// Used to pass wire formats as cty values in VCL expressions (e.g.
// wire_format.myproto).
var WireFormatCapsuleType = cty.CapsuleWithOps("wire_format", reflect.TypeOf(wireFormatHolder{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		h := val.(*wireFormatHolder)
		return fmt.Sprintf("wire_format(%s)", h.WireFormat.Name())
	},
	TypeGoString: func(_ reflect.Type) string {
		return "wire_format"
	},
})

// NewWireFormatCapsule wraps a wire.WireFormat in a cty capsule value.
func NewWireFormatCapsule(wf wire.WireFormat) cty.Value {
	return cty.CapsuleVal(WireFormatCapsuleType, &wireFormatHolder{WireFormat: wf})
}

// GetWireFormatFromValue extracts a wire.WireFormat from a cty value.
// The value can be:
//   - A string naming a built-in wire format (resolved via wire.ByName)
//   - A capsule wrapping a wire.WireFormat
func GetWireFormatFromValue(val cty.Value) (wire.WireFormat, error) {
	if val.Type() == cty.String {
		name := val.AsString()
		wf := wire.ByName(name)
		if wf == nil {
			return nil, fmt.Errorf("unknown wire format %q", name)
		}
		return wf, nil
	}

	if val.Type() == WireFormatCapsuleType {
		h, ok := val.EncapsulatedValue().(*wireFormatHolder)
		if !ok {
			return nil, fmt.Errorf("wire_format capsule does not contain a WireFormat")
		}
		return h.WireFormat, nil
	}

	return nil, fmt.Errorf("wire_format must be a string or wire_format capsule, got %s", val.Type().FriendlyName())
}
