package config

import (
	"testing"

	wire "github.com/tsarna/vinculum-wire"
	"github.com/zclconf/go-cty/cty"
)

func TestCtyWireFormat_SerializeCtyValue(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.Auto}

	val := cty.ObjectVal(map[string]cty.Value{
		"count": cty.NumberIntVal(42),
	})
	b, err := wf.Serialize(val)
	if err != nil {
		t.Fatalf("Serialize() error = %v", err)
	}
	if string(b) != `{"count":42}` {
		t.Errorf("Serialize() = %q, want %q", string(b), `{"count":42}`)
	}
}

func TestCtyWireFormat_SerializeCtyString(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.Auto}

	b, err := wf.Serialize(cty.StringVal("hello"))
	if err != nil {
		t.Fatalf("Serialize() error = %v", err)
	}
	// auto format passes strings through verbatim
	if string(b) != "hello" {
		t.Errorf("Serialize() = %q, want %q", string(b), "hello")
	}
}

func TestCtyWireFormat_SerializeNonCty(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.Auto}

	b, err := wf.Serialize(map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("Serialize() error = %v", err)
	}
	if string(b) != `{"a":1}` {
		t.Errorf("Serialize() = %q, want %q", string(b), `{"a":1}`)
	}
}

func TestCtyWireFormat_SerializeString(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.JSON}

	s, err := wf.SerializeString(cty.NumberIntVal(99))
	if err != nil {
		t.Fatalf("SerializeString() error = %v", err)
	}
	if s != "99" {
		t.Errorf("SerializeString() = %q, want %q", s, "99")
	}
}

func TestCtyWireFormat_DeserializeReturnsCtyCty(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.Auto}

	result, err := wf.Deserialize([]byte(`{"key":"val"}`))
	if err != nil {
		t.Fatalf("Deserialize() error = %v", err)
	}
	cv, ok := result.(cty.Value)
	if !ok {
		t.Fatalf("Deserialize() returned %T, want cty.Value", result)
	}
	// go2cty2go.AnyToCty converts map[string]any with homogeneous values to a cty map
	if !cv.Type().IsMapType() {
		t.Errorf("Deserialize() type = %s, want map type", cv.Type().FriendlyName())
	}
	v := cv.Index(cty.StringVal("key"))
	if v.AsString() != "val" {
		t.Errorf("Deserialize()[\"key\"] = %q, want %q", v.AsString(), "val")
	}
}

func TestCtyWireFormat_DeserializeString(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.Auto}

	result, err := wf.Deserialize([]byte("plain text"))
	if err != nil {
		t.Fatalf("Deserialize() error = %v", err)
	}
	cv, ok := result.(cty.Value)
	if !ok {
		t.Fatalf("Deserialize() returned %T, want cty.Value", result)
	}
	if cv.Type() != cty.String {
		t.Errorf("Deserialize() type = %s, want string", cv.Type().FriendlyName())
	}
	if cv.AsString() != "plain text" {
		t.Errorf("Deserialize() = %q, want %q", cv.AsString(), "plain text")
	}
}

func TestCtyWireFormat_Name(t *testing.T) {
	wf := &CtyWireFormat{Inner: wire.JSON}
	if wf.Name() != "json" {
		t.Errorf("Name() = %q, want %q", wf.Name(), "json")
	}
}

func TestGetWireFormatFromValue_String(t *testing.T) {
	wf, err := GetWireFormatFromValue(cty.StringVal("json"))
	if err != nil {
		t.Fatalf("GetWireFormatFromValue() error = %v", err)
	}
	if wf.Name() != "json" {
		t.Errorf("got %q, want %q", wf.Name(), "json")
	}
}

func TestGetWireFormatFromValue_UnknownString(t *testing.T) {
	_, err := GetWireFormatFromValue(cty.StringVal("unknown"))
	if err == nil {
		t.Fatal("expected error for unknown wire format name")
	}
}

func TestGetWireFormatFromValue_Capsule(t *testing.T) {
	capsule := NewWireFormatCapsule(wire.Bytes)
	wf, err := GetWireFormatFromValue(capsule)
	if err != nil {
		t.Fatalf("GetWireFormatFromValue() error = %v", err)
	}
	if wf.Name() != "bytes" {
		t.Errorf("got %q, want %q", wf.Name(), "bytes")
	}
}

func TestGetWireFormatFromValue_InvalidType(t *testing.T) {
	_, err := GetWireFormatFromValue(cty.NumberIntVal(42))
	if err == nil {
		t.Fatal("expected error for non-string, non-capsule value")
	}
}

func TestNewWireFormatCapsule_RoundTrip(t *testing.T) {
	capsule := NewWireFormatCapsule(wire.String)
	if capsule.Type() != WireFormatCapsuleType {
		t.Fatalf("capsule type = %s, want wire_format", capsule.Type().FriendlyName())
	}
	wf, err := GetWireFormatFromValue(capsule)
	if err != nil {
		t.Fatalf("GetWireFormatFromValue() error = %v", err)
	}
	if wf.Name() != "string" {
		t.Errorf("got %q, want %q", wf.Name(), "string")
	}
}
