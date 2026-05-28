package ambient

import (
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterAmbientProvider("env", func(_ *cfg.Config) cty.Value {
		return hclutil.EnvObject()
	})
}
