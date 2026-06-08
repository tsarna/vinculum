//go:build cgo

// Package sqlite registers the `client "sqlite"` block. It requires a
// cgo-enabled build (the mattn/go-sqlite3 driver links against SQLite via cgo).
// On a non-cgo build, sqlite_nocgo.go registers a stub that reports a clear
// "not compiled in" error instead.
package sqlite

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/jmoiron/sqlx"
	sqlite3 "github.com/mattn/go-sqlite3"
	sqlengine "github.com/tsarna/vinculum/clients/sql"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("sqlite", process)
}

// sqlitePoolDefaults are the SQLite-flavored pool defaults: modest concurrency,
// idle == open, unlimited lifetimes.
var sqlitePoolDefaults = sqlengine.PoolDefaults{
	MaxOpenConns:    4,
	MaxIdleConns:    4,
	ConnMaxLifetime: 0,
	ConnMaxIdleTime: 0,
}

// sqliteDef decodes the SQLite-specific connection attributes. The remaining
// body (pool knobs + query blocks) is decoded by sqlengine.DecodeCommonDef.
type sqliteDef struct {
	Path        string         `hcl:"path,optional"`
	Mode        string         `hcl:"mode,optional"`         // rw|ro|rwc; default rw
	SharedCache bool           `hcl:"shared_cache,optional"` // default false
	Pragmas     hcl.Expression `hcl:"pragmas,optional"`

	Remain hcl.Body `hcl:",remain"`
}

func process(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Client, hcl.Diagnostics) {
	var def sqliteDef
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

	return sqlengine.ProcessCommon(config, block, sqliteDialect{dsn: dsn}, common, sqlitePoolDefaults)
}

// buildDSN assembles a go-sqlite3 DSN. An in-memory database is forced to
// shared-cache form so the connection pool sees a single shared database
// rather than one empty database per connection.
func buildDSN(config *cfg.Config, block *hcl.Block, def *sqliteDef) (string, hcl.Diagnostics) {
	path := def.Path
	if path == "" {
		path = ":memory:"
	}

	params := []string{}

	memory := path == ":memory:" || strings.HasPrefix(path, "file::memory:")
	var base string
	if memory {
		base = "file::memory:"
		params = append(params, "cache=shared")
	} else {
		base = "file:" + path
		mode := def.Mode
		if mode == "" {
			mode = "rw"
		}
		switch mode {
		case "rw", "ro", "rwc":
		default:
			return "", hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid sqlite mode",
				Detail:   fmt.Sprintf("mode must be \"rw\", \"ro\", or \"rwc\"; got %q", mode),
				Subject:  &block.DefRange,
			}}
		}
		params = append(params, "mode="+mode)
		if def.SharedCache {
			params = append(params, "cache=shared")
		}
	}

	pragmaParams, pDiags := pragmaParams(config, def, block)
	if pDiags.HasErrors() {
		return "", pDiags
	}
	params = append(params, pragmaParams...)

	if len(params) == 0 {
		return base, nil
	}
	return base + "?" + strings.Join(params, "&"), nil
}

// pragmaParams turns the pragmas map into go-sqlite3 _pragma=KEY(VALUE) DSN
// parameters applied on each new connection.
func pragmaParams(config *cfg.Config, def *sqliteDef, block *hcl.Block) ([]string, hcl.Diagnostics) {
	if def.Pragmas == nil {
		return nil, nil
	}
	val, diags := def.Pragmas.Value(config.EvalCtx())
	if diags.HasErrors() {
		return nil, diags
	}
	if val.IsNull() {
		return nil, nil
	}
	if !val.Type().IsObjectType() && !val.Type().IsMapType() {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid pragmas",
			Detail:   "pragmas must be an object of name = value pairs",
			Subject:  &block.DefRange,
		}}
	}

	var out []string
	for it := val.ElementIterator(); it.Next(); {
		k, v := it.Element()
		out = append(out, fmt.Sprintf("_pragma=%s(%s)", k.AsString(), pragmaValueString(v)))
	}
	return out, nil
}

func pragmaValueString(v cty.Value) string {
	if v.IsNull() {
		return ""
	}
	switch v.Type() {
	case cty.String:
		return v.AsString()
	case cty.Bool:
		if v.True() {
			return "ON"
		}
		return "OFF"
	case cty.Number:
		bf := v.AsBigFloat()
		if i, acc := bf.Int64(); acc == 0 {
			return strconv.FormatInt(i, 10)
		}
		return bf.Text('f', -1)
	default:
		return ""
	}
}

// sqliteDialect implements sqlengine.Dialect for SQLite.
type sqliteDialect struct {
	dsn string
}

func (d sqliteDialect) DriverName() string { return "sqlite3" }
func (d sqliteDialect) DSN() string        { return d.dsn }
func (d sqliteDialect) BindType() int      { return sqlx.QUESTION }
func (d sqliteDialect) Name() string       { return "sqlite" }

// ClassifyError reports the SQLite extended result code as the error code.
// SQLite has no SQLSTATE, so sqlstate is always empty.
func (d sqliteDialect) ClassifyError(err error) (code, sqlstate string) {
	var serr sqlite3.Error
	if errors.As(err, &serr) {
		return strconv.Itoa(int(serr.ExtendedCode)), ""
	}
	return "", ""
}
