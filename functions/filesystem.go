package functions

import (
	"github.com/hashicorp/go-cty-funcs/filesystem"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("filesystem", func(c *cfg.Config) map[string]function.Function {
		base := c.GetFeature("readfiles")
		if base == "" {
			return nil
		}
		return map[string]function.Function{
			"abspath":    filesystem.AbsPathFunc,
			"basename":   filesystem.BasenameFunc,
			"dirname":    filesystem.DirnameFunc,
			"file":       filesystem.MakeFileFunc(base, false),
			"fileexists": filesystem.MakeFileExistsFunc(base),
			"fileset":    filesystem.MakeFileSetFunc(base),
			"filebase64": filesystem.MakeFileFunc(base, true),
			"filebytes":  MakeFileBytesFunc(base),
			"pathexpand": filesystem.PathExpandFunc,
		}
	})
}
