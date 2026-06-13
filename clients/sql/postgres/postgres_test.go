package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/jackc/pgx/v5/pgconn"
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
		Labels: []string{"postgres", "db"},
	}
}

func TestBuildDSNPassthrough(t *testing.T) {
	// dsn wins over discrete fields and is emitted verbatim.
	def := &postgresDef{
		DSN:      "postgres://u:p@h:5432/db?sslmode=require",
		Host:     "ignored",
		User:     "ignored",
		Database: "ignored",
	}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	if diags.HasErrors() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if got != def.DSN {
		t.Fatalf("dsn passthrough: got %q want %q", got, def.DSN)
	}
}

func TestBuildDSNDiscreteDefaults(t *testing.T) {
	def := &postgresDef{User: "alice", Database: "appdb"}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	if diags.HasErrors() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	for _, want := range []string{"host=localhost", "port=5432", "user=alice", "dbname=appdb"} {
		if !strings.Contains(got, want) {
			t.Errorf("dsn %q missing %q", got, want)
		}
	}
	// No password supplied → no password key.
	if strings.Contains(got, "password=") {
		t.Errorf("dsn %q should not contain password", got)
	}
}

func TestBuildDSNDiscreteOverrides(t *testing.T) {
	def := &postgresDef{
		Host:       "db.internal",
		Port:       intPtr(6432),
		User:       "alice",
		Password:   "secret",
		Database:   "appdb",
		SSLMode:    "require",
		SearchPath: "myschema, public",
	}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	if diags.HasErrors() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	for _, want := range []string{
		"host=db.internal", "port=6432", "user=alice", "password=secret",
		"dbname=appdb", "sslmode=require", "search_path='myschema, public'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dsn %q missing %q", got, want)
		}
	}
}

func TestBuildDSNPasswordEscaping(t *testing.T) {
	def := &postgresDef{User: "alice", Database: "appdb", Password: `p a's\w`}
	got, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	if diags.HasErrors() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	// Space, single-quote, and backslash force quoting with backslash escaping.
	want := `password='p a\'s\\w'`
	if !strings.Contains(got, want) {
		t.Errorf("dsn %q missing escaped %q", got, want)
	}
}

func TestBuildDSNMissingRequired(t *testing.T) {
	for _, def := range []*postgresDef{
		{User: "alice"},     // missing database
		{Database: "appdb"}, // missing user
		{},                  // missing both
	} {
		_, diags := buildDSN(&cfg.Config{}, testBlock(), def)
		if !diags.HasErrors() {
			t.Errorf("expected error for def %+v", def)
		}
	}
}

func TestBuildDSNTLSParams(t *testing.T) {
	def := &postgresDef{
		User:     "alice",
		Database: "appdb",
		SSLMode:  "verify-full",
		TLS: &cfg.TLSConfig{
			CACert: "/etc/ssl/ca.pem", // absolute → as-is
			Cert:   "client.pem",      // relative → joined with BaseDir
			Key:    "client.key",
		},
	}
	got, diags := buildDSN(&cfg.Config{BaseDir: "/conf"}, testBlock(), def)
	if diags.HasErrors() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	for _, want := range []string{
		"sslrootcert=/etc/ssl/ca.pem",
		"sslcert=/conf/client.pem",
		"sslkey=/conf/client.key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dsn %q missing %q", got, want)
		}
	}
}

func TestBuildDSNInsecureSkipVerifyConflict(t *testing.T) {
	def := &postgresDef{
		User:     "alice",
		Database: "appdb",
		SSLMode:  "verify-full",
		TLS:      &cfg.TLSConfig{InsecureSkipVerify: true},
	}
	_, diags := buildDSN(&cfg.Config{}, testBlock(), def)
	if !diags.HasErrors() {
		t.Fatal("expected error for insecure_skip_verify with verify-full")
	}

	// Same flag is fine with a non-verifying sslmode.
	def.SSLMode = "require"
	if _, diags := buildDSN(&cfg.Config{}, testBlock(), def); diags.HasErrors() {
		t.Fatalf("insecure_skip_verify with require should be allowed: %v", diags)
	}
}

func TestQuoteValue(t *testing.T) {
	cases := map[string]string{
		"":          "''",
		"simple":    "simple",
		"has space": "'has space'",
		`a'b`:       `'a\'b'`,
		`a\b`:       `'a\\b'`,
	}
	for in, want := range cases {
		if got := quoteValue(in); got != want {
			t.Errorf("quoteValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDialect(t *testing.T) {
	d := postgresDialect{dsn: "x"}
	if d.DriverName() != "pgx" {
		t.Errorf("DriverName = %q", d.DriverName())
	}
	if d.Name() != "postgres" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.BindType() != sqlx.DOLLAR {
		t.Errorf("BindType = %d, want %d (DOLLAR)", d.BindType(), sqlx.DOLLAR)
	}
	if d.DSN() != "x" {
		t.Errorf("DSN = %q", d.DSN())
	}
}

// TestProcessRegistersClient exercises the full decode → register path through
// the public config API without a database (Build does not open a connection;
// Start does). It runs in any build, with no cgo and no live Postgres.
func TestProcessRegistersClient(t *testing.T) {
	const vcl = `
client "postgres" "db" {
    dsn = "postgres://u:p@localhost:5432/app?sslmode=disable"

    max_open_conns = 7

    query "by_id" {
        cardinality = "one"
        sql         = "SELECT id FROM t WHERE id = :id"
    }
}
`
	config, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	require.NotNil(t, config.Clients["postgres"]["db"], "postgres client should be registered")

	// The named query was decoded from the common body and exposed on the client.
	clientVal := config.CtyClientMap["db"]
	require.False(t, clientVal.IsNull())
	assert.False(t, clientVal.GetAttr("by_id").IsNull(), "named query by_id should be exposed")
}

func TestClassifyError(t *testing.T) {
	d := postgresDialect{}

	code, sqlstate := d.ClassifyError(&pgconn.PgError{Code: "23505"})
	if code != "23505" || sqlstate != "23505" {
		t.Errorf("PgError: got (%q, %q), want (23505, 23505)", code, sqlstate)
	}

	code, sqlstate = d.ClassifyError(errors.New("plain error"))
	if code != "" || sqlstate != "" {
		t.Errorf("non-pg error: got (%q, %q), want empty", code, sqlstate)
	}
}
