package sql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"time"

	richcty "github.com/tsarna/rich-cty-types"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

// SQLClientCapsuleType is the client-specific capsule carried under the sql
// client object's _capsule attribute, so client.<name> for a sql client is
// nameable as "sql_client" in .cty annotations (distinct from the generic
// "client" open type, which also matches it since *SQLClient implements
// config.Client). get()/set()/call() dispatch through richcty interfaces on the
// encapsulated value, not this type, so they are unaffected.
var SQLClientCapsuleType = cty.CapsuleWithOps("sql_client", reflect.TypeOf((*SQLClient)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val any) string {
		return fmt.Sprintf("sql_client(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "sql_client"
	},
})

// IsSQLClient reports whether val is (or carries under a _capsule attribute) a
// *SQLClient. It backs the functy "sql_client" open type. This must be open, not
// identity on SQLClientCapsuleType: client.<name> for a sql client is a rich
// object whose attribute set *varies per instance* — one attribute per declared
// query (`{_capsule: sql_client, <query>: sql_query, …}`) — so there is no single
// fixed cty.Type to match by identity. GetCapsuleFromValue unwraps the _capsule.
// Null is handled by the constraint before the predicate runs.
func IsSQLClient(val cty.Value) error {
	enc, err := richcty.GetCapsuleFromValue(val)
	if err != nil {
		return err
	}
	if _, ok := enc.(*SQLClient); !ok {
		return fmt.Errorf("expected a sql client, got %T", enc)
	}
	return nil
}

// SQLClient is the dialect-agnostic runtime for a SQL client block. The same
// type backs every dialect; the Dialect supplies driver name, DSN, and bindvar
// style.
type SQLClient struct {
	cfg.BaseClient

	dialect Dialect
	db      *sql.DB
	stmtTO  time.Duration // client-default statement timeout; 0 = none

	queries    map[string]*namedQuery
	queryOrder []string

	// resolved pool knobs
	maxOpen, maxIdle         int
	connMaxLife, connMaxIdle time.Duration
}

// Interface assertions.
var (
	_ cfg.Client    = (*SQLClient)(nil)
	_ cfg.CtyValuer = (*SQLClient)(nil)
	_ cfg.Startable = (*SQLClient)(nil)
	_ cfg.Stoppable = (*SQLClient)(nil)
)

// Start opens the connection pool and verifies connectivity.
func (c *SQLClient) Start() error {
	db, err := sql.Open(c.dialect.DriverName(), c.dialect.DSN())
	if err != nil {
		return fmt.Errorf("sql client %q: open: %w", c.Name, err)
	}
	db.SetMaxOpenConns(c.maxOpen)
	db.SetMaxIdleConns(c.maxIdle)
	db.SetConnMaxLifetime(c.connMaxLife)
	db.SetConnMaxIdleTime(c.connMaxIdle)

	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return fmt.Errorf("sql client %q: connect: %w", c.Name, err)
	}

	c.db = db
	return nil
}

// Stop closes the connection pool.
func (c *SQLClient) Stop() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// CtyValue exposes the client as `client.<name>`: a rich object whose _capsule
// is the client itself (for the bare inline-SQL form) and whose named
// attributes are the declared queries (`client.<name>.<query>`).
func (c *SQLClient) CtyValue() cty.Value {
	fields := map[string]cty.Value{
		"_capsule": cty.CapsuleVal(SQLClientCapsuleType, c),
	}
	for _, name := range c.queryOrder {
		fields[name] = newQueryCapsule(c.queries[name])
	}
	return cty.ObjectVal(fields)
}

// Get implements richcty.Gettable for the bare client form:
//
//	get(ctx, client.<name>, "SELECT ...", params...)
//
// args[0] is the inline SQL string; remaining args are parameters.
func (c *SQLClient) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	sqlText, params, err := splitInlineArgs("get", args)
	if err != nil {
		return cty.NilVal, err
	}
	return c.getOneRow(ctx, sqlText, params, c.stmtTO)
}

// Call implements richcty.Callable for the bare client form:
//
//	call(ctx, client.<name>, "SQL ...", params...)
func (c *SQLClient) Call(ctx context.Context, args []cty.Value) (cty.Value, error) {
	sqlText, params, err := splitInlineArgs("call", args)
	if err != nil {
		return cty.NilVal, err
	}
	return c.callStmt(ctx, sqlText, params, c.stmtTO, nil)
}

// splitInlineArgs validates and splits the inline-SQL argument form into the
// SQL string and the parameter values.
func splitInlineArgs(verb string, args []cty.Value) (string, []cty.Value, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("%s: a SQL string argument is required", verb)
	}
	if args[0].IsNull() || args[0].Type() != cty.String {
		return "", nil, fmt.Errorf("%s: the first argument after the client must be a SQL string", verb)
	}
	return args[0].AsString(), args[1:], nil
}
