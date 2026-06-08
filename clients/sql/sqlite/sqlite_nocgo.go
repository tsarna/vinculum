//go:build !cgo

package sqlite

import (
	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
)

// On a non-cgo build the "sqlite" client type is still registered, so using it
// produces a clear "not compiled in" error rather than the generic "unknown
// client type" error.
func init() {
	cfg.RegisterClientType("sqlite", processUnsupported)
}

func processUnsupported(_ *cfg.Config, block *hcl.Block, _ hcl.Body) (cfg.Client, hcl.Diagnostics) {
	r := block.DefRange
	return nil, hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "SQLite not compiled into this build",
		Detail: "SQLite support requires a cgo-enabled build. Use the \"vinculum\" full image, " +
			"or rebuild with CGO_ENABLED=1.",
		Subject: &r,
	}}
}
