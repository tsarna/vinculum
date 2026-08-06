package sqlite

// The decode struct and its schema live in this untagged file so that the
// block is described identically whether or not the build has cgo. A non-cgo
// build still registers the type — to report that SQLite was compiled out
// rather than that the type is unknown — and it should still document it.

import (
	"github.com/hashicorp/hcl/v2"
	sqlengine "github.com/tsarna/vinculum/clients/sql"
	cfg "github.com/tsarna/vinculum/config"
)

// sqliteDef decodes the SQLite-specific connection attributes. The remaining
// body (pool knobs + query blocks) is decoded by sqlengine.DecodeCommonDef.
type sqliteDef struct {
	Path        string         `hcl:"path,optional"`
	Mode        string         `hcl:"mode,optional"`         // rw|ro|rwc; default rw
	SharedCache bool           `hcl:"shared_cache,optional"` // default false
	Pragmas     hcl.Expression `hcl:"pragmas,optional"`

	Remain hcl.Body `hcl:",remain"`
}

var sqliteSchema = cfg.TypeSchema{
	Sample:      &sqliteDef{},
	AlsoSamples: []any{sqlengine.CommonSchema.Sample},
	Summary:     "A SQLite database client.",
	DocPage:     "client-sql.md#client-sqlite-name",
	Doc: `Opens a SQLite database file and exposes each ` + "`query`" + ` block as a callable
statement: ` + "`call(client.<name>, \"<query>\", args…)`" + `.

Requires a cgo-enabled build; the standard minimal image is built without cgo
and reports SQLite as unavailable.`,
	Attrs: cfg.MergeAttrs(sqlengine.CommonAttrs, map[string]cfg.AttrMeta{
		// A SQLite database is a file, not a server, so it pools far smaller
		// than the client/server dialects do.
		"max_open_conns": sqlengine.CommonAttrs["max_open_conns"].WithDefault("4"),
		"max_idle_conns": sqlengine.CommonAttrs["max_idle_conns"].WithDefault("4"),

		"path": {
			Summary: "Path to the database file.",
			Doc:     "Use `:memory:` for a database that lives only as long as the process.",
		},
		"mode": {
			Summary: "How to open the database.",
			Doc:     "`rw` requires it to exist, `rwc` creates it if missing, `ro` opens it read-only.",
			Enum:    []string{"rw", "ro", "rwc"},
		},
		"shared_cache": {
			Summary: "Share one page cache across connections.",
			Hint:    cfg.HintBool,
		},
		"pragmas": {
			Summary: "PRAGMA statements applied to each new connection.",
			Doc:     "A map of pragma name to value, for example `{ journal_mode = \"WAL\" }`.",
		},
	}),
	Blocks: sqlengine.CommonSchema.Blocks,
}
