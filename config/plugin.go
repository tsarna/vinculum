//go:build linux || darwin || freebsd

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"plugin"

	"github.com/hashicorp/hcl/v2"
)

// loadPlugin resolves the .so file for the given label under pluginPath,
// opens it with Go's plugin package, looks up the VinculumPluginInit
// symbol, and invokes it with the supplied PluginContext.
//
// All failure modes are fatal: missing file, ABI mismatch, missing
// symbol, wrong symbol type, returned error diagnostics, or panic during
// init. Any returned diagnostics are surfaced as-is.
func loadPlugin(pluginPath, label string, ctx *PluginContext) hcl.Diagnostics {
	defRange := ctx.Block.DefRange

	soPath := filepath.Join(pluginPath, label+".so")

	if _, err := os.Stat(soPath); err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Plugin file not found",
			Detail: fmt.Sprintf(
				"Plugin %q resolved to %q, which could not be opened: %s.",
				label, soPath, err),
			Subject: &defRange,
		}}
	}

	p, err := plugin.Open(soPath)
	if err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Failed to load plugin",
			Detail: fmt.Sprintf(
				"plugin.Open(%q) failed: %s. This usually indicates an ABI mismatch — the plugin must be built with the same Go toolchain and dependency versions as the host binary.",
				soPath, err),
			Subject: &defRange,
		}}
	}

	sym, err := p.Lookup("VinculumPluginInit")
	if err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Plugin entry point not found",
			Detail: fmt.Sprintf(
				"Plugin %q does not export the required symbol VinculumPluginInit: %s.",
				label, err),
			Subject: &defRange,
		}}
	}

	initFn, ok := sym.(VinculumPluginInitFunc)
	if !ok {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Plugin entry point has the wrong signature",
			Detail: fmt.Sprintf(
				"Plugin %q exports VinculumPluginInit but its type is %T, not func(*config.PluginContext) hcl.Diagnostics.",
				label, sym),
			Subject: &defRange,
		}}
	}

	return invokePluginInit(label, defRange, initFn, ctx)
}

// invokePluginInit calls the plugin's init function under a deferred
// recover so a panic inside the plugin becomes a fatal diagnostic rather
// than terminating the host. Only same-goroutine panics are caught.
func invokePluginInit(
	label string,
	defRange hcl.Range,
	fn VinculumPluginInitFunc,
	ctx *PluginContext,
) (diags hcl.Diagnostics) {
	defer func() {
		if r := recover(); r != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Plugin panicked during initialization",
				Detail: fmt.Sprintf(
					"Plugin %q panicked: %v",
					label, r),
				Subject: &defRange,
			})
		}
	}()
	return fn(ctx)
}
