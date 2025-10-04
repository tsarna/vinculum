package functions

import (
	"github.com/hashicorp/go-cty-funcs/cidr"
	"github.com/hashicorp/go-cty-funcs/crypto"
	"github.com/hashicorp/go-cty-funcs/encoding"
	"github.com/hashicorp/go-cty-funcs/filesystem"
	"github.com/hashicorp/go-cty-funcs/uuid"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

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
		"element":      stdlib.ElementFunc,
		"length":       stdlib.LengthFunc,
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

		// Date/time functions
		"formatdate": stdlib.FormatDateFunc,
		"timeadd":    stdlib.TimeAddFunc,

		// Type conversion functions (using MakeToFunc)
		"tostring": stdlib.MakeToFunc(cty.String),
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
		"base64decode": encoding.Base64DecodeFunc,
		"base64encode": encoding.Base64EncodeFunc,
		"urlencode":    encoding.URLEncodeFunc,

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
