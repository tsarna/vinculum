package functions

import (
	sqidcty "github.com/tsarna/sqid-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("sqid", func(_ *cfg.Config) map[string]function.Function {
		return sqidcty.GetSqidFunctions()
	})
}
