//go:build cgo

package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bytescty "github.com/tsarna/bytes-cty-type"
	richcty "github.com/tsarna/rich-cty-types"
	timecty "github.com/tsarna/time-cty-funcs"
	sqlengine "github.com/tsarna/vinculum/clients/sql"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// inlineClient is the subset of *sqlengine.SQLClient used by the inline (bare
// client) form. The concrete type lives in another package, so the test reaches
// it through these interface methods.
type inlineClient interface {
	Get(ctx context.Context, args []cty.Value) (cty.Value, error)
	Call(ctx context.Context, args []cty.Value) (cty.Value, error)
}

func buildClient(t *testing.T, vcl string) (*cfg.Config, inlineClient) {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	raw := config.Clients["sqlite"]["db"]
	require.NotNil(t, raw)
	sc, ok := raw.(*sqlengine.SQLClient)
	require.True(t, ok)
	require.NoError(t, sc.Start())
	t.Cleanup(func() { _ = sc.Stop() })
	return config, sc
}

func call(t *testing.T, c inlineClient, sql string, params ...cty.Value) cty.Value {
	t.Helper()
	res, err := c.Call(context.Background(), append([]cty.Value{cty.StringVal(sql)}, params...))
	require.NoError(t, err)
	return res
}

func get(t *testing.T, c inlineClient, sql string, params ...cty.Value) (cty.Value, error) {
	t.Helper()
	return c.Get(context.Background(), append([]cty.Value{cty.StringVal(sql)}, params...))
}

func requireNoResultError(t *testing.T, res cty.Value) {
	t.Helper()
	assert.True(t, res.GetAttr("error").IsNull(), "unexpected result.error: %#v", res.GetAttr("error"))
}

func intAttr(t *testing.T, v cty.Value, attr string) int64 {
	t.Helper()
	n, _ := v.GetAttr(attr).AsBigFloat().Int64()
	return n
}

const basicVCL = `
client "sqlite" "db" {
}
`

func TestInlineLifecycle(t *testing.T) {
	_, c := buildClient(t, basicVCL)

	// DDL
	res := call(t, c, `CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		score REAL,
		data BLOB,
		active BOOLEAN,
		created DATETIME
	)`)
	requireNoResultError(t, res)

	// Insert (exec): affected + last_insert_id
	res = call(t, c, "INSERT INTO users (name, score) VALUES (?, ?)", cty.StringVal("alice"), cty.NumberFloatVal(1.5))
	requireNoResultError(t, res)
	assert.Equal(t, int64(1), intAttr(t, res, "affected"))
	assert.Equal(t, int64(1), intAttr(t, res, "last_insert_id"))

	// get one + field access
	row, err := get(t, c, "SELECT id, name FROM users WHERE id = ?", cty.NumberIntVal(1))
	require.NoError(t, err)
	assert.Equal(t, "alice", row.GetAttr("name").AsString())
	assert.Equal(t, int64(1), func() int64 { n, _ := row.GetAttr("id").AsBigFloat().Int64(); return n }())

	// call many
	call(t, c, "INSERT INTO users (name) VALUES (?)", cty.StringVal("bob"))
	call(t, c, "INSERT INTO users (name) VALUES (?)", cty.StringVal("carol"))
	res = call(t, c, "SELECT * FROM users ORDER BY id")
	requireNoResultError(t, res)
	assert.Equal(t, int64(3), intAttr(t, res, "row_count"))
	assert.Equal(t, 3, res.GetAttr("rows").LengthInt())
}

