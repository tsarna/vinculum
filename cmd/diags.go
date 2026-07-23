package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/hashicorp/hcl/v2"
)

// reportWarnings writes any warning-severity diagnostics to stderr. Build() may
// return warnings alongside a perfectly valid config (deprecations, precision
// loss, etc.); callers decide success via diags.HasErrors(), but warnings must
// still be surfaced — otherwise they are silently dropped.
func reportWarnings(diags hcl.Diagnostics) {
	for _, d := range diags {
		if d.Severity == hcl.DiagWarning {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", d.Error())
		}
	}
}

// printDiags renders diagnostics to w with source context. files maps each
// filename to its raw bytes so the writer can quote the offending line; a
// zero-value *hcl.File{Bytes: src} is sufficient for the source snippet.
func printDiags(w io.Writer, files map[string]*hcl.File, diags hcl.Diagnostics) {
	tw := hcl.NewDiagnosticTextWriter(w, files, 0, false)
	tw.WriteDiagnostics(diags) //nolint:errcheck
}
