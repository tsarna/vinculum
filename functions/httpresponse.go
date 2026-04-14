package functions

import (
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	timecty "github.com/tsarna/time-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("httpresponse", func(_ *cfg.Config) map[string]function.Function {
		return GetHTTPResponseFunctions()
	})
}

// GetHTTPResponseFunctions returns all HTTP response functions for global registration.
func GetHTTPResponseFunctions() map[string]function.Function {
	return map[string]function.Function{
		"http_response": HTTPResponseFunc,
		"http_redirect": HTTPRedirectFunc,
		"http_error":    HTTPErrorFunc,
		"addheader":     AddHeaderFunc,
		"removeheader":  RemoveHeaderFunc,
		"setcookie":     SetCookieFunc,
	}
}

// HTTPResponseFunc implements http_response(status[, body[, headers]]).
var HTTPResponseFunc = function.New(&function.Spec{
	Description: "Builds an HTTP response value with the given status code, optional body, and optional headers",
	Params: []function.Parameter{
		{Name: "status", Type: cty.Number},
	},
	VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
	Type:     function.StaticReturnType(types.HTTPResponseObjectType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		status, _ := args[0].AsBigFloat().Int64()
		r := &types.HTTPResponseWrapper{
			Status:  int(status),
			Headers: make(http.Header),
		}

		if len(args) > 1 {
			body, ct, err := types.CoerceBodyToBytes(args[1])
			if err != nil {
				return cty.NilVal, fmt.Errorf("http_response: invalid body: %w", err)
			}
			r.Body = body
			r.ContentType = ct
		}

		if len(args) > 2 {
			if err := applyHeadersArg(r.Headers, args[2]); err != nil {
				return cty.NilVal, fmt.Errorf("http_response: invalid headers: %w", err)
			}
		}

		return types.BuildHTTPResponseObject(r), nil
	},
})

// HTTPRedirectFunc implements http_redirect(url) and http_redirect(status, url).
// When the first argument is a string it is treated as the URL with a default 302 status.
// When the first argument is a number it is treated as the status code and the second
// argument must be the URL string.
var HTTPRedirectFunc = function.New(&function.Spec{
	Description: "Builds an HTTP redirect response; http_redirect(url) defaults to 302, http_redirect(status, url) uses the given status",
	Params: []function.Parameter{
		{Name: "first", Type: cty.DynamicPseudoType},
	},
	VarParam: &function.Parameter{Name: "rest", Type: cty.DynamicPseudoType},
	Type:     function.StaticReturnType(types.HTTPResponseObjectType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		var status int
		var url string

		switch {
		case args[0].Type() == cty.String:
			if len(args) != 1 {
				return cty.NilVal, fmt.Errorf("http_redirect: url-only form takes exactly 1 argument")
			}
			status = http.StatusFound
			url = args[0].AsString()

		case args[0].Type() == cty.Number:
			if len(args) != 2 {
				return cty.NilVal, fmt.Errorf("http_redirect: status+url form takes exactly 2 arguments")
			}
			if args[1].Type() != cty.String {
				return cty.NilVal, fmt.Errorf("http_redirect: second argument must be a string URL, got %s", args[1].Type().FriendlyName())
			}
			s, _ := args[0].AsBigFloat().Int64()
			status = int(s)
			url = args[1].AsString()

		default:
			return cty.NilVal, fmt.Errorf("http_redirect: first argument must be a URL string or status number, got %s", args[0].Type().FriendlyName())
		}

		r := &types.HTTPResponseWrapper{
			Status:  status,
			Headers: http.Header{"Location": {url}},
		}
		return types.BuildHTTPResponseObject(r), nil
	},
})

// HTTPErrorFunc implements http_error(status, message).
// Returns an httpresponse value marked as an error with the given status and plain-text body.
var HTTPErrorFunc = function.New(&function.Spec{
	Description: "Builds an HTTP error response with the given status code and message body",
	Params: []function.Parameter{
		{Name: "status", Type: cty.Number},
		{Name: "message", Type: cty.String},
	},
	Type: function.StaticReturnType(types.HTTPResponseObjectType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		status, _ := args[0].AsBigFloat().Int64()
		r := &types.HTTPResponseWrapper{
			Status:      int(status),
			Headers:     make(http.Header),
			Body:        []byte(args[1].AsString()),
			ContentType: "text/plain; charset=utf-8",
			IsError:     true,
		}
		return types.BuildHTTPResponseObject(r), nil
	},
})

// AddHeaderFunc implements addheader(response, name, value).
// Returns a new httpresponse with the given header appended (multi-value safe).
var AddHeaderFunc = function.New(&function.Spec{
	Description: "Returns a new response with the given header value appended",
	Params: []function.Parameter{
		{Name: "response", Type: cty.DynamicPseudoType},
		{Name: "name", Type: cty.String},
		{Name: "value", Type: cty.String},
	},
	Type: function.StaticReturnType(types.HTTPResponseObjectType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		orig, ok := types.GetHTTPResponseFromValue(args[0])
		if !ok {
			return cty.NilVal, fmt.Errorf("addheader: first argument must be an httpresponse value")
		}
		r := cloneResponse(orig)
		r.Headers.Add(textproto.CanonicalMIMEHeaderKey(args[1].AsString()), args[2].AsString())
		return types.BuildHTTPResponseObject(r), nil
	},
})

