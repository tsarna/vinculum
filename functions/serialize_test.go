package functions

import (
	"testing"

	bytescty "github.com/tsarna/bytes-cty-type"
	cfg "github.com/tsarna/vinculum/config"
	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
)

func TestSerialize_JSONString(t *testing.T) {
	fn := makeSerializeFunc()
	result, err := fn.Call([]cty.Value{
		cty.StringVal("json"),
		cty.StringVal("hello"),
	})
	if err != nil {
		t.Fatalf("serialize() error = %v", err)
	}
	b, bErr := bytescty.GetBytesFromValue(result)
	if bErr != nil {
		t.Fatalf("expected bytes result: %v", bErr)
	}
	if string(b.Data) != `"hello"` {
		t.Errorf("serialize() = %q, want %q", string(b.Data), `"hello"`)
	}
}

func TestSerialize_AutoObject(t *testing.T) {
	fn := makeSerializeFunc()
	obj := cty.ObjectVal(map[string]cty.Value{
		"key": cty.StringVal("val"),
	})
	result, err := fn.Call([]cty.Value{cty.StringVal("auto"), obj})
	if err != nil {
		t.Fatalf("serialize() error = %v", err)
	}
	b, _ := bytescty.GetBytesFromValue(result)
	if string(b.Data) != `{"key":"val"}` {
		t.Errorf("serialize() = %q, want %q", string(b.Data), `{"key":"val"}`)
	}
}

func TestSerialize_Capsule(t *testing.T) {
	fn := makeSerializeFunc()
	capsule := cfg.NewWireFormatCapsule(wire.JSON)
	result, err := fn.Call([]cty.Value{
		capsule,
		cty.NumberIntVal(42),
	})
	if err != nil {
		t.Fatalf("serialize() error = %v", err)
	}
	b, _ := bytescty.GetBytesFromValue(result)
	if string(b.Data) != "42" {
		t.Errorf("serialize() = %q, want %q", string(b.Data), "42")
	}
}

func TestSerialize_InvalidFormat(t *testing.T) {
	fn := makeSerializeFunc()
	_, err := fn.Call([]cty.Value{
		cty.StringVal("unknown"),
		cty.StringVal("x"),
	})
	if err == nil {
		t.Fatal("expected error for unknown wire format")
	}
}

func TestSerializeStr_JSON(t *testing.T) {
	fn := makeSerializeStrFunc()
	result, err := fn.Call([]cty.Value{
		cty.StringVal("json"),
		cty.ObjectVal(map[string]cty.Value{"a": cty.NumberIntVal(1)}),
	})
	if err != nil {
		t.Fatalf("serializestr() error = %v", err)
	}
	if result.AsString() != `{"a":1}` {
		t.Errorf("serializestr() = %q, want %q", result.AsString(), `{"a":1}`)
	}
}

func TestSerializeStr_Auto(t *testing.T) {
	fn := makeSerializeStrFunc()
	result, err := fn.Call([]cty.Value{
		cty.StringVal("auto"),
		cty.StringVal("plain"),
	})
	if err != nil {
		t.Fatalf("serializestr() error = %v", err)
	}
	if result.AsString() != "plain" {
		t.Errorf("serializestr() = %q, want %q", result.AsString(), "plain")
	}
}

func TestDeserialize_JSONString(t *testing.T) {
	fn := makeDeserializeFunc()
	result, err := fn.Call([]cty.Value{
		cty.StringVal("json"),
		cty.StringVal(`{"key":"val"}`),
	})
	if err != nil {
		t.Fatalf("deserialize() error = %v", err)
	}
	if !result.IsKnown() {
		t.Fatal("deserialize() returned unknown value")
	}
}

func TestDeserialize_AutoPlainText(t *testing.T) {
	fn := makeDeserializeFunc()
	result, err := fn.Call([]cty.Value{
		cty.StringVal("auto"),
		cty.StringVal("hello world"),
	})
	if err != nil {
		t.Fatalf("deserialize() error = %v", err)
	}
	if result.Type() != cty.String {
		t.Errorf("deserialize() type = %s, want string", result.Type().FriendlyName())
	}
	if result.AsString() != "hello world" {
		t.Errorf("deserialize() = %q, want %q", result.AsString(), "hello world")
	}
}

func TestDeserialize_Bytes(t *testing.T) {
	fn := makeDeserializeFunc()
	bytesVal := bytescty.BuildBytesObject([]byte(`{"n":99}`), "application/json")
	result, err := fn.Call([]cty.Value{
		cty.StringVal("json"),
		bytesVal,
	})
	if err != nil {
		t.Fatalf("deserialize() error = %v", err)
	}
	if !result.IsKnown() {
		t.Fatal("deserialize() returned unknown value")
	}
}

func TestDeserialize_InvalidFormat(t *testing.T) {
	fn := makeDeserializeFunc()
	_, err := fn.Call([]cty.Value{
		cty.StringVal("unknown"),
		cty.StringVal("x"),
	})
	if err == nil {
		t.Fatal("expected error for unknown wire format")
	}
}

func TestDeserialize_InvalidData(t *testing.T) {
	fn := makeDeserializeFunc()
	_, err := fn.Call([]cty.Value{
		cty.StringVal("json"),
		cty.NumberIntVal(42),
	})
	if err == nil {
		t.Fatal("expected error for non-string/non-bytes data")
	}
}
