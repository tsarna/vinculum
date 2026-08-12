package cmd

import (
	"errors"
	"io"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/config"
)

// reportBuildDiags renders everything Build() had to say — warnings as well as
// errors — with the offending source line quoted, and returns a non-nil error
// when any of them were errors.
//
// The error is marked Reported so main prints nothing further: hcl.Diagnostics
// renders as *the first diagnostic plus a count*, so returning the diagnostics
// themselves would restate one error and swallow the rest. Warnings are
// reported even on success, since Build() returns a usable Config alongside
// deprecation and precision-loss warnings that must not be silently dropped.
func reportBuildDiags(w io.Writer, cb *config.ConfigBuilder, diags hcl.Diagnostics) error {
	if len(diags) > 0 {
		printDiags(w, cb.Files(), diags)
	}
	if diags.HasErrors() {
		return &ExitCodeError{Code: 1, Err: errors.New("invalid configuration"), Reported: true}
	}
	return nil
}

// printDiags renders diagnostics to w with source context. files maps each
// filename to its raw bytes so the writer can quote the offending line; a
// zero-value *hcl.File{Bytes: src} is sufficient for the source snippet.
func printDiags(w io.Writer, files map[string]*hcl.File, diags hcl.Diagnostics) {
	tw := hcl.NewDiagnosticTextWriter(w, files, 0, false)
	tw.WriteDiagnostics(diags) //nolint:errcheck
}
