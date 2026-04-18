package types

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"

	bytescty "github.com/tsarna/bytes-cty-type"
	richcty "github.com/tsarna/rich-cty-types"
	timecty "github.com/tsarna/time-cty-funcs"
	urlcty "github.com/tsarna/url-cty-funcs"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// HTTPRequestWrapper wraps a *http.Request as a cty capsule.
type HTTPRequestWrapper struct {
	R *http.Request
}

// HTTPRequestCapsuleType is the cty capsule type for HTTPRequestWrapper values.
var HTTPRequestCapsuleType = cty.CapsuleWithOps("httprequest", reflect.TypeOf(HTTPRequestWrapper{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		w := val.(*HTTPRequestWrapper)
		return fmt.Sprintf("httprequest(%s %s)", w.R.Method, w.R.URL.String())
	},
	TypeGoString: func(_ reflect.Type) string {
		return "httprequest"
	},
})

// CookieObjectType is the static cty object type for cookie values.
var CookieObjectType = cty.Object(map[string]cty.Type{
	"name":        cty.String,
	"value":       cty.String,
	"quoted":      cty.Bool,
	"path":        cty.String,
	"domain":      cty.String,
	"expires":     timecty.TimeCapsuleType,
	"raw_expires": cty.String,
	"max_age":     cty.Number,
	"secure":      cty.Bool,
	"http_only":   cty.Bool,
	"same_site":   cty.String,
	"partitioned": cty.Bool,
	"raw":         cty.String,
})

// HTTPRequestObjectType is the static cty object type returned by BuildHTTPRequestObject.
var HTTPRequestObjectType = cty.Object(map[string]cty.Type{
	"method":       cty.String,
	"url":          urlcty.URLObjectType,
	"proto":        cty.String,
	"proto_major":  cty.Number,
	"proto_minor":  cty.Number,
	"host":         cty.String,
	"remote_addr":  cty.String,
	"user":         cty.String,
	"password":     cty.String,
	"password_set": cty.Bool,
	"headers":      cty.Map(cty.List(cty.String)),
	"_capsule":     HTTPRequestCapsuleType,
})

// NewHTTPRequestCapsule wraps a *http.Request in a cty capsule value.
func NewHTTPRequestCapsule(r *http.Request) cty.Value {
	return cty.CapsuleVal(HTTPRequestCapsuleType, &HTTPRequestWrapper{R: r})
}

// GetHTTPRequestFromValue extracts a *http.Request from an httprequest capsule or
// an object with a _capsule attribute.
func GetHTTPRequestFromValue(val cty.Value) (*http.Request, error) {
	enc, err := richcty.GetCapsuleFromValue(val)
	if err != nil {
		return nil, fmt.Errorf("expected httprequest value: %w", err)
	}
	w, ok := enc.(*HTTPRequestWrapper)
	if !ok {
		return nil, fmt.Errorf("expected httprequest, got %T", enc)
	}
	return w.R, nil
}

// BuildHTTPRequestObject builds a cty object value with all request fields
// materialized as attributes, plus a _capsule attribute holding the request capsule.
func BuildHTTPRequestObject(r *http.Request) cty.Value {
	// Build headers: map[string][]string -> cty.Map(cty.List(cty.String))
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

	// Basic auth
	user, password, passwordSet := "", "", false
	if u, p, ok := r.BasicAuth(); ok {
		user = u
		password = p
		passwordSet = p != "" || ok
	}

	return cty.ObjectVal(map[string]cty.Value{
		"method":       cty.StringVal(r.Method),
		"url":          urlcty.BuildURLObject(r.URL),
		"proto":        cty.StringVal(r.Proto),
		"proto_major":  cty.NumberIntVal(int64(r.ProtoMajor)),
		"proto_minor":  cty.NumberIntVal(int64(r.ProtoMinor)),
		"host":         cty.StringVal(r.Host),
		"remote_addr":  cty.StringVal(r.RemoteAddr),
		"user":         cty.StringVal(user),
		"password":     cty.StringVal(password),
		"password_set": cty.BoolVal(passwordSet),
		"headers":      headersVal,
		"_capsule":     NewHTTPRequestCapsule(r),
	})
}

