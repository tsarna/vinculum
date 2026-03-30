package types

import (
	"context"
	"fmt"
	"reflect"

	"github.com/tsarna/vinculum/ctyutil"
	"github.com/zclconf/go-cty/cty"
)

// Bytes is an immutable byte slice with an optional content/MIME type.
type Bytes struct {
	Data        []byte
	ContentType string
}

// BytesCapsuleType is the cty capsule type for Bytes values.
var BytesCapsuleType = cty.CapsuleWithOps("bytes", reflect.TypeOf(Bytes{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		b := val.(*Bytes)
		return fmt.Sprintf("bytes(%d bytes)", len(b.Data))
	},
	TypeGoString: func(_ reflect.Type) string {
		return "Bytes"
	},
})

// BytesObjectType is the cty object type returned by bytes-producing functions.
// It exposes content_type as a direct attribute and carries the underlying capsule
// in the _capsule attribute for interface dispatch (Stringable, Lengthable, etc.).
var BytesObjectType = cty.Object(map[string]cty.Type{
	"content_type": cty.String,
	"_capsule":     BytesCapsuleType,
})

// NewBytesCapsule wraps a byte slice and optional content type in a cty capsule value.
func NewBytesCapsule(data []byte, contentType string) cty.Value {
	return cty.CapsuleVal(BytesCapsuleType, &Bytes{Data: data, ContentType: contentType})
}

// BuildBytesObject returns a cty object with content_type and _capsule attributes.
func BuildBytesObject(data []byte, contentType string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"content_type": cty.StringVal(contentType),
		"_capsule":     NewBytesCapsule(data, contentType),
	})
}

// GetBytesFromCapsule extracts a *Bytes from a cty capsule value.
func GetBytesFromCapsule(val cty.Value) (*Bytes, error) {
	if val.Type() != BytesCapsuleType {
		return nil, fmt.Errorf("expected bytes capsule, got %s", val.Type().FriendlyName())
	}
	b, ok := val.EncapsulatedValue().(*Bytes)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not Bytes, got %T", val.EncapsulatedValue())
	}
	return b, nil
}

// GetBytesFromValue extracts a *Bytes from a bytes object, capsule, or anything
// accepted by GetCapsuleFromValue.
func GetBytesFromValue(val cty.Value) (*Bytes, error) {
	enc, err := ctyutil.GetCapsuleFromValue(val)
	if err != nil {
		return nil, fmt.Errorf("expected bytes value: %w", err)
	}
	b, ok := enc.(*Bytes)
	if !ok {
		return nil, fmt.Errorf("expected bytes, got %T", enc)
	}
	return b, nil
}

// ToString implements Stringable, returning the bytes data as a UTF-8 string.
func (b *Bytes) ToString(_ context.Context) (string, error) {
	return string(b.Data), nil
}

// Length implements Lengthable, returning the byte length.
func (b *Bytes) Length(_ context.Context) (int64, error) {
	return int64(len(b.Data)), nil
}