// RemoveHeaderFunc implements removeheader(response, name).
// Returns a new httpresponse with all values for the given header removed.
var RemoveHeaderFunc = function.New(&function.Spec{
	Description: "Returns a new response with all values for the given header removed",
	Params: []function.Parameter{
		{Name: "response", Type: cty.DynamicPseudoType},
		{Name: "name", Type: cty.String},
	},
	Type: function.StaticReturnType(types.HTTPResponseObjectType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		orig, ok := types.GetHTTPResponseFromValue(args[0])
		if !ok {
			return cty.NilVal, fmt.Errorf("removeheader: first argument must be an httpresponse value")
		}
		r := cloneResponse(orig)
		r.Headers.Del(args[1].AsString())
		return types.BuildHTTPResponseObject(r), nil
	},
})

// SetCookieFunc implements setcookie(cookieObj).
// Formats a Set-Cookie header value from an object with cookie fields.
// The name and value fields are required; all others are optional.
var SetCookieFunc = function.New(&function.Spec{
	Description: "Formats a Set-Cookie header value from a cookie definition object",
	Params: []function.Parameter{
		{Name: "cookie", Type: cty.DynamicPseudoType},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		val := args[0]
		if !val.Type().IsObjectType() {
			return cty.NilVal, fmt.Errorf("setcookie: argument must be an object, got %s", val.Type().FriendlyName())
		}

		if !val.Type().HasAttribute("name") || !val.Type().HasAttribute("value") {
			return cty.NilVal, fmt.Errorf("setcookie: object must have 'name' and 'value' attributes")
		}

		c := &http.Cookie{
			Name:  val.GetAttr("name").AsString(),
			Value: val.GetAttr("value").AsString(),
		}

		if val.Type().HasAttribute("path") {
			c.Path = val.GetAttr("path").AsString()
		}
		if val.Type().HasAttribute("domain") {
			c.Domain = val.GetAttr("domain").AsString()
		}
		if val.Type().HasAttribute("expires") {
			exp := val.GetAttr("expires")
			switch {
			case exp.Type() == timecty.TimeCapsuleType:
				t, _ := timecty.GetTime(exp)
				c.Expires = t
			case exp.Type() == timecty.DurationCapsuleType:
				d, _ := timecty.GetDuration(exp)
				c.Expires = time.Now().Add(d)
			case exp.Type() == cty.String:
				s := exp.AsString()
				if s != "" {
					t, err := time.Parse(time.RFC3339, s)
					if err != nil {
						return cty.NilVal, fmt.Errorf("setcookie: invalid expires value %q: %w", s, err)
					}
					c.Expires = t
				}
			default:
				return cty.NilVal, fmt.Errorf("setcookie: expires must be a time, duration, or RFC3339 string, got %s", exp.Type().FriendlyName())
			}
		}
		if val.Type().HasAttribute("max_age") {
			f, _ := val.GetAttr("max_age").AsBigFloat().Int64()
			c.MaxAge = int(f)
		}
		if val.Type().HasAttribute("secure") {
			c.Secure = val.GetAttr("secure").True()
		}
		if val.Type().HasAttribute("http_only") {
			c.HttpOnly = val.GetAttr("http_only").True()
		}
		if val.Type().HasAttribute("same_site") {
			switch strings.ToLower(val.GetAttr("same_site").AsString()) {
			case "strict":
				c.SameSite = http.SameSiteStrictMode
			case "lax":
				c.SameSite = http.SameSiteLaxMode
			case "none":
				c.SameSite = http.SameSiteNoneMode
			default:
				c.SameSite = http.SameSiteDefaultMode
			}
		}
		if val.Type().HasAttribute("partitioned") {
			c.Partitioned = val.GetAttr("partitioned").True()
		}

		return cty.StringVal(c.String()), nil
	},
})

// cloneResponse returns a shallow copy of the response with a cloned Headers map.
func cloneResponse(orig *types.HTTPResponseWrapper) *types.HTTPResponseWrapper {
	r := *orig
	r.Headers = orig.Headers.Clone()
	return &r
}

// applyHeadersArg copies header values from a cty map or object into h.
// Each value may be a string (single value) or a list/tuple of strings (multi-value).
func applyHeadersArg(h http.Header, val cty.Value) error {
	if val.IsNull() {
		return nil
	}
	if !val.Type().IsMapType() && !val.Type().IsObjectType() {
		return fmt.Errorf("headers must be a map or object, got %s", val.Type().FriendlyName())
	}
	for name, elem := range val.AsValueMap() {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		switch {
		case elem.Type() == cty.String:
			h.Add(canonical, elem.AsString())
		case elem.Type().IsListType() || elem.Type().IsTupleType():
			for it := elem.ElementIterator(); it.Next(); {
				_, v := it.Element()
				h.Add(canonical, v.AsString())
			}
		default:
			return fmt.Errorf("header %q value must be a string or list of strings, got %s", name, elem.Type().FriendlyName())
		}
	}
	return nil
}
