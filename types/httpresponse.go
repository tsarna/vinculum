package types

import (
	"fmt"
	"net/http"
	"reflect"

	richcty "github.com/tsarna/rich-cty-types"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// HTTPResponseWrapper holds the data for an HTTP response being built by an action expression.
type HTTPResponseWrapper struct {
	Status      int
	Headers     http.Header
	Body        []byte
	ContentType string
	IsError     bool
}

// HTTPResponseCapsuleType is the cty capsule type for HTTPResponseWrapper values.
var HTTPResponseCapsuleType = cty.CapsuleWithOps("httpresponse", reflect.TypeOf(HTTPResponseWrapper{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		r := val.(*HTTPResponseWrapper)
		return fmt.Sprintf("httpresponse(%d)", r.Status)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "httpresponse"
	},
})

// HTTPResponseObjectType is the static cty object type returned by BuildHTTPResponseObject.
var HTTPResponseObjectType = cty.Object(map[string]cty.Type{
	"status":   cty.Number,
	"headers":  cty.Map(cty.List(cty.String)),
	"_capsule": HTTPResponseCapsuleType,
})

// NewHTTPResponseCapsule wraps an HTTPResponseWrapper in a cty capsule value.
func NewHTTPResponseCapsule(r *HTTPResponseWrapper) cty.Value {
	return cty.CapsuleVal(HTTPResponseCapsuleType, r)
}

// BuildHTTPResponseObject builds a cty object value with status and headers materialized
// as attributes, plus a _capsule attribute holding the response capsule.
func BuildHTTPResponseObject(r *HTTPResponseWrapper) cty.Value {
	var headersVal cty.Value
	if len(r.Headers) == 0 {
		headersVal = cty.MapValEmpty(cty.List(cty.String))
	} else {
		headersMap := make(map[string]cty.Value, len(r.Headers))
		for name, vals := range r.Headers {
			listVals := make([]cty.Value, len(vals))
			for i, v := range vals {
				listVals[i] = cty.StringVal(v)
			}
			headersMap[name] = cty.ListVal(listVals)
		}
		headersVal = cty.MapVal(headersMap)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"status":   cty.NumberIntVal(int64(r.Status)),
		"headers":  headersVal,
		"_capsule": NewHTTPResponseCapsule(r),
	})
}

// GetHTTPResponseFromValue extracts an *HTTPResponseWrapper from an httpresponse capsule
// or an object with a _capsule attribute.
func GetHTTPResponseFromValue(val cty.Value) (*HTTPResponseWrapper, bool) {
	enc, err := richcty.GetCapsuleFromValue(val)
	if err != nil {
		return nil, false
	}
	r, ok := enc.(*HTTPResponseWrapper)
	return r, ok
}

// CoerceBodyToBytes converts a cty value to a byte slice and content-type string
// suitable for use as an HTTP response body. It is shared between the http_response()
// constructor and the automatic type-coercion path in writeHTTPResponse.
//
// Coercion rules:
//   - null    → (nil, "", nil)
//   - string  → (utf-8 bytes, "text/plain; charset=utf-8", nil)
//   - bytes   → (raw bytes, content_type from object or "application/octet-stream", nil)
//   - other   → JSON-encoded via ctyjson, "application/json"
func CoerceBodyToBytes(val cty.Value) ([]byte, string, error) {
	if val.IsNull() {
		return nil, "", nil
	}

	switch {
	case val.Type() == cty.String:
		return []byte(val.AsString()), "text/plain; charset=utf-8", nil

	case val.Type().IsObjectType() || val.Type().IsCapsuleType():
		if b, err := GetBytesFromValue(val); err == nil {
			ct := b.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			return b.Data, ct, nil
		}
		// Not a bytes object — fall through to JSON
		body, err := ctyjson.Marshal(val, val.Type())
		if err != nil {
			return nil, "", fmt.Errorf("failed to marshal value as JSON: %w", err)
		}
		return body, "application/json", nil

	default:
		body, err := ctyjson.Marshal(val, val.Type())
		if err != nil {
			return nil, "", fmt.Errorf("failed to marshal value as JSON: %w", err)
		}
		return body, "application/json", nil
	}
}
