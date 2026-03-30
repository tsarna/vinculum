package types

import (
	"context"
	"fmt"
	"reflect"

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

// NewBytesCapsule wraps a byte slice and optional content type in a cty capsule value.
func NewBytesCapsule(data []byte, contentType string) cty.Value {
	return cty.CapsuleVal(BytesCapsuleType, &Bytes{Data: data, ContentType: contentType})
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

// ToString implements Stringable, returning the bytes data as a UTF-8 string.
func (b *Bytes) ToString(_ context.Context) (string, error) {
	return string(b.Data), nil
}

// Length implements Lengthable, returning the byte length.
func (b *Bytes) Length(_ context.Context) (int64, error) {
	return int64(len(b.Data)), nil
}

// Get implements Gettable, allowing bytes values to be read via get().
//
// get(b, "content_type"): returns the content/MIME type string (may be empty).
func (b *Bytes) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("bytes get: field argument required (use tostring() for UTF-8 string, length() for byte count)")
	}
	if args[0].Type() != cty.String {
		return cty.NilVal, fmt.Errorf("bytes get: field argument must be a string")
	}
	switch args[0].AsString() {
	case "content_type":
		return cty.StringVal(b.ContentType), nil
	default:
		return cty.NilVal, fmt.Errorf("bytes get: unknown field %q (valid: content_type)", args[0].AsString())
	}
}
