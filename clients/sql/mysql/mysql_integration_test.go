//go:build integration

// Integration tests for the mysql client against a REAL MySQL server.
//
// Gated by the `integration` build tag AND by environment variables. With no env
// set every test skips, so the default
//
//	go test ./...
//
// is unaffected. To run against a database:
//
//	docker run --rm -e MYSQL_ROOT_PASSWORD=secret -e MYSQL_DATABASE=app -p 3306:3306 mysql
//	MYSQL_DSN="root:secret@tcp(localhost:3306)/app" \
//	    go test -tags=integration -run TestMy -v ./clients/sql/mysql/...
//
// Env vars consumed (MYSQL_DSN takes precedence; otherwise discrete fields):
//
//	MYSQL_DSN   full go-sql-driver DSN (absence of both this and MYSQL_HOST => skip)
//	MYSQL_HOST  host (default 127.0.0.1)
//	MYSQL_PORT  port (default 3306)
//	MYSQL_USER  user (default root)
//	MYSQL_PASS  password
//	MYSQL_DB    database (default app)
package mysql

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

// dsnFromEnv builds a DSN from MYSQL_DSN, or from the discrete MYSQL_* fields. It
// skips the test when no connection information is provided at all.
func dsnFromEnv(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		return dsn
	}
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		t.Skip("MYSQL_DSN / MYSQL_HOST not set; skipping MySQL integration test")
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s",
		getenvDef("MYSQL_USER", "root"), os.Getenv("MYSQL_PASS"),
		host, getenvDef("MYSQL_PORT", "3306"), getenvDef("MYSQL_DB", "app"))
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

