package mysql

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/hcl/v2"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

func intPtr(i int) *int { return &i }

func testBlock() *hcl.Block {
	return &hcl.Block{
		Type:   "client",
		Labels: []string{"mysql", "db"},
	}
}

func TestBuildDSNPassthroughForcesParseTime(t *testing.T) {
	// A dsn with no parseTime/loc gets them forced; round-trips through ParseDSN.
	def := &mysqlDef{DSN: "user:pass@tcp(db.internal:3306)/app"}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	require.False(t, diags.HasErrors(), diags.Error())

	mc, err := mysql.ParseDSN(got)
	require.NoError(t, err)
	assert.True(t, mc.ParseTime, "parseTime should be forced on")
	assert.Equal(t, "app", mc.DBName)
	assert.Equal(t, "db.internal:3306", mc.Addr)
}

func TestBuildDSNPassthroughHonorsOverride(t *testing.T) {
	// An explicit parseTime=false in the dsn is preserved.
	def := &mysqlDef{DSN: "user:pass@tcp(h:3306)/app?parseTime=false"}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	require.False(t, diags.HasErrors(), diags.Error())

	mc, err := mysql.ParseDSN(got)
	require.NoError(t, err)
	assert.False(t, mc.ParseTime, "explicit parseTime=false must be preserved")
}

func TestBuildDSNDiscreteDefaults(t *testing.T) {
	def := &mysqlDef{User: "alice", Database: "appdb"}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	require.False(t, diags.HasErrors(), diags.Error())

	mc, err := mysql.ParseDSN(got)
	require.NoError(t, err)
	assert.Equal(t, "alice", mc.User)
	assert.Equal(t, "tcp", mc.Net)
	assert.Equal(t, "localhost:3306", mc.Addr)
	assert.Equal(t, "appdb", mc.DBName)
	assert.True(t, mc.ParseTime)
}

func TestBuildDSNDiscreteOverrides(t *testing.T) {
	def := &mysqlDef{
		Host:     "db.internal",
		Port:     intPtr(3307),
		User:     "alice",
		Password: "s3cr3t",
		Database: "appdb",
	}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	require.False(t, diags.HasErrors(), diags.Error())

	mc, err := mysql.ParseDSN(got)
	require.NoError(t, err)
	assert.Equal(t, "db.internal:3307", mc.Addr)
	assert.Equal(t, "s3cr3t", mc.Passwd)
}

func TestBuildDSNMissingRequired(t *testing.T) {
	for _, def := range []*mysqlDef{
		{User: "alice"},     // missing database
		{Database: "appdb"}, // missing user
		{},                  // missing both
	} {
		_, diags := buildDSN(&cfg.Config{}, testBlock(), def)
		assert.True(t, diags.HasErrors(), "expected error for def %+v", def)
	}
}

func TestBuildDSNTLSRegistersAndReferences(t *testing.T) {
	def := &mysqlDef{
		User:     "alice",
		Database: "appdb",
		TLS:      &cfg.TLSConfig{Enabled: true, InsecureSkipVerify: true},
	}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	require.False(t, diags.HasErrors(), diags.Error())

	mc, err := mysql.ParseDSN(got)
	require.NoError(t, err)
	assert.Equal(t, "vinculum-mysql-db", mc.TLSConfig, "dsn should reference the registered tls config by name")
}

func TestBuildDSNDisabledTLSNotRegistered(t *testing.T) {
	def := &mysqlDef{
		User:     "alice",
		Database: "appdb",
		TLS:      &cfg.TLSConfig{Enabled: false},
	}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	require.False(t, diags.HasErrors(), diags.Error())

	mc, err := mysql.ParseDSN(got)
	require.NoError(t, err)
	assert.Empty(t, mc.TLSConfig, "a disabled tls block should leave the connection unencrypted")
}

func TestDialect(t *testing.T) {
	d := mysqlDialect{dsn: "x"}
	assert.Equal(t, "mysql", d.DriverName())
	assert.Equal(t, "mysql", d.Name())
	assert.Equal(t, sqlx.QUESTION, d.BindType())
	assert.Equal(t, "x", d.DSN())
}

func TestClassifyError(t *testing.T) {
	d := mysqlDialect{}

	code, sqlstate := d.ClassifyError(&mysql.MySQLError{
		Number:   1062,
		SQLState: [5]byte{'2', '3', '0', '0', '0'},
		Message:  "Duplicate entry",
	})
	assert.Equal(t, "1062", code)
	assert.Equal(t, "23000", sqlstate)

	code, sqlstate = d.ClassifyError(errors.New("plain error"))
	assert.Empty(t, code)
	assert.Empty(t, sqlstate)
}

// TestProcessRegistersClient exercises the full decode → register path through
// the public config API without a database (Build does not open a connection;
// Start does). Runs in any build, no cgo and no live MySQL.
func TestProcessRegistersClient(t *testing.T) {
	const vcl = `
client "mysql" "db" {
    dsn = "user:pass@tcp(localhost:3306)/app"

    max_open_conns = 7

    query "by_id" {
        cardinality = "one"
        sql         = "SELECT id FROM t WHERE id = :id"
    }
}
`
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	require.NotNil(t, config.Clients["mysql"]["db"], "mysql client should be registered")

	clientVal := config.CtyClientMap["db"]
	require.False(t, clientVal.IsNull())
	assert.False(t, clientVal.GetAttr("by_id").IsNull(), "named query by_id should be exposed")
}