// The default mode ("rw") must create the database file if it is missing —
// SQLite's URI mode=rw does not create, so the dialect maps it to mode=rwc.
func TestFileDatabaseCreatesIfMissing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	vcl := fmt.Sprintf(`
client "sqlite" "db" {
    path    = %q
    pragmas = { journal_mode = "WAL", foreign_keys = "ON" }
}
`, dbPath)

	_, c := buildClient(t, vcl)
	requireNoResultError(t, call(t, c, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"))
	requireNoResultError(t, call(t, c, "INSERT INTO t (v) VALUES (?)", cty.StringVal("hi")))

	row, err := get(t, c, "SELECT v FROM t WHERE id = 1")
	require.NoError(t, err)
	assert.Equal(t, "hi", row.GetAttr("v").AsString())

	_, statErr := os.Stat(dbPath)
	require.NoError(t, statErr, "database file should have been created on disk")
}

func TestNamedAndPositionalParams(t *testing.T) {
	_, c := buildClient(t, basicVCL)
	call(t, c, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	call(t, c, "INSERT INTO t (id, name) VALUES (1, 'alice')")
	call(t, c, "INSERT INTO t (id, name) VALUES (2, 'bob')")

	// named, with a repeated placeholder
	row, err := get(t, c, "SELECT * FROM t WHERE id = :id OR id = :id",
		cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(1)}))
	require.NoError(t, err)
	assert.Equal(t, "alice", row.GetAttr("name").AsString())

	// positional
	row, err = get(t, c, "SELECT * FROM t WHERE id = ? AND name = ?",
		cty.NumberIntVal(2), cty.StringVal("bob"))
	require.NoError(t, err)
	assert.Equal(t, "bob", row.GetAttr("name").AsString())
}

func TestErrorPath(t *testing.T) {
	_, c := buildClient(t, basicVCL)
	call(t, c, "CREATE TABLE t (id INTEGER PRIMARY KEY, email TEXT UNIQUE)")
	requireNoResultError(t, call(t, c, "INSERT INTO t (email) VALUES (?)", cty.StringVal("a@b.com")))

	// duplicate → constraint violation surfaces in result.error, not a Go error
	res := call(t, c, "INSERT INTO t (email) VALUES (?)", cty.StringVal("a@b.com"))
	errObj := res.GetAttr("error")
	require.False(t, errObj.IsNull(), "expected result.error to be populated")
	assert.Equal(t, "sqlite", errObj.GetAttr("driver").AsString())
	assert.NotEmpty(t, errObj.GetAttr("message").AsString())
	assert.NotEmpty(t, errObj.GetAttr("code").AsString())
	assert.True(t, res.GetAttr("row").IsNull())
}

func TestTypeMapping(t *testing.T) {
	_, c := buildClient(t, basicVCL)
	call(t, c, `CREATE TABLE t (
		i INTEGER, r REAL, s TEXT, b BLOB, n TEXT, dt DATETIME, ok BOOLEAN
	)`)
	call(t, c,
		"INSERT INTO t (i, r, s, b, n, dt, ok) VALUES (?, ?, ?, ?, ?, ?, ?)",
		cty.NumberIntVal(42),
		cty.NumberFloatVal(3.5),
		cty.StringVal("hello"),
		bytescty.BuildBytesObject([]byte{0x01, 0x02, 0x03}, "application/octet-stream"),
		cty.NullVal(cty.String),
		cty.StringVal("2024-01-02 03:04:05"),
		cty.True,
	)

	row, err := get(t, c, "SELECT * FROM t")
	require.NoError(t, err)

	assert.Equal(t, cty.Number, row.GetAttr("i").Type())
	assert.Equal(t, cty.Number, row.GetAttr("r").Type())
	assert.Equal(t, cty.String, row.GetAttr("s").Type())
	assert.True(t, row.GetAttr("n").IsNull())
	assert.Equal(t, cty.Bool, row.GetAttr("ok").Type())
	assert.True(t, row.GetAttr("ok").True())

	// BLOB → bytes object
	b, berr := bytescty.GetBytesFromValue(row.GetAttr("b"))
	require.NoError(t, berr)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, b.Data)

	// DATETIME → time capsule
	_, terr := timecty.GetTime(row.GetAttr("dt"))
	require.NoError(t, terr)
}

func TestGetExactlyOne(t *testing.T) {
	_, c := buildClient(t, basicVCL)
	call(t, c, "CREATE TABLE t (id INTEGER)")
	call(t, c, "INSERT INTO t VALUES (1)")
	call(t, c, "INSERT INTO t VALUES (2)")

	// zero rows → error
	_, err := get(t, c, "SELECT * FROM t WHERE id = 99")
	require.Error(t, err)

	// multiple rows → error
	_, err = get(t, c, "SELECT * FROM t")
	require.Error(t, err)
}

