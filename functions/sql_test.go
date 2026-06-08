package functions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func sqlSuccessResult() cty.Value {
	errType := cty.Object(map[string]cty.Type{
		"code": cty.String, "message": cty.String, "sqlstate": cty.String, "driver": cty.String,
	})
	return cty.ObjectVal(map[string]cty.Value{
		"rows":           cty.EmptyTupleVal,
		"row":            cty.NullVal(cty.DynamicPseudoType),
		"row_count":      cty.NumberIntVal(0),
		"affected":       cty.NumberIntVal(1),
		"last_insert_id": cty.NumberIntVal(7),
		"error":          cty.NullVal(errType),
	})
}

func sqlErrorResult() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"rows":           cty.EmptyTupleVal,
		"row":            cty.NullVal(cty.DynamicPseudoType),
		"row_count":      cty.NumberIntVal(0),
		"affected":       cty.NumberIntVal(0),
		"last_insert_id": cty.NullVal(cty.Number),
		"error": cty.ObjectVal(map[string]cty.Value{
			"code":     cty.StringVal("2067"),
			"message":  cty.StringVal("UNIQUE constraint failed: users.email"),
			"sqlstate": cty.StringVal(""),
			"driver":   cty.StringVal("sqlite"),
		}),
	})
}

func TestSQLMustPassesThroughSuccess(t *testing.T) {
	in := sqlSuccessResult()
	out, err := SQLMustFunc.Call([]cty.Value{in})
	require.NoError(t, err)
	assert.True(t, in.RawEquals(out), "expected result returned unchanged")
}

func TestSQLMustRaisesOnError(t *testing.T) {
	_, err := SQLMustFunc.Call([]cty.Value{sqlErrorResult()})
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "sql_must")
	assert.Contains(t, msg, "sqlite")
	assert.Contains(t, msg, "code 2067")
	assert.Contains(t, msg, "UNIQUE constraint failed: users.email")
	// empty sqlstate must not appear
	assert.NotContains(t, msg, "sqlstate")
}

func TestSQLMustRejectsNonResult(t *testing.T) {
	_, err := SQLMustFunc.Call([]cty.Value{cty.StringVal("not a result")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a result object")
}

func TestSQLMustRejectsNull(t *testing.T) {
	_, err := SQLMustFunc.Call([]cty.Value{cty.NullVal(cty.DynamicPseudoType)})
	require.Error(t, err)
}

// An LLM-style result (error object with only code+message) is also accepted.
func TestSQLMustWorksOnMinimalErrorObject(t *testing.T) {
	res := cty.ObjectVal(map[string]cty.Value{
		"content": cty.NullVal(cty.String),
		"error": cty.ObjectVal(map[string]cty.Value{
			"code":    cty.StringVal("rate_limit"),
			"message": cty.StringVal("slow down"),
		}),
	})
	_, err := SQLMustFunc.Call([]cty.Value{res})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code rate_limit")
	assert.Contains(t, err.Error(), "slow down")
}
