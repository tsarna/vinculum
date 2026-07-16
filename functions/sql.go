package functions

import (
	"fmt"
	"strings"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("sql", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"sql::must": SQLMustFunc,
		}
	})
}

// SQLMustFunc turns the "error rides in the result object" convention used by
// SQL `call()` into a fail-fast: given a result object, if its `error` field is
// non-null it raises an evaluation error built from the error fields; otherwise
// it returns the result unchanged.
//
//	# branch on the error...
//	r = call(ctx, client.db, "INSERT ...")
//	# ...or just fail the action if it errored:
//	r = sql_must(call(ctx, client.db, "INSERT ..."))
var SQLMustFunc = function.New(&function.Spec{
	Description: "Returns a SQL result unchanged if it carries no error; otherwise raises an HCL error. Its return type echoes the result passed in.",
	Params: []function.Parameter{
		{
			Name:             "result",
			Type:             cty.DynamicPseudoType,
			AllowNull:        true,
			AllowDynamicType: true,
			Description:      "A SQL result object (must have an \"error\" field)",
		},
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		return args[0].Type(), nil
	},
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		result := args[0]
		if !result.IsKnown() {
			return cty.UnknownVal(retType), nil
		}
		if result.IsNull() || !result.Type().IsObjectType() || !result.Type().HasAttribute("error") {
			return cty.NilVal, fmt.Errorf("sql::must: argument is not a result object (no \"error\" field)")
		}

		errVal := result.GetAttr("error")
		if errVal.IsNull() {
			return result, nil
		}

		return cty.NilVal, fmt.Errorf("sql::must: %s", formatSQLError(errVal))
	},
})

// formatSQLError renders a result error object as a single-line message,
// including whichever of driver/code/sqlstate are present and non-empty.
func formatSQLError(errVal cty.Value) string {
	message := errAttr(errVal, "message")
	if message == "" {
		message = "SQL error"
	}

	var qualifiers []string
	if driver := errAttr(errVal, "driver"); driver != "" {
		qualifiers = append(qualifiers, driver)
	}
	if code := errAttr(errVal, "code"); code != "" {
		qualifiers = append(qualifiers, "code "+code)
	}
	if sqlstate := errAttr(errVal, "sqlstate"); sqlstate != "" {
		qualifiers = append(qualifiers, "sqlstate "+sqlstate)
	}

	if len(qualifiers) == 0 {
		return message
	}
	return fmt.Sprintf("%s: %s", strings.Join(qualifiers, " "), message)
}

// errAttr reads a string attribute from an error object, returning "" when it
// is absent, null, unknown, or not a string.
func errAttr(errVal cty.Value, name string) string {
	if !errVal.Type().IsObjectType() || !errVal.Type().HasAttribute(name) {
		return ""
	}
	a := errVal.GetAttr(name)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.String {
		return ""
	}
	return a.AsString()
}
