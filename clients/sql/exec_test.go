package sql

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// stubDialect lets the param-binding logic be tested without a real driver, so
// these tests run in a minimal (CGO_ENABLED=0) build too.
type stubDialect struct{ bind int }

func (d stubDialect) DriverName() string                   { return "stub" }
func (d stubDialect) DSN() string                          { return "" }
func (d stubDialect) BindType() int                        { return d.bind }
func (d stubDialect) Name() string                         { return "stub" }
func (d stubDialect) ClassifyError(error) (string, string) { return "", "" }

func newStubClient(bind int) *SQLClient {
	return &SQLClient{dialect: stubDialect{bind: bind}}
}

func TestBindParamsEmpty(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	sql, args, err := c.bindParams("SELECT 1", nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1", sql)
	assert.Empty(t, args)
}

func TestBindParamsPositionalQuestion(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	sql, args, err := c.bindParams(
		"SELECT * FROM t WHERE id = ? AND active = ?",
		[]cty.Value{cty.NumberIntVal(42), cty.True},
	)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM t WHERE id = ? AND active = ?", sql)
	assert.Equal(t, []any{42, true}, args)
}

func TestBindParamsPositionalDollarRebind(t *testing.T) {
	c := newStubClient(sqlx.DOLLAR)
	sql, args, err := c.bindParams(
		"SELECT * FROM t WHERE id = ?",
		[]cty.Value{cty.NumberIntVal(7)},
	)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM t WHERE id = $1", sql)
	assert.Equal(t, []any{7}, args)
}

func TestBindParamsNamed(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	params := cty.ObjectVal(map[string]cty.Value{
		"id":     cty.NumberIntVal(42),
		"active": cty.True,
	})
	sql, args, err := c.bindParams(
		"SELECT * FROM t WHERE id = :id AND active = :active",
		[]cty.Value{params},
	)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM t WHERE id = ? AND active = ?", sql)
	assert.Equal(t, []any{42, true}, args)
}

func TestBindParamsNamedRepeated(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	params := cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(5)})
	sql, args, err := c.bindParams(
		"SELECT * FROM t WHERE id = :id OR parent_id = :id",
		[]cty.Value{params},
	)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM t WHERE id = ? OR parent_id = ?", sql)
	assert.Equal(t, []any{5, 5}, args)
}

func TestBindParamsMixingNamedWithPositional(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	params := cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(1)})
	_, _, err := c.bindParams("SELECT * FROM t WHERE id = :id AND x = ?", []cty.Value{params})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mixes")
}

func TestBindParamsPositionalWithNamedPlaceholders(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	_, _, err := c.bindParams("SELECT * FROM t WHERE id = :id", []cty.Value{cty.NumberIntVal(1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mixes")
}

func TestBindParamsNamedPlaceholdersNoParams(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	_, _, err := c.bindParams("SELECT * FROM t WHERE id = :id", nil)
	require.Error(t, err)
}

// Postgres `::type` casts must not be mistaken for :name placeholders. In the
// no-param and positional paths the cast is passed through untouched (Rebind only
// rewrites `?`), so these bind cleanly rather than failing with a spurious
// "named placeholders" / "mixes" error.
func TestBindParamsCastsAreNotNamedPlaceholders(t *testing.T) {
	c := newStubClient(sqlx.DOLLAR)

	// no params, inline casts (the case the smoke test hits)
	sql, args, err := c.bindParams(`SELECT '{"k":"v"}'::jsonb AS j, '\x48'::bytea AS b`, nil)
	require.NoError(t, err)
	assert.Equal(t, `SELECT '{"k":"v"}'::jsonb AS j, '\x48'::bytea AS b`, sql)
	assert.Empty(t, args)

	// positional param alongside a cast
	sql, args, err = c.bindParams("SELECT id::text FROM t WHERE id = ?", []cty.Value{cty.NumberIntVal(7)})
	require.NoError(t, err)
	assert.Equal(t, "SELECT id::text FROM t WHERE id = $1", sql)
	assert.Equal(t, []any{7}, args)
}

// In the NAMED-param path, sqlx.Named treats `::` as an escaped literal colon and
// collapses it to a single `:`, which breaks a Postgres `::type` cast. This test
// pins that documented sqlx behavior so the limitation (use CAST(x AS type) in
// named queries) is not silently regressed or "fixed" by accident.
func TestBindParamsCastIsMangledInNamedPath(t *testing.T) {
	c := newStubClient(sqlx.DOLLAR)
	params := cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(5)})
	sql, _, err := c.bindParams("SELECT id::text FROM t WHERE id = :id", []cty.Value{params})
	require.NoError(t, err)
	assert.Equal(t, "SELECT id:text FROM t WHERE id = $1", sql) // note the single colon
}

func TestBindParamsNullParam(t *testing.T) {
	c := newStubClient(sqlx.QUESTION)
	_, args, err := c.bindParams("INSERT INTO t VALUES (?)", []cty.Value{cty.NullVal(cty.String)})
	require.NoError(t, err)
	assert.Equal(t, []any{nil}, args)
}

// A client whose Start() never ran (or failed) has a nil *sql.DB. Calls must
// return a clean error rather than panic on the nil dereference.
func TestNilDBReturnsErrorNotPanic(t *testing.T) {
	c := newStubClient(sqlx.QUESTION) // db is nil

	_, err := c.callStmt(context.Background(), "SELECT 1", nil, 0, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")

	_, err = c.getOneRow(context.Background(), "SELECT 1", nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
}

func TestReturnsRows(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", true},
		{"  select * from t", true},
		{"WITH x AS (...) SELECT *", true},
		{"VALUES (1)", true},
		{"PRAGMA foreign_keys", true},
		{"INSERT INTO t VALUES (1)", false},
		{"UPDATE t SET x = 1", false},
		{"DELETE FROM t", false},
		{"CREATE TABLE t (id INTEGER)", false},
	} {
		assert.Equalf(t, tc.want, returnsRows(tc.sql), "sql=%q", tc.sql)
	}
}
