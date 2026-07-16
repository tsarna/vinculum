package functions

import (
	"github.com/hashicorp/go-cty-funcs/cidr"
	"github.com/hashicorp/go-cty-funcs/crypto"
	"github.com/hashicorp/go-cty-funcs/encoding"
	"github.com/hashicorp/go-cty-funcs/filesystem"
	"github.com/hashicorp/go-cty-funcs/uuid"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

func init() {
	cfg.RegisterFunctionPlugin("stdlib", func(_ *cfg.Config) map[string]function.Function {
		return GetStandardLibraryFunctions()
	})
}

// GetStandardLibraryFunctions returns a map of all cty standard library functions
// suitable for providing to an HCL evaluation context.
func GetStandardLibraryFunctions() map[string]function.Function {
	return map[string]function.Function{
		// String functions
		"upper":     stdlib.UpperFunc,
		"lower":     stdlib.LowerFunc,
		"title":     stdlib.TitleFunc,
		"substr":    stdlib.SubstrFunc,
		"strlen":    stdlib.StrlenFunc,
		"split":     stdlib.SplitFunc,
		"join":      stdlib.JoinFunc,
		"sort":      stdlib.SortFunc,
		"reverse":   stdlib.ReverseFunc,
		"chomp":     stdlib.ChompFunc,
		"indent":    stdlib.IndentFunc,
		"trim":      stdlib.TrimFunc,
		"trimspace": stdlib.TrimSpaceFunc,
		"replace":   stdlib.ReplaceFunc,
		"regex":     stdlib.RegexFunc,
		"regexall":  stdlib.RegexAllFunc,

		// Numeric functions
		"abs":    stdlib.AbsoluteFunc,
		"ceil":   stdlib.CeilFunc,
		"floor":  stdlib.FloorFunc,
		"log":    stdlib.LogFunc,
		"max":    stdlib.MaxFunc,
		"min":    stdlib.MinFunc,
		"pow":    stdlib.PowFunc,
		"signum": stdlib.SignumFunc,

		// Collection functions
		// Note: "length" is registered via the generic plugin with an enhanced
		// implementation that supports Lengthable capsule types.
		"element":      stdlib.ElementFunc,
		"coalesce":     stdlib.CoalesceFunc,
		"coalescelist": stdlib.CoalesceListFunc,
		"compact":      stdlib.CompactFunc,
		"contains":     stdlib.ContainsFunc,
		"distinct":     stdlib.DistinctFunc,
		"flatten":      stdlib.FlattenFunc,
		"keys":         stdlib.KeysFunc,
		"values":       stdlib.ValuesFunc,
		"lookup":       stdlib.LookupFunc,
		"merge":        stdlib.MergeFunc,
		"range":        stdlib.RangeFunc,
		"slice":        stdlib.SliceFunc,
		"zipmap":       stdlib.ZipmapFunc,

		// Encoding functions
		"csvdecode":  stdlib.CSVDecodeFunc,
		"jsondecode": stdlib.JSONDecodeFunc,
		"jsonencode": stdlib.JSONEncodeFunc,

		// Date/time functions.
		//
		// These are the flat, string-in/string-out HCL builtins. The `time` plugin adds
		// the capsule-typed `time::*` and `duration::*` families alongside them:
		// `timeadd` takes RFC 3339 strings and returns one, while `time::add` takes (or
		// parses) a `time` and a `duration` and returns a `time`.
		"formatdate": stdlib.FormatDateFunc,
		"timeadd":    stdlib.TimeAddFunc,

		// Type conversion functions (using MakeToFunc)
		// Note: "tostring" is registered via the generic plugin with an enhanced
		// implementation that supports Stringable capsule types.
		"tonumber": stdlib.MakeToFunc(cty.Number),
		"tobool":   stdlib.MakeToFunc(cty.Bool),
		"tolist":   stdlib.MakeToFunc(cty.List(cty.DynamicPseudoType)),
		"tomap":    stdlib.MakeToFunc(cty.Map(cty.DynamicPseudoType)),
		"toset":    stdlib.MakeToFunc(cty.Set(cty.DynamicPseudoType)),
		"totuple":  stdlib.MakeToFunc(cty.Tuple([]cty.Type{})),
		"totype":   stdlib.MakeToFunc(cty.DynamicPseudoType),

		// Additional functions from go-cty-funcs

		// CIDR functions
		"cidrhost":    cidr.HostFunc,
		"cidrnetmask": cidr.NetmaskFunc,
		"cidrsubnet":  cidr.SubnetFunc,
		"cidrsubnets": cidr.SubnetsFunc,

		// Collection functions (additional)
		// Note: coalescelist is already provided by stdlib.CoalesceListFunc above

		// Crypto functions (hash functions and more)
		"bcrypt":     crypto.BcryptFunc,
		"rsadecrypt": crypto.RsaDecryptFunc,
		"md5":        crypto.Md5Func,
		"sha1":       crypto.Sha1Func,
		"sha256":     crypto.Sha256Func,
		"sha512":     crypto.Sha512Func,

		// Encoding functions (additional - these override stdlib versions if any exist)
		// Note: base64encode and base64decode are provided by the bytes plugin with
		// enhanced implementations that support the bytes capsule type.
		//
		// urlencode is percent-encoding's flat HCL builtin. It is also registered as
		// `url::encode` so that it pairs symmetrically with `url::decode` (from the url
		// plugin), whose family url-cty-funcs namespaced under url::.
		"urlencode":   encoding.URLEncodeFunc,
		"url::encode": encoding.URLEncodeFunc,

		// Filesystem functions
		"abspath":    filesystem.AbsPathFunc,
		"basename":   filesystem.BasenameFunc,
		"dirname":    filesystem.DirnameFunc,
		"pathexpand": filesystem.PathExpandFunc,

		// UUID functions
		"uuidv4": uuid.V4Func,
		"uuidv5": uuid.V5Func,
	}
}
