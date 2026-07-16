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
	// The real signature of barcode(). Its options object is *optional*, and cty can
	// only make an argument optional by making it variadic — which says it may be
	// repeated, when it may not — and it has no way to describe the object's shape, so
	// it reflects as `barcode(type, data, ...options)`, with the four options invisible.
	cfg.RegisterFunctyExterns(barcodecty.ExternsFilename, barcodecty.Externs())
	cfg.RegisterFunctionPlugin("geo", func(_ *cfg.Config) map[string]function.Function {
		return geocty.GetGeoFunctions()
	})
	// `geopoint` — an object with numeric lat and lon, plus any extra attributes
	// (altitude, time, speed, …) carried through. Open, because the extras vary per
	// value. It is the shape every geo function's point arguments speak, and the geo
	// externs below annotate them with it.
	cfg.RegisterFunctyOpenType("geopoint", geocty.IsGeoPoint)
	// The real signatures of the geo functions: those faking an optional
	// coordinate/format/offset/time with a variadic, those dispatching on an argument's
	// type (geo::destination, the sky:: solar functions), geo::point (whose result shape
	// depends on the base it merges), and the fixed-shape ones whose dynamic point
	// arguments poison cty's return type to dynamic, hiding the return shape. One extern
	// file per namespace (geo, sky).
	for name, src := range geocty.Externs() {
		cfg.RegisterFunctyExterns(name, src)
	}
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
	// The real signatures: sqid()'s id is a number-or-list union cty cannot name, and both
	// functions take an optional options object (alphabet/min_length/blocklist) that cty
	// can only fake with a shapeless variadic.
	cfg.RegisterFunctyExterns(sqidcty.ExternsFilename, sqidcty.Externs())
	cfg.RegisterFunctionPlugin("time", func(_ *cfg.Config) map[string]function.Function {
		return timecty.GetTimeFunctions()
	})
	// The real signatures of the time functions cty cannot describe: those faking an
	// optional or defaulted argument with a variadic (time::now, time::from_unix, …),
	// and those genuinely overloaded — time::parse, whose first argument means
	// something different depending on how many there are, and time::sub, whose *return
	// type* depends on what it was handed.
	//
	// One file per namespace, because a functy source declares at most one.
	for name, src := range timecty.Externs() {
		cfg.RegisterFunctyExterns(name, src)
	}
	cfg.RegisterFunctionPlugin("url", func(_ *cfg.Config) map[string]function.Function {
		return urlcty.GetURLFunctions()
	})
	// The real signatures of url::join, url::join_path, and url::query_encode: each takes a
	// union argument (a URL as a string or a `url` object; a query map of strings or lists)
	// that cty declares `dynamic`, which then poisons its return type to dynamic and hides
	// it. The declarations restore the return and name the union. The other three url::
	// functions take a concrete string and are left to their (complete) cty metadata.
	cfg.RegisterFunctyExterns(urlcty.ExternsFilename, urlcty.Externs())
}
