package sql

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bytescty "github.com/tsarna/bytes-cty-type"
	"github.com/tsarna/go2cty2go"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
)

// timeLayouts are the textual datetime forms SQLite (and other drivers that
// hand back strings) may produce. Tried in order; first match wins.
var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// scanRows materializes all rows of a result set into a slice of cty object
// values keyed by column name. Each cell is mapped to a cty value by mapCell.
func scanRows(rows *sql.Rows) ([]cty.Value, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}

	dbTypeNames := make([]string, len(colTypes))
	for i, ct := range colTypes {
		dbTypeNames[i] = strings.ToUpper(ct.DatabaseTypeName())
	}

	var out []cty.Value
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}

		obj := make(map[string]cty.Value, len(cols))
		for i, name := range cols {
			obj[name] = mapCell(holders[i], dbTypeNames[i])
		}
		out = append(out, cty.ObjectVal(obj))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// mapCell maps a single scanned cell to a cty value. It is a two-tier mapper:
// the column's declared database type name is used as a hint, but the concrete
// Go type of the scanned value is the primary driver — essential for SQLite,
// whose DatabaseTypeName() is frequently empty for expression columns and loose
// for declared ones.
func mapCell(v any, dbType string) cty.Value {
	if v == nil {
		return cty.NullVal(cty.DynamicPseudoType)
	}

	switch val := v.(type) {
	case int64:
		if isBoolType(dbType) {
			return cty.BoolVal(val != 0)
		}
		return cty.NumberIntVal(val)
	case float64:
		return cty.NumberFloatVal(val)
	case bool:
		return cty.BoolVal(val)
	case time.Time:
		return timecty.NewTimeCapsule(val)
	case []byte:
		// Textual families arrive as []byte from some drivers; decode them as
		// text/JSON rather than wrapping in a bytes capsule.
		if isJSONType(dbType) {
			return decodeJSONOrString(string(val))
		}
		if isTextType(dbType) || isTimeType(dbType) {
			return mapTextCell(string(val), dbType)
		}
		return bytescty.BuildBytesObject(val, "application/octet-stream")
	case string:
		return mapTextCell(val, dbType)
	default:
		// Unknown driver type: best-effort conversion, falling back to string.
		if cv, err := go2cty2go.AnyToCty(v); err == nil {
			return cv
		}
		return cty.StringVal(fmt.Sprintf("%v", v))
	}
}

// mapTextCell handles a value that arrived as text, applying JSON decoding or
// datetime parsing when the declared type calls for it.
func mapTextCell(s, dbType string) cty.Value {
	switch {
	case isJSONType(dbType):
		return decodeJSONOrString(s)
	case isTimeType(dbType):
		if t, ok := parseTime(s); ok {
			return timecty.NewTimeCapsule(t)
		}
		return cty.StringVal(s)
	default:
		return cty.StringVal(s)
	}
}

// decodeJSONOrString decodes a JSON column value into a cty value. On decode
// failure it falls back to the raw string with no error.
func decodeJSONOrString(s string) cty.Value {
	var anyVal any
	if err := json.Unmarshal([]byte(s), &anyVal); err != nil {
		return cty.StringVal(s)
	}
	cv, err := go2cty2go.AnyToCty(anyVal)
	if err != nil {
		return cty.StringVal(s)
	}
	return cv
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func isTextType(t string) bool {
	switch t {
	case "TEXT", "VARCHAR", "CHAR", "CHARACTER", "CLOB", "NVARCHAR", "NCHAR", "UUID":
		return true
	}
	return strings.Contains(t, "CHAR") || strings.Contains(t, "TEXT")
}

func isTimeType(t string) bool {
	switch t {
	case "DATETIME", "TIMESTAMP", "DATE", "TIMESTAMPTZ":
		return true
	}
	return false
}

func isJSONType(t string) bool {
	return t == "JSON" || t == "JSONB"
}

func isBoolType(t string) bool {
	return t == "BOOLEAN" || t == "BOOL"
}
