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
		// Only the functions that actually read the disk are gated on
		// --file-path. abspath, basename, dirname and pathexpand manipulate a
		// path string and touch nothing, so stdlib registers them
		// unconditionally — which is what doc/functions.md describes. Naming
		// them here as well contributed the identical function value under a
		// second plugin, making which registration won a matter of init()
		// order; harmless while the two agreed, and a collision either way.
		return map[string]function.Function{
			"file":       filesystem.MakeFileFunc(base, false),
			"fileexists": filesystem.MakeFileExistsFunc(base),
			"fileset":    filesystem.MakeFileSetFunc(base),
			"filebase64": filesystem.MakeFileFunc(base, true),
			"filebytes":  MakeFileBytesFunc(base),
		}
	})
}
