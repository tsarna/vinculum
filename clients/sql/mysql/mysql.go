// Package mysql registers the `client "mysql"` block. Like Postgres it is pure
// Go (the go-sql-driver/mysql driver needs no cgo), so there is no build-tag
// split and it is available in both the full and minimal container images.
//
// It supplies a sqlengine.Dialect plus DSN assembly and otherwise reuses the
// dialect-agnostic engine in clients/sql for pooling, named queries, parameter
// binding, row scanning, and the result object. See doc/client-sql.md.
package mysql

import (
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/jmoiron/sqlx"
	sqlengine "github.com/tsarna/vinculum/clients/sql"
	cfg "github.com/tsarna/vinculum/config"
)

func init() {
	cfg.RegisterClientType("mysql", process)
}

// mysqlPoolDefaults are the MySQL-flavored pool defaults from the spec's Pooling
// table: moderate concurrency, unlimited lifetimes.
var mysqlPoolDefaults = sqlengine.PoolDefaults{
	MaxOpenConns:    25,
	MaxIdleConns:    5,
	ConnMaxLifetime: 0,
	ConnMaxIdleTime: 0,
}

// mysqlDef decodes the MySQL-specific connection attributes. The remaining body
// (pool knobs + query blocks) is decoded by sqlengine.DecodeCommonDef.
//
// Either dsn or the discrete host/port/user/... fields are used; dsn wins if both
// are present. MySQL has no sslmode/search_path; TLS is configured via the tls
// block (enabled = true).
type mysqlDef struct {
	DSN      string         `hcl:"dsn,optional"`
	Host     string         `hcl:"host,optional"` // default "localhost"
	Port     *int           `hcl:"port,optional"` // default 3306
	User     string         `hcl:"user,optional"` // required when dsn unset
	Password string         `hcl:"password,optional"`
	Database string         `hcl:"database,optional"` // required when dsn unset
	TLS      *cfg.TLSConfig `hcl:"tls,block"`

	Remain hcl.Body `hcl:",remain"`
}

func process(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Client, hcl.Diagnostics) {
	var def mysqlDef
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

	return sqlengine.ProcessCommon(config, block, mysqlDialect{dsn: dsn}, common, mysqlPoolDefaults)
}

// buildDSN assembles a go-sql-driver DSN via the driver's own mysql.Config (which
// owns all escaping). parseTime=true and loc=UTC are forced so DATETIME/TIMESTAMP
// columns map to cty time values rather than strings — unless the user overrides
// them through an explicit dsn.
//
// When def.DSN is set it is parsed, the parseTime/loc defaults are applied if the
// user left them unset, and it is re-formatted (the dsn wins over the discrete
// fields and the tls block).
func buildDSN(config *cfg.Config, block *hcl.Block, def *mysqlDef) (string, hcl.Diagnostics) {
	if def.DSN != "" {
		mc, err := mysql.ParseDSN(def.DSN)
		if err != nil {
			return "", hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid mysql dsn",
				Detail:   err.Error(),
				Subject:  &block.DefRange,
			}}
		}
		// Force parseTime only when the dsn does not mention it, so an explicit
		// parseTime=false override is honored. ParseDSN already defaults loc=UTC.
		if !strings.Contains(def.DSN, "parseTime") {
			mc.ParseTime = true
		}
		return mc.FormatDSN(), nil
	}

	if def.User == "" || def.Database == "" {
		return "", hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing mysql connection settings",
			Detail:   "either dsn or both user and database must be set",
			Subject:  &block.DefRange,
		}}
	}

	host := def.Host
	if host == "" {
		host = "localhost"
	}
	port := 3306
	if def.Port != nil {
		port = *def.Port
	}

	mc := mysql.NewConfig() // Loc=UTC and other sane defaults
	mc.User = def.User
	mc.Passwd = def.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	mc.DBName = def.Database
	mc.ParseTime = true

	if diags := applyTLS(config, block, def, mc); diags.HasErrors() {
		return "", diags
	}

	return mc.FormatDSN(), nil
}

// applyTLS registers a *tls.Config built from the optional tls {} block and points
// the connection config at it by name. go-sql-driver requires a registered config
// referenced by name in the DSN (a *tls.Config cannot be serialized into the DSN
// string), unlike Postgres's libpq file-path parameters. The shared helper honors
// ca_cert/cert/key and insecure_skip_verify, and returns nil when the block is
// absent or not enabled, in which case the connection is left unencrypted.
func applyTLS(config *cfg.Config, block *hcl.Block, def *mysqlDef, mc *mysql.Config) hcl.Diagnostics {
	if def.TLS == nil {
		return nil
	}
	tlsCfg, err := def.TLS.BuildTLSClientConfig(config.BaseDir)
	if err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid tls configuration",
			Detail:   err.Error(),
			Subject:  &def.TLS.DefRange,
		}}
	}
	if tlsCfg == nil {
		return nil // tls block present but not enabled
	}

	name := "vinculum-mysql-" + block.Labels[1]
	if err := mysql.RegisterTLSConfig(name, tlsCfg); err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Could not register mysql tls configuration",
			Detail:   err.Error(),
			Subject:  &def.TLS.DefRange,
		}}
	}
	mc.TLSConfig = name
	return nil
}

// mysqlDialect implements sqlengine.Dialect for MySQL.
type mysqlDialect struct {
	dsn string
}

func (d mysqlDialect) DriverName() string { return "mysql" }
func (d mysqlDialect) DSN() string        { return d.dsn }
func (d mysqlDialect) BindType() int      { return sqlx.QUESTION }
func (d mysqlDialect) Name() string       { return "mysql" }

// ClassifyError reports the driver-native MySQL error number as the code (e.g.
// "1062" for a duplicate key) and the ANSI SQLSTATE as sqlstate (e.g. "23000").
func (d mysqlDialect) ClassifyError(err error) (code, sqlstate string) {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return strconv.Itoa(int(me.Number)), strings.Trim(string(me.SQLState[:]), "\x00")
	}
	return "", ""
}
