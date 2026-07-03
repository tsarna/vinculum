package config

import (
	"bytes"
	"errors"

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

// functyFileMap returns the map of .cty source files (filename → bytes) needed to
// render a functy error with source context. It is built once during Build()
// (see functyState.compile) and is immutable thereafter, so this is a cheap read
// per error rather than a rebuild. .vcl files are not retained, but a functy
// throw's range always points into a .cty file. Returns nil when there are no
// .cty sources — hcl's diagnostic writer tolerates a nil files map.
func (c *Config) functyFileMap() map[string]*hcl.File {
	if c.functyState == nil {
		return nil
	}
	return c.functyState.files
}

// ActionError renders an action/expression evaluation failure as a zap field
// (keyed "error", like zap.Error) for logging via UserLogger. When the failure
// carries a functy throw, it is rendered against the .cty source — the failing
// line plus any assert operand detail (e.g. `n = -3`) — via the standard hcl
// diagnostic writer; otherwise it falls back to the diagnostics' own text.
//
// Use it in place of zap.Error(diags) at action-evaluation error sites:
//
//	config.UserLogger.Error("... action error", zap.String("name", n), config.ActionError(diags))
func (c *Config) ActionError(diags hcl.Diagnostics) zap.Field {
	return actionErrorField(diags, c.functyFileMap())
}

// actionErrorField is the shared implementation of ActionError, parameterized
// over the .cty file map so callers without a *Config (e.g. SignalActionHandler)
// can render functy throws too.
func actionErrorField(diags hcl.Diagnostics, files map[string]*hcl.File) zap.Field {
	if te, ok := functyThrownError(diags); ok {
		var buf bytes.Buffer
		wr := hcl.NewDiagnosticTextWriter(&buf, files, 0, false)
		if err := wr.WriteDiagnostics(te.Diagnostics()); err == nil {
			return zap.String("error", buf.String())
		}
	}
	return zap.Error(diags)
}
