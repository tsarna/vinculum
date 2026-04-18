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
		Type: function.StaticReturnType(bytescty.BytesObjectType),
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
