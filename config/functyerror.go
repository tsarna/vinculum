package config

import (
	"bytes"
	"errors"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/tsarna/functy"
	"go.uber.org/zap"
)

// functyThrownError recovers a functy *ThrownError carried in a set of
// diagnostics produced by evaluating a VCL expression that called a functy
// function which raised (an uncaught throw or a failed assert). At the
// cty.Function boundary HCL stashes the underlying call error on a diagnostic's
// Extra (exposed via hclsyntax.FunctionCallDiagExtra); when that error is a
// *functy.ThrownError, it is returned so the throw's structure and .cty source
// range survive. This mirrors functy's own boundary recovery.
func functyThrownError(diags hcl.Diagnostics) (*functy.ThrownError, bool) {
	for _, d := range diags {
		if fce, ok := hcl.DiagnosticExtra[hclsyntax.FunctionCallDiagExtra](d); ok {
			var te *functy.ThrownError
			if errors.As(fce.FunctionCallError(), &te) {
				return te, true
			}
		}
	}
	return nil, false
}

// SourceFiles exposes every source file the configuration was built from
// (filename → bytes: .vinit, .vcl, and .cty) so a caller such as
// `vinculum test` can render a failure's diagnostics with source context.
// Returns nil for a Config not produced by Build — hcl's diagnostic writer
// tolerates a nil map.
func (c *Config) SourceFiles() map[string]*hcl.File {
	return c.files
}

// ActionError renders an action/expression evaluation failure as a zap field
// (keyed "error", like zap.Error) for logging via UserLogger: the failing line
// quoted from its source, plus any assert operand detail (e.g. `n = -3`) when a
// functy throw carries one.
//
// A throw is rendered against its own range rather than the call that surfaced
// it, which is why it is recovered first: the useful line is the `assert` in the
// .cty file, not the expression that called into it. Everything else is rendered
// where it failed, which for a VCL expression is the `action =` line.
//
// Use it in place of zap.Error(diags) at action-evaluation error sites:
//
//	config.UserLogger.Error("... action error", zap.String("name", n), config.ActionError(diags))
func (c *Config) ActionError(diags hcl.Diagnostics) zap.Field {
	return actionErrorField(diags, c.files)
}

// ActionErrorShowsSource reports whether ActionError would render these
// diagnostics against their source rather than fall back to their plain text.
//
// It is the question a site that *also* returns the failure to a generic
// handler has to answer: log here only when doing so shows something that
// handler cannot, and leave the rest to the handler rather than say the same
// thing twice.
func (c *Config) ActionErrorShowsSource(diags hcl.Diagnostics) bool {
	_, ok := renderActionError(diags, c.files)
	return ok
}

// reportedError marks a failure this process has already logged in a form its
// caller cannot produce, implementing bus.ReportedError so the event bus skips
// its own log line. It delegates everything else: the text is the failure's
// own, so a dead-letter header carries what it always did, and Unwrap keeps the
// diagnostics reachable for anything that looks.
type reportedError struct{ error }

func (reportedError) AlreadyReported() {}

func (e reportedError) Unwrap() error { return e.error }

// Reported marks an error as already logged, for a subscriber outside this
// package that renders its own failures — a client receiver's action, say.
// Pair it with ActionErrorShowsSource, which says whether there was anything
// worth rendering.
func Reported(err error) error {
	if err == nil {
		return nil
	}
	return reportedError{err}
}

// actionErrorField is the shared implementation of ActionError, parameterized
// over the source file map so callers without a *Config (e.g.
// SignalActionHandler) render the same way.
func actionErrorField(diags hcl.Diagnostics, files map[string]*hcl.File) zap.Field {
	if rendered, ok := renderActionError(diags, files); ok {
		return zap.String("error", rendered)
	}
	return zap.Error(diags)
}

// renderActionError renders an evaluation failure against its source, reporting
// whether it could.
//
// A functy throw is unwrapped first so it is rendered against its own range —
// the `assert` in the .cty file — rather than the call that surfaced it.
func renderActionError(diags hcl.Diagnostics, files map[string]*hcl.File) (string, bool) {
	if te, ok := functyThrownError(diags); ok {
		diags = te.Diagnostics()
	}
	return renderDiags(diags, files)
}

// renderDiags renders diagnostics the way the CLI does, declining unless at
// least one of them points into a file that was actually parsed.
//
// The rendered form earns its several lines by quoting the offending one. With
// no source to quote it prints "(source code not available)" instead, which is
// more lines than the one-line fallback and less in them — and that is the case
// for a range into something synthesized at runtime rather than read from disk.
func renderDiags(diags hcl.Diagnostics, files map[string]*hcl.File) (string, bool) {
	quotable := false
	for _, d := range diags {
		if d.Subject != nil && files[d.Subject.Filename] != nil {
			quotable = true
			break
		}
	}
	if !quotable {
		return "", false
	}

	var buf bytes.Buffer
	wr := hcl.NewDiagnosticTextWriter(&buf, files, 0, false)
	if err := wr.WriteDiagnostics(diags); err != nil {
		return "", false
	}
	// The writer separates consecutive diagnostics with a trailing blank line,
	// which inside a log field is a blank line between records.
	return strings.TrimRight(buf.String(), "\n"), true
}
