//go:build integration

// Integration tests for the postgres client against a REAL Postgres server.
//
// Gated by the `integration` build tag AND by environment variables. With no env
// set every test skips, so the default
//
//	go test ./...
//
// is unaffected. To run against a database:
//
//	docker run --rm -e POSTGRES_PASSWORD=secret -p 5432:5432 postgres
//	PG_DSN="postgres://postgres:secret@localhost:5432/postgres?sslmode=disable" \
//	    go test -tags=integration -run TestPG -v ./clients/sql/postgres/...
//
// Env vars consumed (PG_DSN takes precedence; otherwise discrete fields):
//
//	PG_DSN   full libpq/URL DSN (absence of both this and PG_HOST => skip)
//	PG_HOST  host (default localhost)
//	PG_PORT  port (default 5432)
//	PG_USER  user (default postgres)
//	PG_PASS  password
//	PG_DB    database (default postgres)
package postgres

import (
	"context"
	"fmt"
	"os"
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

// dsnFromEnv builds a DSN from PG_DSN, or from the discrete PG_* fields. It skips
// the test when no connection information is provided at all.
func dsnFromEnv(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("PG_DSN"); dsn != "" {
		return dsn
	}
	host := os.Getenv("PG_HOST")
	if host == "" {
		t.Skip("PG_DSN / PG_HOST not set; skipping Postgres integration test")
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		getenvDef("PG_USER", "postgres"), os.Getenv("PG_PASS"),
		host, getenvDef("PG_PORT", "5432"), getenvDef("PG_DB", "postgres"))
}

func getenvDef(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type inlineClient interface {
	Get(ctx context.Context, args []cty.Value) (cty.Value, error)
	Call(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// buildClient builds a postgres client from env, starts it, and creates a fresh
// scratch table (dropped on cleanup) so reruns are idempotent.
func buildClient(t *testing.T) (*cfg.Config, inlineClient) {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	vcl := fmt.Sprintf(`
client "postgres" "db" {
    dsn = %q

    query "by_id" {
        cardinality = "one"
        sql         = "SELECT id, name FROM pg_smoke WHERE id = :id"
    }
    query "all" {
        cardinality = "many"
        sql         = "SELECT * FROM pg_smoke ORDER BY id"
    }
    query "maybe" {
        cardinality = "zero_or_one"
        sql         = "SELECT id, name FROM pg_smoke WHERE id = :id"
    }
    query "insert_returning" {
        cardinality = "one"
        sql         = "INSERT INTO pg_smoke (name) VALUES (:name) RETURNING id, name"
    }
}
`, dsnFromEnv(t))

	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	sc, ok := config.Clients["postgres"]["db"].(*sqlengine.SQLClient)
	require.True(t, ok)
	require.NoError(t, sc.Start())
	t.Cleanup(func() { _ = sc.Stop() })

	call(t, sc, "DROP TABLE IF EXISTS pg_smoke")
	requireNoResultError(t, call(t, sc, `CREATE TABLE pg_smoke (
		id     BIGSERIAL PRIMARY KEY,
		name   TEXT NOT NULL UNIQUE,
		score  DOUBLE PRECISION,
		active BOOLEAN,
		data   BYTEA,
		meta   JSONB,
		created TIMESTAMPTZ
	)`))
	t.Cleanup(func() {
		_, _ = sc.Call(context.Background(), []cty.Value{cty.StringVal("DROP TABLE IF EXISTS pg_smoke")})
	})

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

func TestPGInlineLifecycleAndRebind(t *testing.T) {
	_, c := buildClient(t)

	// Insert (exec): affected set; last_insert_id is null on Postgres.
	res := call(t, c, "INSERT INTO pg_smoke (name, score) VALUES (?, ?)",
		cty.StringVal("alice"), cty.NumberFloatVal(1.5))
	requireNoResultError(t, res)
	assert.Equal(t, int64(1), intAttr(t, res, "affected"))
	assert.True(t, res.GetAttr("last_insert_id").IsNull(), "postgres has no last_insert_id")

	// get one with positional param (? rebinds to $1).
	row, err := get(t, c, "SELECT id, name FROM pg_smoke WHERE name = ?", cty.StringVal("alice"))
	require.NoError(t, err)
	assert.Equal(t, "alice", row.GetAttr("name").AsString())

	// named params (repeated placeholder).
	call(t, c, "INSERT INTO pg_smoke (name) VALUES (?)", cty.StringVal("bob"))
	row, err = get(t, c, "SELECT * FROM pg_smoke WHERE name = :n OR name = :n",
		cty.ObjectVal(map[string]cty.Value{"n": cty.StringVal("bob")}))
	require.NoError(t, err)
	assert.Equal(t, "bob", row.GetAttr("name").AsString())

	// many.
	res = call(t, c, "SELECT * FROM pg_smoke ORDER BY id")
	requireNoResultError(t, res)
	assert.Equal(t, int64(2), intAttr(t, res, "row_count"))
}

func TestPGTypeMapping(t *testing.T) {
	_, c := buildClient(t)
	call(t, c,
		"INSERT INTO pg_smoke (name, score, active, data, meta, created) VALUES (?, ?, ?, ?, ?, ?)",
		cty.StringVal("typed"),
		cty.NumberFloatVal(3.5),
		cty.True,
		bytescty.BuildBytesObject([]byte{0x01, 0x02, 0x03}, "application/octet-stream"),
		cty.StringVal(`{"k":"v","n":7}`),
		cty.StringVal("2024-01-02T03:04:05Z"),
	)

	row, err := get(t, c, "SELECT * FROM pg_smoke WHERE name = ?", cty.StringVal("typed"))
	require.NoError(t, err)

	assert.Equal(t, cty.Number, row.GetAttr("id").Type())
	assert.Equal(t, cty.Number, row.GetAttr("score").Type())
	assert.Equal(t, cty.Bool, row.GetAttr("active").Type())
	assert.True(t, row.GetAttr("active").True())

	// JSONB → decoded object.
	meta := row.GetAttr("meta")
	require.True(t, meta.Type().IsObjectType(), "meta should decode to an object, got %s", meta.Type().FriendlyName())
	assert.Equal(t, "v", meta.GetAttr("k").AsString())

	// BYTEA → bytes object.
	b, berr := bytescty.GetBytesFromValue(row.GetAttr("data"))
	require.NoError(t, berr)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, b.Data)

	// TIMESTAMPTZ → time capsule.
	_, terr := timecty.GetTime(row.GetAttr("created"))
	require.NoError(t, terr)
}

func TestPGErrorSQLState(t *testing.T) {
	_, c := buildClient(t)
	requireNoResultError(t, call(t, c, "INSERT INTO pg_smoke (name) VALUES (?)", cty.StringVal("dup")))

	// Duplicate key → constraint violation surfaces in result.error, not a Go error.
	res := call(t, c, "INSERT INTO pg_smoke (name) VALUES (?)", cty.StringVal("dup"))
	errObj := res.GetAttr("error")
	require.False(t, errObj.IsNull(), "expected result.error to be populated")
	assert.Equal(t, "postgres", errObj.GetAttr("driver").AsString())
	assert.Equal(t, "23505", errObj.GetAttr("sqlstate").AsString())
	assert.Equal(t, "23505", errObj.GetAttr("code").AsString())
	assert.True(t, res.GetAttr("row").IsNull())
}

func TestPGNamedQueriesAndReturning(t *testing.T) {
	config, c := buildClient(t)
	call(t, c, "INSERT INTO pg_smoke (name) VALUES (?)", cty.StringVal("alice"))

	clientVal := config.CtyClientMap["db"]

	// RETURNING via a "one" named query yields the generated id.
	ins := mustCallable(t, clientVal, "insert_returning")
	res, err := ins.Call(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("bob")}),
	})
	require.NoError(t, err)
	requireNoResultError(t, res)
	assert.Equal(t, "bob", res.GetAttr("row").GetAttr("name").AsString())
	assert.Equal(t, cty.Number, res.GetAttr("row").GetAttr("id").Type())

	// get() on a "many" query → runtime cardinality error.
	all := mustGettable(t, clientVal, "all")
	_, err = all.Get(context.Background(), []cty.Value{cty.EmptyObjectVal})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cardinality")

	// zero_or_one, zero rows → null row, no error.
	maybe := mustCallable(t, clientVal, "maybe")
	res, err = maybe.Call(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"id": cty.NumberIntVal(999999)}),
	})
	require.NoError(t, err)
	requireNoResultError(t, res)
	assert.True(t, res.GetAttr("row").IsNull())
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
