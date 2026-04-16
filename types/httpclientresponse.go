package types

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"

	richcty "github.com/tsarna/rich-cty-types"
	urlcty "github.com/tsarna/url-cty-funcs"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// HTTPClientResponseWrapper wraps a *http.Response received by an HTTP client
// call, tracking extra state that isn't part of the standard response struct.
//
// The Body field of the embedded *http.Response may be consumed lazily by the
// body* get accessors. If the caller passed opts.as to the verb function, the
// body is fully buffered into BufferedBody at request time and the body*
// accessors re-serve from that buffer (and may be called more than once).
type HTTPClientResponseWrapper struct {
	// R is the underlying *http.Response. R.Body is the live stream unless
	// BufferedBody is set, in which case callers should read from BufferedBody
	// instead.
	R *http.Response

	// Redirected is true if at least one redirect was followed to reach the
	// final response.
	Redirected bool

	// BufferedBody, if non-nil, holds the fully-read response body. It is
	// populated when opts.as pre-decodes the body, or when a body* accessor
	// has already consumed R.Body. Once set, body* accessors read from this
	// slice instead of the live stream.
	BufferedBody []byte

	// PreDecodedBody is the cty value for the body, pre-decoded according to
	// opts.as. It is set to cty.NilVal when opts.as was not used or was
	// "none"; the materialized "body" attribute on the cty object reflects
	// this field (null when unset).
	PreDecodedBody cty.Value
}

// HTTPClientResponseCapsuleType is the cty capsule type for
// HTTPClientResponseWrapper values.
var HTTPClientResponseCapsuleType = cty.CapsuleWithOps("httpclientresponse", reflect.TypeOf(HTTPClientResponseWrapper{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		w := val.(*HTTPClientResponseWrapper)
		if w.R == nil {
			return "httpclientresponse(<nil>)"
		}
		return fmt.Sprintf("httpclientresponse(%d %s)", w.R.StatusCode, w.R.Status)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "httpclientresponse"
	},
})

// HTTPClientResponseObjectType is the static cty object type returned by
// BuildHTTPClientResponseObject.
var HTTPClientResponseObjectType = cty.Object(map[string]cty.Type{
	"status":         cty.Number,
	"status_text":    cty.String,
	"ok":             cty.Bool,
	"redirected":     cty.Bool,
	"final_url":      urlcty.URLObjectType,
	"proto":          cty.String,
	"headers":        cty.Map(cty.List(cty.String)),
	"content_length": cty.Number,
	"content_type":   cty.String,
	"body":           cty.DynamicPseudoType,
	"_capsule":       HTTPClientResponseCapsuleType,
})

// NewHTTPClientResponseCapsule wraps an HTTPClientResponseWrapper in a cty
// capsule value.
func NewHTTPClientResponseCapsule(w *HTTPClientResponseWrapper) cty.Value {
	return cty.CapsuleVal(HTTPClientResponseCapsuleType, w)
}

// BuildHTTPClientResponseObject builds a cty object value with the
// materialized static attributes of a response, plus a _capsule attribute
// holding the wrapper for interface dispatch (richcty.Gettable, richcty.Stringable, richcty.Lengthable).
func BuildHTTPClientResponseObject(w *HTTPClientResponseWrapper) cty.Value {
	r := w.R

	// Headers: http.Header -> cty.Map(cty.List(cty.String)).
	var headersVal cty.Value
	if len(r.Header) == 0 {
		headersVal = cty.MapValEmpty(cty.List(cty.String))
	} else {
		hAttrs := make(map[string]cty.Value, len(r.Header))
		for k, vs := range r.Header {
			listItems := make([]cty.Value, len(vs))
			for i, v := range vs {
				listItems[i] = cty.StringVal(v)
			}
			hAttrs[k] = cty.ListVal(listItems)
		}
		headersVal = cty.MapVal(hAttrs)
	}

	// content_type is stripped of parameters (charset, boundary, etc.).
	contentType := ""
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err == nil {
			contentType = mt
		} else {
			contentType = ct
		}
	}

	// final_url: URL of the final response after any redirects. r.Request is
	// the request that produced this response (after CheckRedirect dance).
	var finalURLVal cty.Value
	if r.Request != nil && r.Request.URL != nil {
		finalURLVal = urlcty.BuildURLObject(r.Request.URL)
	} else {
		finalURLVal = cty.NullVal(urlcty.URLObjectType)
	}

	// body: null unless opts.as pre-decoded the body.
	bodyVal := w.PreDecodedBody
	if bodyVal == cty.NilVal {
		bodyVal = cty.NullVal(cty.DynamicPseudoType)
	}

	ok := r.StatusCode >= 200 && r.StatusCode < 300

	return cty.ObjectVal(map[string]cty.Value{
		"status":         cty.NumberIntVal(int64(r.StatusCode)),
		"status_text":    cty.StringVal(r.Status),
		"ok":             cty.BoolVal(ok),
		"redirected":     cty.BoolVal(w.Redirected),
		"final_url":      finalURLVal,
		"proto":          cty.StringVal(r.Proto),
		"headers":        headersVal,
		"content_length": cty.NumberIntVal(r.ContentLength),
		"content_type":   cty.StringVal(contentType),
		"body":           bodyVal,
		"_capsule":       NewHTTPClientResponseCapsule(w),
	})
}