// convertCookieObject converts an *http.Cookie to a cty object matching CookieObjectType.
func convertCookieObject(cookie *http.Cookie) cty.Value {
	var sameSiteStr string
	switch cookie.SameSite {
	case http.SameSiteLaxMode:
		sameSiteStr = "Lax"
	case http.SameSiteStrictMode:
		sameSiteStr = "Strict"
	case http.SameSiteNoneMode:
		sameSiteStr = "None"
	default:
		sameSiteStr = "Default"
	}

	var expiresVal cty.Value
	if cookie.Expires.IsZero() {
		expiresVal = cty.NullVal(timecty.TimeCapsuleType)
	} else {
		expiresVal = timecty.NewTimeCapsule(cookie.Expires)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"name":        cty.StringVal(cookie.Name),
		"value":       cty.StringVal(cookie.Value),
		"quoted":      cty.BoolVal(cookie.Quoted),
		"path":        cty.StringVal(cookie.Path),
		"domain":      cty.StringVal(cookie.Domain),
		"expires":     expiresVal,
		"raw_expires": cty.StringVal(cookie.RawExpires),
		"max_age":     cty.NumberIntVal(int64(cookie.MaxAge)),
		"secure":      cty.BoolVal(cookie.Secure),
		"http_only":   cty.BoolVal(cookie.HttpOnly),
		"same_site":   cty.StringVal(sameSiteStr),
		"partitioned": cty.BoolVal(cookie.Partitioned),
		"raw":         cty.StringVal(cookie.Raw),
	})
}

// Get implements richcty.Gettable, supporting dynamic field access on httprequest values.
func (w *HTTPRequestWrapper) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("httprequest get: field argument required")
	}
	if args[0].Type() != cty.String {
		return cty.NilVal, fmt.Errorf("httprequest get: field argument must be a string")
	}

	field := args[0].AsString()

	// helpers for requiring a string key arg
	requireKey := func() (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("httprequest get: %q requires a key argument", field)
		}
		if args[1].Type() != cty.String {
			return "", fmt.Errorf("httprequest get: %q key must be a string", field)
		}
		return args[1].AsString(), nil
	}

	switch field {
	case "body":
		data, err := io.ReadAll(w.R.Body)
		if err != nil {
			return cty.NilVal, fmt.Errorf("httprequest get body: %w", err)
		}
		return cty.StringVal(string(data)), nil

	case "body_bytes":
		data, err := io.ReadAll(w.R.Body)
		if err != nil {
			return cty.NilVal, fmt.Errorf("httprequest get body_bytes: %w", err)
		}
		mediaType := ""
		if ct := w.R.Header.Get("Content-Type"); ct != "" {
			if mt, _, err := mime.ParseMediaType(ct); err == nil {
				mediaType = mt
			}
		}
		return bytescty.BuildBytesObject(data, mediaType), nil

	case "body_json":
		data, err := io.ReadAll(w.R.Body)
		if err != nil {
			return cty.NilVal, fmt.Errorf("httprequest get body_json: reading body: %w", err)
		}
		ty, err := ctyjson.ImpliedType(data)
		if err != nil {
			return cty.NilVal, fmt.Errorf("httprequest get body_json: invalid JSON: %w", err)
		}
		val, err := ctyjson.Unmarshal(data, ty)
		if err != nil {
			return cty.NilVal, fmt.Errorf("httprequest get body_json: %w", err)
		}
		return val, nil

	case "header":
		key, err := requireKey()
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(w.R.Header.Get(key)), nil

	case "header_all":
		key, err := requireKey()
		if err != nil {
			return cty.NilVal, err
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
		cookie, err := w.R.Cookie(name)
		if err != nil {
			return cty.NilVal, err
		}
		return convertCookieObject(cookie), nil

	case "path_value":
		name, err := requireKey()
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(w.R.PathValue(name)), nil

	case "form_value":
		key, err := requireKey()
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(w.R.FormValue(key)), nil

	case "post_form_value":
		key, err := requireKey()
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(w.R.PostFormValue(key)), nil

	default:
		return cty.NilVal, fmt.Errorf(
			"httprequest get: unknown field %q (valid: body, body_bytes, body_json, header, header_all, cookie, path_value, form_value, post_form_value)",
			field,
		)
	}
}
