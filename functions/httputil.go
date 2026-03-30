package functions

import (
	"encoding/base64"
	"fmt"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("httputil", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"basicauth": BasicAuthFunc,
		}
	})
}

// BasicAuthFunc returns the value for an HTTP Authorization header using Basic auth
var BasicAuthFunc = function.New(&function.Spec{
	Description: "Returns the value for an HTTP Basic Authorization header for the given username and password",
	Params: []function.Parameter{
		{Name: "user", Type: cty.String, Description: "Username"},
		{Name: "password", Type: cty.String, Description: "Password"},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		user := args[0].AsString()
		password := args[1].AsString()
		encoded := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", user, password)))
		return cty.StringVal("Basic " + encoded), nil
	},
})
