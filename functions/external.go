package functions

import (
	randcty "github.com/tsarna/rand-cty-funcs"
	richcty "github.com/tsarna/rich-cty-types"
	sqidcty "github.com/tsarna/sqid-cty-funcs"
	timecty "github.com/tsarna/time-cty-funcs"
	urlcty "github.com/tsarna/url-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("generic", func(_ *cfg.Config) map[string]function.Function {
		return richcty.GetGenericFunctions()
	})
	cfg.RegisterFunctionPlugin("random", func(_ *cfg.Config) map[string]function.Function {
		return randcty.GetRandomFunctions()
	})
	cfg.RegisterFunctionPlugin("sqid", func(_ *cfg.Config) map[string]function.Function {
		return sqidcty.GetSqidFunctions()
	})
	cfg.RegisterFunctionPlugin("time", func(_ *cfg.Config) map[string]function.Function {
		return timecty.GetTimeFunctions()
	})
	cfg.RegisterFunctionPlugin("url", func(_ *cfg.Config) map[string]function.Function {
		return urlcty.GetURLFunctions()
	})
}
