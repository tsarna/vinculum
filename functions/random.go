package functions

import (
	randcty "github.com/tsarna/rand-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("random", func(_ *cfg.Config) map[string]function.Function {
		return randcty.GetRandomFunctions()
	})
}
