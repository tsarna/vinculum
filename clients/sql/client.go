package sql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

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
		"_capsule": cfg.NewClientCapsule(c),
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
