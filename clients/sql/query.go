package sql

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	// Make the sql client's specific type and its named-query capsule
	// (client.<db> and client.<db>.<query>) nameable in .cty type annotations.
	// sql_client is an open type because client.<db> is a rich object, not a bare
	// capsule (see IsSQLClient).
	cfg.RegisterFunctyOpenType("sql_client", IsSQLClient)
	cfg.RegisterFunctyType("sql_query", queryCapsuleType)
}

// QueryDef is a `query "name" { ... }` sub-block inside a SQL client block.
type QueryDef struct {
	Name             string    `hcl:",label"`
	SQL              string    `hcl:"sql"`
	Cardinality      string    `hcl:"cardinality,optional"` // one|zero_or_one|many|exec; default "many"
	OnZero           string    `hcl:"on_zero,optional"`     // null|error; default "null" (zero_or_one only)
	StatementTimeout *string   `hcl:"statement_timeout,optional"`
	Disabled         bool      `hcl:"disabled,optional"`
	DefRange         hcl.Range `hcl:",def_range"`
}

// cardinality declares how many rows a named query is expected to return,
// which constrains whether get() and/or call() may target it.
type cardinality int

const (
	cardMany cardinality = iota // default
	cardOne
	cardZeroOrOne
	cardExec
)

func (c cardinality) String() string {
	switch c {
	case cardOne:
		return "one"
	case cardZeroOrOne:
		return "zero_or_one"
	case cardExec:
		return "exec"
	default:
		return "many"
	}
}

// parseCardinality validates the cardinality attribute, defaulting to "many".
func parseCardinality(s string, r hcl.Range) (cardinality, hcl.Diagnostics) {
	switch s {
	case "", "many":
		return cardMany, nil
	case "one":
		return cardOne, nil
	case "zero_or_one":
		return cardZeroOrOne, nil
	case "exec":
		return cardExec, nil
	default:
		return cardMany, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid query cardinality",
			Detail:   fmt.Sprintf("cardinality must be one of \"one\", \"zero_or_one\", \"many\", \"exec\"; got %q", s),
			Subject:  &r,
		}}
	}
}

// namedQuery is a compiled `query` sub-block bound to its parent client.
type namedQuery struct {
	client      *SQLClient
	name        string
	sql         string
	card        cardinality
	onZeroError bool          // on_zero = "error" (zero_or_one only)
	stmtTO      time.Duration // resolved: query override else client default
	defRange    hcl.Range
}

// Get implements richcty.Gettable. It enforces verb/cardinality compatibility
// at runtime: get() is only valid on "one"/"zero_or_one" queries.
func (q *namedQuery) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if q.card == cardMany || q.card == cardExec {
		return cty.NilVal, fmt.Errorf("get() not allowed on query %q with cardinality %q; use call()", q.name, q.card)
	}
	return q.client.getOneRow(ctx, q.sql, args, q.stmtTO)
}

// Call implements richcty.Callable, returning the full result object. For a
// zero_or_one query with on_zero = "error", a zero-row result is reported as
// result.error instead of a null row.
func (q *namedQuery) Call(ctx context.Context, args []cty.Value) (cty.Value, error) {
	forceExec := q.card == cardExec
	res, err := q.client.callStmt(ctx, q.sql, args, q.stmtTO, &forceExec)
	if err != nil {
		return res, err
	}

	if q.card == cardZeroOrOne && q.onZeroError && res.GetAttr("error").IsNull() {
		if rc, _ := res.GetAttr("row_count").AsBigFloat().Int64(); rc == 0 {
			return errorResult(q.client.dialect, fmt.Errorf("query %q returned no rows", q.name)), nil
		}
	}
	return res, nil
}

// queryCapsuleType wraps a *namedQuery so get()/call() can dispatch on it via
// richcty.GetCapsuleFromValue.
var queryCapsuleType = cty.CapsuleWithOps("sql_query", reflect.TypeOf((*namedQuery)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val any) string {
		q := val.(*namedQuery)
		return fmt.Sprintf("sql_query(%s)", q.name)
	},
	TypeGoString: func(reflect.Type) string { return "sql_query" },
})

func newQueryCapsule(q *namedQuery) cty.Value {
	return cty.CapsuleVal(queryCapsuleType, q)
}
