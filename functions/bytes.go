package functions

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("bytes", func(_ *cfg.Config) map[string]function.Function {
		return GetBytesFunctions()
	})
}

// GetBytesFunctions returns bytes-related cty functions.
func GetBytesFunctions() map[string]function.Function {
	return map[string]function.Function{
		"bytes":        makeBytesFunc(),
		"base64encode": makeBase64EncodeFunc(),
		"base64decode": makeBase64DecodeFunc(),
	}
}

// makeBytesFunc returns a function that creates a bytes object from a UTF-8 string
// or re-wraps an existing bytes value with a different content type.
//
//	bytes(str)               - bytes from UTF-8 string, no content type
//	bytes(str, content_type) - bytes from UTF-8 string with content type
//	bytes(b)                 - copy of bytes value (preserves content type)
//	bytes(b, content_type)   - copy of bytes value with overridden content type
func makeBytesFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Converts a UTF-8 string or existing bytes value to a bytes object, with an optional content type",
		Params: []function.Parameter{
			{Name: "value", Type: cty.DynamicPseudoType, Description: "UTF-8 string or bytes value"},
		},
		VarParam: &function.Parameter{
			Name:        "content_type",
			Type:        cty.String,
			Description: "Optional MIME/content type",
		},
		Type: function.StaticReturnType(types.BytesObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			val := args[0]
			var data []byte
			contentType := ""

			switch {
			case val.Type() == cty.String:
				data = []byte(val.AsString())
			default:
				b, err := types.GetBytesFromValue(val)
				if err != nil {
					return cty.NilVal, fmt.Errorf("bytes: expected string or bytes value, got %s", val.Type().FriendlyName())
				}
				data = b.Data
				contentType = b.ContentType
			}

			if len(args) > 1 {
				contentType = args[1].AsString()
			}
			return types.BuildBytesObject(data, contentType), nil
		},
	})
}

// makeBase64EncodeFunc returns a base64encode function that accepts either a string or bytes value.
func makeBase64EncodeFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Encodes a string or bytes value to a base64 string",
		Params: []function.Parameter{
			{Name: "value", Type: cty.DynamicPseudoType, Description: "String or bytes value to encode"},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			val := args[0]
			var data []byte
			switch {
			case val.Type() == cty.String:
				data = []byte(val.AsString())
			default:
				b, err := types.GetBytesFromValue(val)
				if err != nil {
					return cty.NilVal, fmt.Errorf("base64encode: expected string or bytes value, got %s", val.Type().FriendlyName())
				}
				data = b.Data
			}
			return cty.StringVal(base64.StdEncoding.EncodeToString(data)), nil
		},
	})
}

// makeBase64DecodeFunc returns a base64decode function.
//
// When called with one argument it returns a string, preserving backward
// compatibility with the stdlib version. When a second (content_type) argument
// is present — even if it is the empty string — it returns a bytes object.
//
//	base64decode(str)                - returns string (backward compatible)
//	base64decode(str, "")            - returns bytes object, no content type
//	base64decode(str, "image/png")   - returns bytes object with content type
func makeBase64DecodeFunc() function.Function {
	return function.New(&function.Spec{
		Description: "Decodes a base64 string. Returns a string (one arg) or a bytes object (two args).",
		Params: []function.Parameter{
			{Name: "str", Type: cty.String, Description: "Base64-encoded string to decode"},
		},
		VarParam: &function.Parameter{
			Name:        "content_type",
			Type:        cty.String,
			Description: "If provided, returns a bytes object with this content type (use \"\" for untyped bytes)",
		},
		Type: func(args []cty.Value) (cty.Type, error) {
			if len(args) > 1 {
				return types.BytesObjectType, nil
			}
			return cty.String, nil
		},
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			data, err := base64.StdEncoding.DecodeString(args[0].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("base64decode: invalid base64: %w", err)
			}
			if len(args) > 1 {
				return types.BuildBytesObject(data, args[1].AsString()), nil
			}
			return cty.StringVal(string(data)), nil
		},
	})
}

// MakeFileBytesFunc returns a filebytes function restricted to baseDir.
// filebytes(path) or filebytes(path, content_type)
func MakeFileBytesFunc(baseDir string) function.Function {
	return function.New(&function.Spec{
		Description: "Reads a file and returns its contents as a bytes object",
		Params: []function.Parameter{
			{Name: "path", Type: cty.String, Description: "Path to the file to read"},
		},
		VarParam: &function.Parameter{
			Name:        "content_type",
			Type:        cty.String,
			Description: "Optional MIME/content type",
		},
		Type: function.StaticReturnType(types.BytesObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			path := args[0].AsString()
			if !filepath.IsAbs(path) {
				path = filepath.Join(baseDir, path)
			}
			clean := filepath.Clean(path)
			rel, err := filepath.Rel(baseDir, clean)
			if err != nil || strings.HasPrefix(rel, "..") {
				return cty.NilVal, fmt.Errorf("filebytes: path %q is outside the allowed directory", args[0].AsString())
			}
			data, err := os.ReadFile(clean)
			if err != nil {
				return cty.NilVal, fmt.Errorf("filebytes: %w", err)
			}
			contentType := ""
			if len(args) > 1 {
				contentType = args[1].AsString()
			}
			return types.BuildBytesObject(data, contentType), nil
		},
	})
}
