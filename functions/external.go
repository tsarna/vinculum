package functions

import (
	barcodecty "github.com/tsarna/barcode-cty-func"
	geocty "github.com/tsarna/geo-cty-funcs"
	randcty "github.com/tsarna/rand-cty-funcs"
	richcty "github.com/tsarna/rich-cty-types"
	sqidcty "github.com/tsarna/sqid-cty-funcs"
	timecty "github.com/tsarna/time-cty-funcs"
	urlcty "github.com/tsarna/url-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("barcode", func(_ *cfg.Config) map[string]function.Function {
		return barcodecty.GetBarcodeFunctions()
	})
	cfg.RegisterFunctionPlugin("geo", func(_ *cfg.Config) map[string]function.Function {
		return geocty.GetGeoFunctions()
	})
	cfg.RegisterFunctionPlugin("generic", func(_ *cfg.Config) map[string]function.Function {
		return richcty.GetGenericFunctions()
	})
	// The real signatures of those functions. Each takes an optional *leading*
	// context, which cty cannot express — it can only make trailing parameters
	// optional — so from cty metadata alone the whole family reads as
	// `get(thing, ...args)`. These declarations are what help() shows instead.
	cfg.RegisterFunctyExterns(richcty.ExternsFilename, richcty.Externs())
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
