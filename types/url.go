package types

import (
	"context"
	"fmt"
	"net/url"
	"reflect"

	"github.com/zclconf/go-cty/cty"
)

// URLWrapper wraps a parsed *url.URL as a cty capsule.
type URLWrapper struct {
	U *url.URL
}

// URLCapsuleType is the cty capsule type for URLWrapper values.
var URLCapsuleType = cty.CapsuleWithOps("url", reflect.TypeOf(URLWrapper{}), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		w := val.(*URLWrapper)
		return fmt.Sprintf("url(%s)", w.U.String())
	},
	TypeGoString: func(_ reflect.Type) string {
		return "URL"
	},
})

// URLObjectType is the static cty object type returned by urlparse, urljoin, urljoinpath.
var URLObjectType = cty.Object(map[string]cty.Type{
	"scheme":       cty.String,
	"opaque":       cty.String,
	"username":     cty.String,
	"password":     cty.String,
	"password_set": cty.Bool,
	"host":         cty.String,
	"hostname":     cty.String,
	"port":         cty.String,
	"path":         cty.String,
	"raw_path":     cty.String,
	"raw_query":    cty.String,
	"query":        cty.Map(cty.List(cty.String)),
	"fragment":     cty.String,
	"raw_fragment": cty.String,
	"force_query":  cty.Bool,
	"omit_host":    cty.Bool,
	"_capsule":     URLCapsuleType,
})

// NewURLCapsule wraps a *url.URL in a cty capsule value.
func NewURLCapsule(u *url.URL) cty.Value {
	return cty.CapsuleVal(URLCapsuleType, &URLWrapper{U: u})
}

// GetURLFromCapsule extracts a *url.URL from a URL capsule value.
func GetURLFromCapsule(val cty.Value) (*url.URL, error) {
	if val.Type() != URLCapsuleType {
		return nil, fmt.Errorf("expected url capsule, got %s", val.Type().FriendlyName())
	}
	w, ok := val.EncapsulatedValue().(*URLWrapper)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not URLWrapper, got %T", val.EncapsulatedValue())
	}
	return w.U, nil
}

// GetURLFromValue extracts a *url.URL from a string, URL capsule, or URL object
// (with _capsule attribute). Strings are parsed with url.Parse.
func GetURLFromValue(val cty.Value) (*url.URL, error) {
	switch {
	case val.Type() == cty.String:
		u, err := url.Parse(val.AsString())
		if err != nil {
			return nil, fmt.Errorf("invalid URL %q: %w", val.AsString(), err)
		}
		return u, nil
	case val.Type() == URLCapsuleType:
		return GetURLFromCapsule(val)
	case val.Type().IsObjectType() && val.Type().HasAttribute("_capsule"):
		return GetURLFromCapsule(val.GetAttr("_capsule"))
	default:
		return nil, fmt.Errorf("expected string, url capsule, or url object, got %s", val.Type().FriendlyName())
	}
}

// BuildURLObject builds a cty object value with all URL fields materialized as
// attributes, plus a _capsule attribute holding the URL capsule.
func BuildURLObject(u *url.URL) cty.Value {
	username := ""
	password := ""
	passwordSet := false
	if u.User != nil {
		username = u.User.Username()
		password, passwordSet = u.User.Password()
	}

	// Build query map: map[string][]string -> cty.Map(cty.List(cty.String))
	var queryVal cty.Value
	queryMap := u.Query()
	if len(queryMap) == 0 {
		queryVal = cty.MapValEmpty(cty.List(cty.String))
	} else {
		qAttrs := make(map[string]cty.Value, len(queryMap))
		for k, vs := range queryMap {
			listItems := make([]cty.Value, len(vs))
			for i, v := range vs {
				listItems[i] = cty.StringVal(v)
			}
			qAttrs[k] = cty.ListVal(listItems)
		}
		queryVal = cty.MapVal(qAttrs)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"scheme":       cty.StringVal(u.Scheme),
		"opaque":       cty.StringVal(u.Opaque),
		"username":     cty.StringVal(username),
		"password":     cty.StringVal(password),
		"password_set": cty.BoolVal(passwordSet),
		"host":         cty.StringVal(u.Host),
		"hostname":     cty.StringVal(u.Hostname()),
		"port":         cty.StringVal(u.Port()),
		"path":         cty.StringVal(u.Path),
		"raw_path":     cty.StringVal(u.RawPath),
		"raw_query":    cty.StringVal(u.RawQuery),
		"query":        queryVal,
		"fragment":     cty.StringVal(u.Fragment),
		"raw_fragment": cty.StringVal(u.RawFragment),
		"force_query":  cty.BoolVal(u.ForceQuery),
		"omit_host":    cty.BoolVal(u.OmitHost),
		"_capsule":     NewURLCapsule(u),
	})
}

// ToString implements richcty.Stringable, returning the canonical URL string.
func (w *URLWrapper) ToString(_ context.Context) (string, error) {
	return w.U.String(), nil
}

// Get implements richcty.Gettable, supporting dynamic field access on URL values.
//
// get(u, "query_param", key): returns list(string) for the named query parameter.
func (w *URLWrapper) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("url get: field argument required")
	}
	if args[0].Type() != cty.String {
		return cty.NilVal, fmt.Errorf("url get: field argument must be a string")
	}
	switch args[0].AsString() {
	case "query_param":
		if len(args) < 2 {
			return cty.NilVal, fmt.Errorf("url get: query_param requires a key argument")
		}
		if args[1].Type() != cty.String {
			return cty.NilVal, fmt.Errorf("url get: query_param key must be a string")
		}
		key := args[1].AsString()
		vals := w.U.Query()[key]
		if len(vals) == 0 {
			return cty.ListValEmpty(cty.String), nil
		}
		items := make([]cty.Value, len(vals))
		for i, v := range vals {
			items[i] = cty.StringVal(v)
		}
		return cty.ListVal(items), nil
	default:
		return cty.NilVal, fmt.Errorf("url get: unknown field %q (valid: query_param)", args[0].AsString())
	}
}
