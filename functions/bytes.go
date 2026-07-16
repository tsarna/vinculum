package functions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bytescty "github.com/tsarna/bytes-cty-type"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("bytes", func(_ *cfg.Config) map[string]function.Function {
		return bytescty.GetBytesFunctions()
	})
	// The real signatures of those functions. Each takes a string *or* a bytes value,
	// and cty has no union type; bytes() and base64decode() also take an optional
	// trailing content type, which cty can only fake with a variadic — and for
	// base64decode that argument decides the return type, so from cty metadata alone
	// its signature is anyone's guess.
	cfg.RegisterFunctyExterns(bytescty.ExternsFilename, bytescty.Externs())
}

// MakeFileBytesFunc returns a filebytes function restricted to baseDir.
// filebytes(path) or filebytes(path, content_type)
func MakeFileBytesFunc(baseDir string) function.Function {
	return function.New(&function.Spec{
		Description: "Reads a file and returns its contents as a bytes object",
		Params: []function.Parameter{
			{Name: "path", Type: cty.String, Description: "Path to the file to read"},
		},
		// The variadic is how cty fakes an *optional* content type — the only way it
		// offers — so it has no upper bound of its own, and the Impl below reads only
		// the first. Without the ceiling declared in Type, the rest are dropped in
		// silence.
		VarParam: &function.Parameter{
			Name:        "content_type",
			Type:        cty.String,
			Description: "Optional MIME/content type (at most one)",
		},
		Type: func(args []cty.Value) (cty.Type, error) {
			if len(args) > 2 {
				return cty.NilType, fmt.Errorf("filebytes() takes at most 2 arguments, got %d", len(args))
			}
			return bytescty.BytesObjectType, nil
		},
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
			return bytescty.BuildBytesObject(data, contentType), nil
		},
	})
}
