// Package postgres registers the `client "postgres"` block. Unlike SQLite it is
// pure Go (the jackc/pgx/v5/stdlib driver needs no cgo), so there is no build-tag
// split and it is available in both the full and minimal container images.
//
// It supplies a sqlengine.Dialect plus DSN assembly and otherwise reuses the
// dialect-agnostic engine in clients/sql for pooling, named queries, parameter
// binding, row scanning, and the result object. See doc/client-sql.md.
package postgres

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/jmoiron/sqlx"
	sqlengine "github.com/tsarna/vinculum/clients/sql"
	cfg "github.com/tsarna/vinculum/config"
)

func init() {
	cfg.RegisterClientType("postgres", process)
}

// postgresPoolDefaults are the Postgres-flavored pool defaults from the spec's
// Pooling table: moderate concurrency, unlimited lifetimes.
var postgresPoolDefaults = sqlengine.PoolDefaults{
	MaxOpenConns:    25,
	MaxIdleConns:    5,
	ConnMaxLifetime: 0,
	ConnMaxIdleTime: 0,
}

// postgresDef decodes the Postgres-specific connection attributes. The remaining
// body (pool knobs + query blocks) is decoded by sqlengine.DecodeCommonDef.
//
// Either dsn or the discrete host/port/user/... fields are used; dsn wins if both
// are present.
type postgresDef struct {
	DSN        string         `hcl:"dsn,optional"`
	Host       string         `hcl:"host,optional"` // default "localhost"
	Port       *int           `hcl:"port,optional"` // default 5432
	User       string         `hcl:"user,optional"` // required when dsn unset
	Password   string         `hcl:"password,optional"`
	Database   string         `hcl:"database,optional"` // required when dsn unset
	SSLMode    string         `hcl:"sslmode,optional"`  // disable|require|verify-ca|verify-full
	SearchPath string         `hcl:"search_path,optional"`
	TLS        *cfg.TLSConfig `hcl:"tls,block"`

	Remain hcl.Body `hcl:",remain"`
}

func process(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Client, hcl.Diagnostics) {
	var def postgresDef
	diags := gohcl.DecodeBody(body, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}

	dsn, dsnDiags := buildDSN(config, block, &def)
	if dsnDiags.HasErrors() {
		return nil, dsnDiags
	}

	common, cDiags := sqlengine.DecodeCommonDef(def.Remain, config.EvalCtx())
	if cDiags.HasErrors() {
		return nil, cDiags
	}

	return sqlengine.ProcessCommon(config, block, postgresDialect{dsn: dsn}, common, postgresPoolDefaults)
}

// buildDSN assembles a libpq keyword/value connection string. The keyword/value
// form is preferred over the URL form so passwords with special characters need
// no percent-encoding — embedded values are quoted/escaped per libpq rules.
//
// When def.DSN is set it is passed through verbatim (it wins). Otherwise the
// discrete fields are assembled; user and database are required.
func buildDSN(config *cfg.Config, block *hcl.Block, def *postgresDef) (string, hcl.Diagnostics) {
	if def.DSN != "" {
		return def.DSN, nil
	}

	if def.User == "" || def.Database == "" {
		return "", hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing postgres connection settings",
			Detail:   "either dsn or both user and database must be set",
			Subject:  &block.DefRange,
		}}
	}

	host := def.Host
	if host == "" {
		host = "localhost"
	}
	port := 5432
	if def.Port != nil {
		port = *def.Port
	}

	var kv []string
	add := func(k, v string) { kv = append(kv, k+"="+quoteValue(v)) }

	add("host", host)
	add("port", strconv.Itoa(port))
	add("user", def.User)
	if def.Password != "" {
		add("password", def.Password)
	}
	add("dbname", def.Database)
	if def.SSLMode != "" {
		add("sslmode", def.SSLMode)
	}
	if def.SearchPath != "" {
		add("search_path", def.SearchPath)
	}

	tlsParams, tDiags := tlsDSNParams(config, block, def)
	if tDiags.HasErrors() {
		return "", tDiags
	}
	for k, v := range tlsParams {
		add(k, v)
	}

	return strings.Join(kv, " "), nil
}

// tlsDSNParams maps the optional tls {} block to libpq file-path parameters
// (sslrootcert/sslcert/sslkey). The Dialect stays a pure DSN string — no
// stdlib.RegisterConnConfig global state. Custom CA/cert are only meaningful when
// verifying (sslmode verify-ca/verify-full), but the params are harmless
// otherwise so they are always emitted when present.
//
// insecure_skip_verify has no libpq equivalent: combining it with a verify-*
// sslmode is contradictory and is rejected. For encryption without verification,
// use sslmode = "require".
func tlsDSNParams(config *cfg.Config, block *hcl.Block, def *postgresDef) (map[string]string, hcl.Diagnostics) {
	if def.TLS == nil {
		return nil, nil
	}

	verifying := def.SSLMode == "verify-ca" || def.SSLMode == "verify-full"
	if def.TLS.InsecureSkipVerify && verifying {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Contradictory TLS settings",
			Detail: fmt.Sprintf("insecure_skip_verify cannot be combined with sslmode = %q; "+
				"use sslmode = \"require\" for encryption without certificate verification", def.SSLMode),
			Subject: &def.TLS.DefRange,
		}}
	}

	out := map[string]string{}
	if def.TLS.CACert != "" {
		out["sslrootcert"] = resolvePath(config.BaseDir, def.TLS.CACert)
	}
	if def.TLS.Cert != "" {
		out["sslcert"] = resolvePath(config.BaseDir, def.TLS.Cert)
	}
	if def.TLS.Key != "" {
		out["sslkey"] = resolvePath(config.BaseDir, def.TLS.Key)
	}
	return out, nil
}

// resolvePath returns path as-is if absolute, otherwise joins it with baseDir.
// Cert paths are commonly absolute (/etc/ssl/...) and may live outside baseDir,
// so the contained-path SafeResolvePath check is intentionally not used here.
func resolvePath(baseDir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

// quoteValue applies libpq keyword/value quoting: a value is wrapped in single
// quotes (with backslash and single-quote escaped) when it is empty or contains a
// space, single quote, or backslash. Other values are emitted bare.
func quoteValue(v string) string {
	if v != "" && !strings.ContainsAny(v, " '\\") {
		return v
	}
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range v {
		if r == '\'' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// postgresDialect implements sqlengine.Dialect for Postgres.
type postgresDialect struct {
	dsn string
}

func (d postgresDialect) DriverName() string { return "pgx" }
func (d postgresDialect) DSN() string        { return d.dsn }
func (d postgresDialect) BindType() int      { return sqlx.DOLLAR }
func (d postgresDialect) Name() string       { return "postgres" }

// ClassifyError reports the Postgres SQLSTATE as both code and sqlstate.
func (d postgresDialect) ClassifyError(err error) (code, sqlstate string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.Code
	}
	return "", ""
}
