package config

import (
	"github.com/hashicorp/hcl/v2"
	"go.uber.org/zap"
)

// PluginContext is the single argument passed to a Go plugin's
// VinculumPluginInit entry point. New fields may be appended in future
// releases to grow the surface without breaking existing plugins;
// existing fields will not change type.
type PluginContext struct {
	// Block is the `plugin "<label>" { ... }` block as parsed from the
	// .vinit file. Block.Labels[0] is the plugin label. Block.Body is the
	// full block body; the plugin decodes its own schema from it. The
	// `disabled` attribute has already been consumed by Vinculum but is
	// still syntactically present in the body.
	Block *hcl.Block

	// EvalContext is the minimal .vinit eval context: env.* and the cty
	// standard library. No const, no user functions, no contributions from
	// other plugins.
	EvalContext *hcl.EvalContext

	// Logger is a zap logger pre-bound with `plugin=<label>` as a
	// structured field. Plugins should use this rather than constructing
	// their own logger so plugin output is consistent with Vinculum's.
	// May be nil if Vinculum was started without a logger.
	Logger *zap.Logger
}

// VinculumPluginInitFunc is the required signature of the entry point that
// every Go plugin must export under the name VinculumPluginInit. Returning
// a non-empty error diagnostics value aborts startup.
type VinculumPluginInitFunc = func(*PluginContext) hcl.Diagnostics