func TestMixingFormsIsCallSiteError(t *testing.T) {
	_, c := buildClient(t, basicVCL)
	_, err := c.Call(context.Background(), []cty.Value{
		cty.StringVal("SELECT * FROM t WHERE a = :a AND b = ?"),
		cty.ObjectVal(map[string]cty.Value{"a": cty.NumberIntVal(1)}),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mixes")
}

const namedQueryVCL = `
client "sqlite" "db" {
	query "by_id" {
		cardinality = "one"
		sql         = "SELECT id, name FROM t WHERE id = :id"
	}
	query "all" {
		cardinality = "many"
		sql         = "SELECT * FROM t"
	}
	query "insert_one" {
		cardinality = "exec"
		sql         = "INSERT INTO t (name) VALUES (:name)"
	}
	query "maybe_by_id" {
		cardinality = "zero_or_one"
		sql         = "SELECT id, name FROM t WHERE id = :id"
	}
	query "require_by_id" {
		cardinality = "zero_or_one"
		on_zero     = "error"
		sql         = "SELECT id, name FROM t WHERE id = :id"
	}
}
`

func TestNamedQueries(t *testing.T) {
	config, c := buildClient(t, namedQueryVCL)
	call(t, c, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	call(t, c, "INSERT INTO t (id, name) VALUES (1, 'alice')")

	clientVal := config.CtyClientMap["db"]

	// get(ctx, client.db.by_id, {id=1})
	byID := mustGettable(t, clientVal, "by_id")
	row, err := byID.Get(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(1)}),
	})
	require.NoError(t, err)
	assert.Equal(t, "alice", row.GetAttr("name").AsString())

	// get() on a "many" query → runtime cardinality error
	all := mustGettable(t, clientVal, "all")
	_, err = all.Get(context.Background(), []cty.Value{cty.EmptyObjectVal})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cardinality")

	// call() on the "exec" query → affected
	insert := mustCallable(t, clientVal, "insert_one")
	res, err := insert.Call(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("bob")}),
	})
	require.NoError(t, err)
	requireNoResultError(t, res)
	assert.Equal(t, int64(1), intAttr(t, res, "affected"))

	// zero_or_one, on_zero defaulting to "null": zero rows → null row, no error
	maybe := mustCallable(t, clientVal, "maybe_by_id")
	res, err = maybe.Call(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(999)}),
	})
	require.NoError(t, err)
	requireNoResultError(t, res)
	assert.True(t, res.GetAttr("row").IsNull())
	assert.Equal(t, int64(0), intAttr(t, res, "row_count"))

	// zero_or_one with on_zero = "error": zero rows → result.error populated
	require_ := mustCallable(t, clientVal, "require_by_id")
	res, err = require_.Call(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(999)}),
	})
	require.NoError(t, err)
	errObj := res.GetAttr("error")
	require.False(t, errObj.IsNull(), "expected on_zero=error to populate result.error")
	assert.Contains(t, errObj.GetAttr("message").AsString(), "no rows")

	// ...but a present row passes through cleanly
	res, err = require_.Call(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(1)}),
	})
	require.NoError(t, err)
	requireNoResultError(t, res)
	assert.Equal(t, "alice", res.GetAttr("row").GetAttr("name").AsString())
}

func mustGettable(t *testing.T, clientVal cty.Value, query string) richcty.Gettable {
	t.Helper()
	enc, err := richcty.GetCapsuleFromValue(clientVal.GetAttr(query))
	require.NoError(t, err)
	g, ok := enc.(richcty.Gettable)
	require.True(t, ok)
	return g
}

func mustCallable(t *testing.T, clientVal cty.Value, query string) richcty.Callable {
	t.Helper()
	enc, err := richcty.GetCapsuleFromValue(clientVal.GetAttr(query))
	require.NoError(t, err)
	c, ok := enc.(richcty.Callable)
	require.True(t, ok)
	return c
}
