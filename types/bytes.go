package types

import (
	"context"
	"encoding/base64"
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

// Get implements Gettable, allowing bytes values to be read via get().
//
// With no args or get(b, "utf8"): returns the data as a UTF-8 string.
// get(b, "base64"): returns the data as a standard base64-encoded string.
// get(b, "len"): returns the byte length as a cty.Number.
// get(b, "content_type"): returns the content/MIME type string (may be empty).
func (b *Bytes) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	mode := "utf8"
	if len(args) > 0 {
		if args[0].Type() != cty.String {
			return cty.NilVal, fmt.Errorf("bytes get: mode argument must be a string")
		}
		mode = args[0].AsString()
	}
	switch mode {
	case "utf8", "string", "text":
		return cty.StringVal(string(b.Data)), nil
	case "base64":
		return cty.StringVal(base64.StdEncoding.EncodeToString(b.Data)), nil
	case "len", "length", "size":
		return cty.NumberIntVal(int64(len(b.Data))), nil
	case "content_type", "content-type", "mime", "mime_type":
		return cty.StringVal(b.ContentType), nil
	default:
		return cty.NilVal, fmt.Errorf("bytes get: unknown mode %q (valid: utf8, base64, len, content_type)", mode)
	}
}