// GetHTTPClientResponseFromValue extracts an *HTTPClientResponseWrapper from
// an httpclientresponse capsule or an object with a _capsule attribute.
func GetHTTPClientResponseFromValue(val cty.Value) (*HTTPClientResponseWrapper, bool) {
	enc, err := richcty.GetCapsuleFromValue(val)
	if err != nil {
		return nil, false
	}
	w, ok := enc.(*HTTPClientResponseWrapper)
	return w, ok
}

// readBody returns the response body, consuming the live stream on first call
// and buffering it so subsequent calls to any body* accessor can re-serve it.
func (w *HTTPClientResponseWrapper) readBody() ([]byte, error) {
	if w.BufferedBody != nil {
		return w.BufferedBody, nil
	}
	if w.R == nil || w.R.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(w.R.Body)
	if err != nil {
		return nil, err
	}
	_ = w.R.Body.Close()
	// Replace R.Body with a re-readable buffer for any downstream code that
	// still reaches for it directly.
	w.R.Body = io.NopCloser(bytes.NewReader(data))
	w.BufferedBody = data
	return data, nil
}

// responseContentType returns the response's media type (without parameters),
// or the empty string if no Content-Type header is set.
func (w *HTTPClientResponseWrapper) responseContentType() string {
	if w.R == nil {
		return ""
	}
	ct := w.R.Header.Get("Content-Type")
	if ct == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		return mt
	}
	return ct
}

// Get implements richcty.Gettable, supporting dynamic field access on
// httpclientresponse values.
func (w *HTTPClientResponseWrapper) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("httpclientresponse get: field argument required")
	}
	if args[0].Type() != cty.String {
		return cty.NilVal, fmt.Errorf("httpclientresponse get: field argument must be a string")
	}

	field := args[0].AsString()

	requireKey := func() (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("httpclientresponse get: %q requires a key argument", field)
		}
		if args[1].Type() != cty.String {
			return "", fmt.Errorf("httpclientresponse get: %q key must be a string", field)
		}
		return args[1].AsString(), nil
	}

	switch field {
	case "body":
		data, err := w.readBody()
		if err != nil {
			return cty.NilVal, fmt.Errorf("httpclientresponse get body: %w", err)
		}
		return cty.StringVal(string(data)), nil

	case "body_bytes":
		data, err := w.readBody()
		if err != nil {
			return cty.NilVal, fmt.Errorf("httpclientresponse get body_bytes: %w", err)
		}
		return BuildBytesObject(data, w.responseContentType()), nil

	case "body_json":
		data, err := w.readBody()
		if err != nil {
			return cty.NilVal, fmt.Errorf("httpclientresponse get body_json: reading body: %w", err)
		}
		ty, err := ctyjson.ImpliedType(data)
		if err != nil {
			return cty.NilVal, fmt.Errorf("httpclientresponse get body_json: invalid JSON: %w", err)
		}
		val, err := ctyjson.Unmarshal(data, ty)
		if err != nil {
			return cty.NilVal, fmt.Errorf("httpclientresponse get body_json: %w", err)
		}
		return val, nil

	case "header":
		key, err := requireKey()
		if err != nil {
			return cty.NilVal, err
		}
		if w.R == nil {
			return cty.StringVal(""), nil
		}
		return cty.StringVal(w.R.Header.Get(key)), nil

	case "header_all":
		key, err := requireKey()
		if err != nil {
			return cty.NilVal, err
		}
		if w.R == nil {
			return cty.ListValEmpty(cty.String), nil
		}
		vals := w.R.Header.Values(key)
		if len(vals) == 0 {
			return cty.ListValEmpty(cty.String), nil
		}
		items := make([]cty.Value, len(vals))
		for i, v := range vals {
			items[i] = cty.StringVal(v)
		}
		return cty.ListVal(items), nil

	case "cookie":
		name, err := requireKey()
		if err != nil {
			return cty.NilVal, err
		}
		if w.R == nil {
			return cty.NilVal, fmt.Errorf("httpclientresponse get cookie: no response")
		}
		for _, c := range w.R.Cookies() {
			if c.Name == name {
				return convertCookieObject(c), nil
			}
		}
		return cty.NilVal, fmt.Errorf("httpclientresponse get cookie: no cookie named %q", name)

	case "cookies":
		if w.R == nil {
			return cty.ListValEmpty(CookieObjectType), nil
		}
		cookies := w.R.Cookies()
		if len(cookies) == 0 {
			return cty.ListValEmpty(CookieObjectType), nil
		}
		items := make([]cty.Value, len(cookies))
		for i, c := range cookies {
			items[i] = convertCookieObject(c)
		}
		return cty.ListVal(items), nil

	default:
		return cty.NilVal, fmt.Errorf(
			"httpclientresponse get: unknown field %q (valid: body, body_bytes, body_json, header, header_all, cookie, cookies)",
			field,
		)
	}
}

// ToString implements richcty.Stringable, returning the response body as a string.
// Convenience for small responses; for large or binary responses prefer
// get(r, "body_bytes").
func (w *HTTPClientResponseWrapper) ToString(_ context.Context) (string, error) {
	data, err := w.readBody()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Length implements richcty.Lengthable. Returns the buffered body length if the body
// has been read (e.g. via opts.as), otherwise falls back to R.ContentLength
// (which may be -1 if unknown).
func (w *HTTPClientResponseWrapper) Length(_ context.Context) (int64, error) {
	if w.BufferedBody != nil {
		return int64(len(w.BufferedBody)), nil
	}
	if w.R == nil {
		return 0, nil
	}
	return w.R.ContentLength, nil
}
