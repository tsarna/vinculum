package functions

import (
	"fmt"
	"net/url"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("url", func(_ *cfg.Config) map[string]function.Function {
		return GetURLFunctions()
	})
}

func GetURLFunctions() map[string]function.Function {
	return map[string]function.Function{
		"urlparse":       makeURLParseFunc(),
		"urljoin":        makeURLJoinFunc(),
		"urljoinpath":    makeURLJoinPathFunc(),
		"urlqueryencode": makeURLQueryEncodeFunc(),
		"urlquerydecode": makeURLQueryDecodeFunc(),
		"urldecode":      makeURLDecodeFunc(),
	}
}

// makeURLParseFunc returns urlparse(rawURL) -> url object
func makeURLParseFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Parses a URL string into a URL object with named attributes",
		Params: []function.Parameter{
			{Name: "rawURL", Type: cty.String, Description: "The URL string to parse"},
		},
		Type: function.StaticReturnType(types.URLObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			u, err := url.Parse(args[0].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("urlparse: %w", err)
			}
			return types.BuildURLObject(u), nil
		},
	})
}

// makeURLJoinFunc returns urljoin(base, ref) -> url object
func makeURLJoinFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Resolves ref against base following RFC 3986",
		Params: []function.Parameter{
			{Name: "base", Type: cty.DynamicPseudoType, Description: "Base URL (string, url object, or url capsule)"},
			{Name: "ref", Type: cty.DynamicPseudoType, Description: "Reference URL to resolve (string, url object, or url capsule)"},
		},
		Type: function.StaticReturnType(types.URLObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			base, err := types.GetURLFromValue(args[0])
			if err != nil {
				return cty.NilVal, fmt.Errorf("urljoin: base: %w", err)
			}
			ref, err := types.GetURLFromValue(args[1])
			if err != nil {
				return cty.NilVal, fmt.Errorf("urljoin: ref: %w", err)
			}
			return types.BuildURLObject(base.ResolveReference(ref)), nil
		},
	})
}

// makeURLJoinPathFunc returns urljoinpath(base, elem...) -> url object
func makeURLJoinPathFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Joins path elements to base, each element is path-escaped",
		Params: []function.Parameter{
			{Name: "base", Type: cty.DynamicPseudoType, Description: "Base URL (string, url object, or url capsule)"},
		},
		VarParam: &function.Parameter{Name: "elem", Type: cty.String, Description: "Path elements to append"},
		Type:     function.StaticReturnType(types.URLObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			base, err := types.GetURLFromValue(args[0])
			if err != nil {
				return cty.NilVal, fmt.Errorf("urljoinpath: %w", err)
			}
			elems := make([]string, len(args)-1)
			for i, v := range args[1:] {
				elems[i] = v.AsString()
			}
			return types.BuildURLObject(base.JoinPath(elems...)), nil
		},
	})
}

// makeURLQueryEncodeFunc returns urlqueryencode(params) -> string
func makeURLQueryEncodeFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Encodes a map of query parameters into a URL-encoded query string",
		Params: []function.Parameter{
			{Name: "params", Type: cty.DynamicPseudoType, Description: "map(string) or map(list(string)) of query parameters"},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			val := args[0]
			if !val.Type().IsMapType() {
				return cty.NilVal, fmt.Errorf("urlqueryencode: expected map, got %s", val.Type().FriendlyName())
			}

			values := make(url.Values)
			for k, v := range val.AsValueMap() {
				switch {
				case v.Type() == cty.String:
					values[k] = []string{v.AsString()}
				case v.Type().IsListType() || v.Type().IsTupleType():
					var strs []string
					for it := v.ElementIterator(); it.Next(); {
						_, elem := it.Element()
						if elem.Type() != cty.String {
							return cty.NilVal, fmt.Errorf("urlqueryencode: map value list elements must be strings")
						}
						strs = append(strs, elem.AsString())
					}
					values[k] = strs
				default:
					return cty.NilVal, fmt.Errorf("urlqueryencode: map values must be strings or lists of strings, got %s", v.Type().FriendlyName())
				}
			}
			return cty.StringVal(values.Encode()), nil
		},
	})
}

// makeURLQueryDecodeFunc returns urlquerydecode(query) -> map(list(string))
func makeURLQueryDecodeFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Decodes a URL query string into a map of string lists",
		Params: []function.Parameter{
			{Name: "query", Type: cty.String, Description: "Query string to decode (leading ? is optional)"},
		},
		Type: function.StaticReturnType(cty.Map(cty.List(cty.String))),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			q := args[0].AsString()
			// Strip leading ?
			if len(q) > 0 && q[0] == '?' {
				q = q[1:]
			}
			parsed, err := url.ParseQuery(q)
			if err != nil {
				return cty.NilVal, fmt.Errorf("urlquerydecode: %w", err)
			}
			if len(parsed) == 0 {
				return cty.MapValEmpty(cty.List(cty.String)), nil
			}
			result := make(map[string]cty.Value, len(parsed))
			for k, vs := range parsed {
				items := make([]cty.Value, len(vs))
				for i, v := range vs {
					items[i] = cty.StringVal(v)
				}
				result[k] = cty.ListVal(items)
			}
			return cty.MapVal(result), nil
		},
	})
}

// makeURLDecodeFunc returns urldecode(str) -> string
func makeURLDecodeFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Decodes a percent-encoded string (inverse of urlencode); decodes + as space",
		Params: []function.Parameter{
			{Name: "str", Type: cty.String, Description: "Percent-encoded string to decode"},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			decoded, err := url.QueryUnescape(args[0].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("urldecode: %w", err)
			}
			return cty.StringVal(decoded), nil
		},
	})
}
