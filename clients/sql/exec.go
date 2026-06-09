package sql

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	bytescty "github.com/tsarna/bytes-cty-type"
	"github.com/tsarna/go2cty2go"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
)

// namedPlaceholderRe matches :name style placeholders. It is a deliberately
// simple heuristic for the mixing-forms check; full placeholder parsing
// (including string-literal awareness) is delegated to sqlx.
var namedPlaceholderRe = regexp.MustCompile(`:[a-zA-Z_][a-zA-Z0-9_]*`)

// queryLeadingKeywordRe extracts the first SQL keyword to decide, for inline
// call(), whether a statement returns rows (Query) or not (Exec).
var queryLeadingKeywordRe = regexp.MustCompile(`^\s*\(*\s*([a-zA-Z]+)`)

// returnsRows reports whether an inline statement should be run via
// QueryContext (true) or ExecContext (false), based on its leading keyword.
func returnsRows(sqlText string) bool {
	m := queryLeadingKeywordRe.FindStringSubmatch(sqlText)
	if m == nil {
		return false
	}
	switch strings.ToUpper(m[1]) {
	case "SELECT", "WITH", "VALUES", "SHOW", "PRAGMA", "EXPLAIN", "DESCRIBE", "DESC", "TABLE":
		return true
	default:
		return false
	}
}

// bindParams converts the raw SQL plus cty params into a driver-ready statement
// and positional argument slice, following the argument-form rules:
//
//   - no params        → statement rebound, no args
//   - one object param  → named (:name) binding via sqlx.Named
//   - otherwise         → positional (?) binding
//
// Mixing ? and :name in a single statement is an argument-shape error, returned
// as a Go error so it surfaces at the call site (not in result.error).
func (c *SQLClient) bindParams(rawSQL string, params []cty.Value) (string, []any, error) {
	bindType := c.dialect.BindType()

	switch {
	case len(params) == 0:
		if hasNamed(rawSQL) {
			return "", nil, fmt.Errorf("statement uses :name placeholders but no parameters were supplied")
		}
		return sqlx.Rebind(bindType, rawSQL), nil, nil

	case len(params) == 1 && params[0].Type().IsObjectType() && !isBytesValue(params[0]):
		// Named params.
		if hasPositional(rawSQL) {
			return "", nil, fmt.Errorf("statement mixes ? and :name placeholders; use one form")
		}
		m, err := ctyObjectToParamMap(params[0])
		if err != nil {
			return "", nil, err
		}
		bound, args, err := sqlx.Named(rawSQL, m)
		if err != nil {
			return "", nil, fmt.Errorf("binding named parameters: %w", err)
		}
		return sqlx.Rebind(bindType, bound), args, nil

	default:
		// Positional params.
		if hasNamed(rawSQL) {
			return "", nil, fmt.Errorf("statement mixes ? and :name placeholders; use one form")
		}
		args := make([]any, len(params))
		for i, v := range params {
			a, err := ctyToParam(v)
			if err != nil {
				return "", nil, fmt.Errorf("parameter %d: %w", i+1, err)
			}
			args[i] = a
		}
		return sqlx.Rebind(bindType, rawSQL), args, nil
	}
}

func hasNamed(sqlText string) bool      { return namedPlaceholderRe.MatchString(sqlText) }
func hasPositional(sqlText string) bool { return strings.Contains(sqlText, "?") }

// isBytesValue reports whether a value is a bytes object/capsule, which is an
// object type but must be treated as a single positional parameter rather than
// a named-parameter map.
func isBytesValue(v cty.Value) bool {
	_, err := bytescty.GetBytesFromValue(v)
	return err == nil
}

// ctyObjectToParamMap converts a cty object to a map for sqlx.Named binding.
func ctyObjectToParamMap(obj cty.Value) (map[string]any, error) {
	out := make(map[string]any, len(obj.Type().AttributeTypes()))
	for name := range obj.Type().AttributeTypes() {
		a, err := ctyToParam(obj.GetAttr(name))
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		out[name] = a
	}
	return out, nil
}

// ctyToParam converts a single cty value to a driver-bindable Go value,
// special-casing bytes (→ []byte) and time (→ time.Time) before falling back
// to the generic cty→Go conversion.
func ctyToParam(v cty.Value) (any, error) {
	if v.IsNull() {
		return nil, nil
	}
	if b, err := bytescty.GetBytesFromValue(v); err == nil {
		return b.Data, nil
	}
	if t, err := timecty.GetTime(v); err == nil {
		return t, nil
	}
	return go2cty2go.CtyToAny(v)
}

// withTimeout applies the effective statement timeout: the minimum of the
// configured duration and any inbound context deadline. A zero duration means
// no engine-imposed timeout (rely on the inbound context).
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= d {
		return ctx, func() {} // inbound deadline already tighter
	}
	return context.WithTimeout(ctx, d)
}

// getOneRow executes a statement expected to return exactly one row and returns
// it as a cty object. Zero or more-than-one rows is a Go error (get() implies
// exactly one).
func (c *SQLClient) getOneRow(ctx context.Context, rawSQL string, params []cty.Value, timeout time.Duration) (cty.Value, error) {
	if c.db == nil {
		return cty.NilVal, fmt.Errorf("sql client %q is not connected", c.Name)
	}
	bound, args, err := c.bindParams(rawSQL, params)
	if err != nil {
		return cty.NilVal, err
	}
	qctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	rows, err := c.db.QueryContext(qctx, bound, args...)
	if err != nil {
		return cty.NilVal, err
	}
	defer rows.Close()

	scanned, err := scanRows(rows)
	if err != nil {
		return cty.NilVal, err
	}
	switch len(scanned) {
	case 0:
		return cty.NilVal, fmt.Errorf("get() expected exactly one row, got zero")
	case 1:
		return scanned[0], nil
	default:
		return cty.NilVal, fmt.Errorf("get() expected exactly one row, got %d", len(scanned))
	}
}

// callStmt executes a statement and returns the result object. Execution
// failures are reported in result.error, never as a Go error; only
// argument-shape (binding) errors are returned as Go errors.
//
// forceExec selects ExecContext (for named queries with cardinality "exec");
// when nil, inline statements are classified by their leading keyword.
func (c *SQLClient) callStmt(ctx context.Context, rawSQL string, params []cty.Value, timeout time.Duration, forceExec *bool) (cty.Value, error) {
	if c.db == nil {
		return cty.NilVal, fmt.Errorf("sql client %q is not connected", c.Name)
	}
	bound, args, err := c.bindParams(rawSQL, params)
	if err != nil {
		return cty.NilVal, err
	}
	qctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	useExec := false
	if forceExec != nil {
		useExec = *forceExec
	} else {
		useExec = !returnsRows(rawSQL)
	}

	if useExec {
		res, err := c.db.ExecContext(qctx, bound, args...)
		if err != nil {
			return errorResult(c.dialect, err), nil
		}
		var affectedPtr, lastPtr *int64
		if n, err := res.RowsAffected(); err == nil {
			affectedPtr = &n
		}
		if id, err := res.LastInsertId(); err == nil {
			lastPtr = &id
		}
		return successResult(nil, affectedPtr, lastPtr), nil
	}

	rows, err := c.db.QueryContext(qctx, bound, args...)
	if err != nil {
		return errorResult(c.dialect, err), nil
	}
	defer rows.Close()

	scanned, err := scanRows(rows)
	if err != nil {
		return errorResult(c.dialect, err), nil
	}
	return successResult(scanned, nil, nil), nil
}
