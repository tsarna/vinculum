package functions

import (
	timecty "github.com/tsarna/time-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("time", func(_ *cfg.Config) map[string]function.Function {
		return timecty.GetTimeFunctions()
	})
}
