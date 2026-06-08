//go:build !cgo

package sqlite

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On a non-cgo build, using `client "sqlite"` must surface the clear
// "not compiled in" error rather than "unknown client type".
func TestSQLiteUnsupportedDiagnostic(t *testing.T) {
	block := &hcl.Block{
		Type:   "client",
		Labels: []string{"sqlite", "appdb"},
		DefRange: hcl.Range{
			Filename: "appdb.vcl",
			Start:    hcl.Pos{Line: 3, Column: 3},
			End:      hcl.Pos{Line: 3, Column: 27},
		},
	}

	client, diags := processUnsupported(nil, block, nil)
	require.Nil(t, client)
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags[0].Summary, "SQLite not compiled into this build")
	assert.Contains(t, diags[0].Detail, "CGO_ENABLED=1")
}