// buildClient builds a mysql client from env, starts it, and creates a fresh
// scratch table (dropped on cleanup) so reruns are idempotent.
func buildClient(t *testing.T) (*cfg.Config, inlineClient) {
	t.Helper()
	logger, err := zap.NewDevelopment()
	require.NoError(t, err)

	vcl := fmt.Sprintf(`
client "mysql" "db" {
    dsn = %q

    query "by_id" {
        cardinality = "one"
        sql         = "SELECT id, name FROM my_smoke WHERE id = :id"
    }
    query "all" {
        cardinality = "many"
        sql         = "SELECT * FROM my_smoke ORDER BY id"
    }
    query "maybe" {
        cardinality = "zero_or_one"
        sql         = "SELECT id, name FROM my_smoke WHERE id = :id"
    }
    query "insert_one" {
        cardinality = "exec"
        sql         = "INSERT INTO my_smoke (name) VALUES (:name)"
    }
}
`, dsnFromEnv(t))

	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	sc, ok := config.Clients["mysql"]["db"].(*sqlengine.SQLClient)
	require.True(t, ok)
	require.NoError(t, sc.Start())
	t.Cleanup(func() { _ = sc.Stop() })

	call(t, sc, "DROP TABLE IF EXISTS my_smoke")
	requireNoResultError(t, call(t, sc, `CREATE TABLE my_smoke (
		id     BIGINT AUTO_INCREMENT PRIMARY KEY,
		name   VARCHAR(255) NOT NULL UNIQUE,
		score  DOUBLE,
		active TINYINT(1),
		data   BLOB,
		meta   JSON,
		created DATETIME
	)`))
	t.Cleanup(func() {
		_, _ = sc.Call(context.Background(), []cty.Value{cty.StringVal("DROP TABLE IF EXISTS my_smoke")})
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

func TestMyInlineLifecycleAndLastInsertID(t *testing.T) {
	_, c := buildClient(t)

	// Insert (exec): affected AND last_insert_id are set (MySQL's differentiator).
	res := call(t, c, "INSERT INTO my_smoke (name, score) VALUES (?, ?)",
		cty.StringVal("alice"), cty.NumberFloatVal(1.5))
	requireNoResultError(t, res)
	assert.Equal(t, int64(1), intAttr(t, res, "affected"))
	assert.False(t, res.GetAttr("last_insert_id").IsNull(), "mysql populates last_insert_id")
	assert.Equal(t, int64(1), intAttr(t, res, "last_insert_id"))

	// get one with positional param.
	row, err := get(t, c, "SELECT id, name FROM my_smoke WHERE name = ?", cty.StringVal("alice"))
	require.NoError(t, err)
	assert.Equal(t, "alice", row.GetAttr("name").AsString())

	// named params (repeated placeholder).
	call(t, c, "INSERT INTO my_smoke (name) VALUES (?)", cty.StringVal("bob"))
	row, err = get(t, c, "SELECT * FROM my_smoke WHERE name = :n OR name = :n",
		cty.ObjectVal(map[string]cty.Value{"n": cty.StringVal("bob")}))
	require.NoError(t, err)
	assert.Equal(t, "bob", row.GetAttr("name").AsString())

	// many.
	res = call(t, c, "SELECT * FROM my_smoke ORDER BY id")
	requireNoResultError(t, res)
	assert.Equal(t, int64(2), intAttr(t, res, "row_count"))
}

func TestMyTypeMapping(t *testing.T) {
	_, c := buildClient(t)
	call(t, c,
		"INSERT INTO my_smoke (name, score, active, data, meta, created) VALUES (?, ?, ?, ?, ?, ?)",
		cty.StringVal("typed"),
		cty.NumberFloatVal(3.5),
		cty.NumberIntVal(1),
		bytescty.BuildBytesObject([]byte{0x01, 0x02, 0x03}, "application/octet-stream"),
		cty.StringVal(`{"k":"v","n":7}`),
		cty.StringVal("2024-01-02 03:04:05"),
	)

	row, err := get(t, c, "SELECT * FROM my_smoke WHERE name = ?", cty.StringVal("typed"))
	require.NoError(t, err)

	assert.Equal(t, cty.Number, row.GetAttr("id").Type())
	assert.Equal(t, cty.Number, row.GetAttr("score").Type())
	// TINYINT(1) surfaces as a number (0/1), not bool — see scan.go / docs.
	assert.Equal(t, cty.Number, row.GetAttr("active").Type())

	// JSON → decoded object.
	meta := row.GetAttr("meta")
	require.True(t, meta.Type().IsObjectType(), "meta should decode to an object, got %s", meta.Type().FriendlyName())
	assert.Equal(t, "v", meta.GetAttr("k").AsString())

	// BLOB → bytes object.
	b, berr := bytescty.GetBytesFromValue(row.GetAttr("data"))
	require.NoError(t, berr)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, b.Data)

	// DATETIME (parseTime forced) → time capsule.
	_, terr := timecty.GetTime(row.GetAttr("created"))
	require.NoError(t, terr)
}

func TestMyErrorCodeAndSQLState(t *testing.T) {
	_, c := buildClient(t)
	requireNoResultError(t, call(t, c, "INSERT INTO my_smoke (name) VALUES (?)", cty.StringVal("dup")))

	// Duplicate key → constraint violation surfaces in result.error, not a Go error.
	res := call(t, c, "INSERT INTO my_smoke (name) VALUES (?)", cty.StringVal("dup"))
	errObj := res.GetAttr("error")
	require.False(t, errObj.IsNull(), "expected result.error to be populated")
	assert.Equal(t, "mysql", errObj.GetAttr("driver").AsString())
	assert.Equal(t, "1062", errObj.GetAttr("code").AsString())      // MySQL error number
	assert.Equal(t, "23000", errObj.GetAttr("sqlstate").AsString()) // ANSI SQLSTATE
	assert.True(t, res.GetAttr("row").IsNull())
}

func TestMyNamedQueriesAndCardinality(t *testing.T) {
	config, c := buildClient(t)
	call(t, c, "INSERT INTO my_smoke (name) VALUES (?)", cty.StringVal("alice"))

	clientVal := config.CtyClientMap["db"]

	// exec named query → affected + last_insert_id.
	ins := mustCallable(t, clientVal, "insert_one")
	res, err := ins.Call(context.Background(), []cty.Value{
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("bob")}),
	})
	require.NoError(t, err)
	requireNoResultError(t, res)
	assert.Equal(t, int64(1), intAttr(t, res, "affected"))

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
