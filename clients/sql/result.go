package sql

import (
	"github.com/zclconf/go-cty/cty"
)

// errorObjectType is the cty type of the result object's `error` field. It is
// used both for the populated error object and for the typed null on success.
var errorObjectType = cty.Object(map[string]cty.Type{
	"code":     cty.String,
	"message":  cty.String,
	"sqlstate": cty.String,
	"driver":   cty.String,
})

// successResult builds the result object returned by call() on success.
//
//	rows           tuple of row objects (possibly empty)
//	row            rows[0], or null
//	row_count      length(rows)
//	affected       rows affected by a modifying statement (0 for SELECT)
//	last_insert_id last AUTO_INCREMENT/ROWID, or null
//	error          null
//
// affected and lastID are pointers so "not applicable" maps to a null/zero
// distinct from a real zero where it matters (last_insert_id).
func successResult(rows []cty.Value, affected, lastID *int64) cty.Value {
	var rowsVal, rowVal cty.Value
	if len(rows) == 0 {
		rowsVal = cty.EmptyTupleVal
		rowVal = cty.NullVal(cty.DynamicPseudoType)
	} else {
		rowsVal = cty.TupleVal(rows)
		rowVal = rows[0]
	}

	affectedVal := cty.NumberIntVal(0)
	if affected != nil {
		affectedVal = cty.NumberIntVal(*affected)
	}

	lastVal := cty.NullVal(cty.Number)
	if lastID != nil {
		lastVal = cty.NumberIntVal(*lastID)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"rows":           rowsVal,
		"row":            rowVal,
		"row_count":      cty.NumberIntVal(int64(len(rows))),
		"affected":       affectedVal,
		"last_insert_id": lastVal,
		"error":          cty.NullVal(errorObjectType),
	})
}

// errorResult builds the result object returned by call() when the database
// reports an execution failure. call() never returns a Go error for execution
// failures (mirroring the LLM client); the error rides in this object.
func errorResult(d Dialect, err error) cty.Value {
	code, sqlstate := d.ClassifyError(err)
	return cty.ObjectVal(map[string]cty.Value{
		"rows":           cty.EmptyTupleVal,
		"row":            cty.NullVal(cty.DynamicPseudoType),
		"row_count":      cty.NumberIntVal(0),
		"affected":       cty.NumberIntVal(0),
		"last_insert_id": cty.NullVal(cty.Number),
		"error": cty.ObjectVal(map[string]cty.Value{
			"code":     cty.StringVal(code),
			"message":  cty.StringVal(err.Error()),
			"sqlstate": cty.StringVal(sqlstate),
			"driver":   cty.StringVal(d.Name()),
		}),
	})
}
