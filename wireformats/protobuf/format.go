package protobuf

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// mode selects the representation of messages as VCL values.
type mode int

const (
	// modeNative maps messages to rich VCL values: proto snake_case field
	// names, Time/Duration capsules, bytes objects, 64-bit ints as numbers.
	modeNative mode = iota
	// modeJSON maps messages to protojson-flavored JSON: camelCase field
	// names, RFC3339/duration strings, base64 bytes, 64-bit ints as strings.
	modeJSON
)

// decodeMode reads and validates the optional mode attribute.
func decodeMode(config *cfg.Config, content *hcl.BodyContent) (mode, hcl.Diagnostics) {
	attr, ok := content.Attributes["mode"]
	if !ok {
		return modeNative, nil
	}
	val, diags := attr.Expr.Value(config.EvalCtx())
	if diags.HasErrors() {
		return modeNative, diags
	}
	if val.IsNull() || val.Type() != cty.String {
		return modeNative, diagAt("Invalid mode", "mode must be a string", attr.Range)
	}
	switch val.AsString() {
	case "native":
		return modeNative, nil
	case "json":
		return modeJSON, nil
	default:
		return modeNative, diagAt("Invalid mode",
			fmt.Sprintf("mode must be \"native\" or \"json\", got %q", val.AsString()), attr.Range)
	}
}

// protoFormat is an immutable, concurrency-safe wire.WireFormat bound to one
// protobuf message type. It is wrapped by the client layer in a
// cfg.CtyWireFormat, which reduces cty<->native Go around it: our Serialize
// receives native Go (already reduced by go2cty2go.CtyToAny) and our
// Deserialize returns a value that cfg.CtyWireFormat then runs through
// go2cty2go.AnyToCty. In native mode we build the whole cty tree ourselves and
// return it as one cty.Value, which AnyToCty passes through unchanged.
type protoFormat struct {
	schema *schema
	md     protoreflect.MessageDescriptor
	mt     protoreflect.MessageType
	mode   mode
}

// Name reports the format identifier used in wire.DecodeError.Format, so an
// on_decode_error hook can distinguish message types.
func (f *protoFormat) Name() string {
	return "protobuf:" + string(f.md.FullName())
}

// Serialize converts a VCL value shaped like the message into protobuf binary.
//
// If v is a cty.Value (a direct caller, not the cfg.CtyWireFormat wrapper), we
// run the same go2cty2go.CtyToAny reduction the wrapper would, so both paths
// converge on a single native-Go encoder.
func (f *protoFormat) Serialize(v any) ([]byte, error) {
	if cv, ok := v.(cty.Value); ok {
		native, err := go2cty2go.CtyToAny(cv)
		if err != nil {
			return nil, err
		}
		v = native
	}

	switch f.mode {
	case modeJSON:
		return f.serializeJSON(v)
	default:
		return f.serializeNative(v)
	}
}

func (f *protoFormat) SerializeString(v any) (string, error) {
	b, err := f.Serialize(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Deserialize converts protobuf binary into a VCL value per the active mode.
func (f *protoFormat) Deserialize(b []byte) (any, error) {
	switch f.mode {
	case modeJSON:
		return f.deserializeJSON(b)
	default:
		return f.deserializeNative(b)
	}
}
