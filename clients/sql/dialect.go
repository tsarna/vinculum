// Package sql provides the dialect-agnostic engine shared by all SQL client
// blocks (`client "postgres"`, `client "mysql"`, `client "sqlite"`).
//
// This package is pure Go and carries no database driver imports: a concrete
// dialect (e.g. clients/sql/sqlite) supplies a Dialect and calls ProcessCommon.
// SQL statements are executed through the existing polymorphic get()/call()
// functions; see doc/client-sql.md for the user-facing surface.
package sql

import (
	"time"
)

// Dialect is everything the common engine needs from a concrete database.
// A dialect package implements this and hands it to ProcessCommon along with
// the decoded common configuration.
type Dialect interface {
	// DriverName is the database/sql driver name ("sqlite3", "pgx", "mysql").
	DriverName() string

	// DSN is the fully-assembled connection string passed to sql.Open. The
	// dialect package owns all discrete-field → DSN assembly and escaping.
	DSN() string

	// BindType selects the sqlx.Rebind target style: sqlx.QUESTION for
	// sqlite/mysql, sqlx.DOLLAR for postgres.
	BindType() int

	// Name is the dialect label reported in the result object's error.driver
	// field ("sqlite", "postgres", "mysql").
	Name() string

	// ClassifyError extracts (code, sqlstate) from a driver error for the
	// result error object. Returning ("", "") is fine — message-only errors
	// still work.
	ClassifyError(err error) (code, sqlstate string)
}

// PoolDefaults carries the dialect-flavored default pool tuning used when a
// client block leaves a knob unset. Postgres/MySQL use larger defaults than
// SQLite; the dialect package supplies the appropriate values.
type PoolDefaults struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}
