package functions

import (
	"fmt"

	bytescty "github.com/tsarna/bytes-cty-type"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("serialize", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"serialize":    makeSerializeFunc(),
			"serializestr": makeSerializeStrFunc(),
			"deserialize":  makeDeserializeFunc(),
		}
	})
}

// resolveWireFormat extracts a CtyWireFormat from the first argument,
// which may be a string (built-in name) or a wire_format capsule.
func resolveWireFormat(arg cty.Value) (*cfg.CtyWireFormat, error) {
	wf, err := cfg.GetWireFormatFromValue(arg)
	if err != nil {
		return nil, err
	}
	return &cfg.CtyWireFormat{Inner: wf}, nil
}

// makeSerializeFunc creates serialize(wire_format, value) -> bytes.
func makeSerializeFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Serializes a value using the given wire format, returning bytes",
		Params: []function.Parameter{
			{Name: "wire_format", Type: cty.DynamicPseudoType, Description: "Wire format name (string) or wire_format capsule"},
			{Name: "value", Type: cty.DynamicPseudoType, Description: "Value to serialize"},
		},
		Type: function.StaticReturnType(bytescty.BytesObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			wf, err := resolveWireFormat(args[0])
			if err != nil {
				return cty.NilVal, fmt.Errorf("serialize: %w", err)
			}
			b, err := wf.Serialize(args[1])
			if err != nil {
				return cty.NilVal, fmt.Errorf("serialize: %w", err)
			}
			return bytescty.BuildBytesObject(b, ""), nil
		},
	})
}

// makeSerializeStrFunc creates serializestr(wire_format, value) -> string.
func makeSerializeStrFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Serializes a value using the given wire format, returning a string",
		Params: []function.Parameter{
			{Name: "wire_format", Type: cty.DynamicPseudoType, Description: "Wire format name (string) or wire_format capsule"},
			{Name: "value", Type: cty.DynamicPseudoType, Description: "Value to serialize"},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			wf, err := resolveWireFormat(args[0])
			if err != nil {
				return cty.NilVal, fmt.Errorf("serializestr: %w", err)
			}
			s, err := wf.SerializeString(args[1])
			if err != nil {
				return cty.NilVal, fmt.Errorf("serializestr: %w", err)
			}
			return cty.StringVal(s), nil
		},
	})
}

// makeDeserializeFunc creates deserialize(wire_format, bytes_or_string) -> value.
func makeDeserializeFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Deserializes bytes or a string using the given wire format",
		Params: []function.Parameter{
			{Name: "wire_format", Type: cty.DynamicPseudoType, Description: "Wire format name (string) or wire_format capsule"},
			{Name: "data", Type: cty.DynamicPseudoType, Description: "Bytes or string to deserialize"},
		},
		Type: function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			wf, err := resolveWireFormat(args[0])
			if err != nil {
				return cty.NilVal, fmt.Errorf("deserialize: %w", err)
			}

			var raw []byte
			data := args[1]
			switch {
			case data.Type() == cty.String:
				raw = []byte(data.AsString())
			default:
				b, bErr := bytescty.GetBytesFromValue(data)
				if bErr != nil {
					return cty.NilVal, fmt.Errorf("deserialize: data must be a string or bytes value, got %s", data.Type().FriendlyName())
				}
				raw = b.Data
			}

			result, err := wf.Deserialize(raw)
			if err != nil {
				return cty.NilVal, fmt.Errorf("deserialize: %w", err)
			}
			cv, ok := result.(cty.Value)
			if !ok {
				return cty.NilVal, fmt.Errorf("deserialize: unexpected result type %T", result)
			}
			return cv, nil
		},
	})
}
