//go:build !linux && !darwin && !freebsd

package config

import (
	"fmt"
	"runtime"

	"github.com/hashicorp/hcl/v2"
)

// loadPlugin on unsupported platforms always returns a fatal diagnostic.
// Go's `plugin` package only supports Linux, macOS, and FreeBSD. Plugin
// source code still compiles on other platforms because PluginContext is
// defined in plugin_common.go; only the act of loading is unsupported.
func loadPlugin(pluginPath, label string, ctx *PluginContext) hcl.Diagnostics {
	defRange := ctx.Block.DefRange
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Plugins are not supported on this platform",
		Detail: fmt.Sprintf(
			"Plugin %q cannot be loaded: Vinculum's plugin loader is only available on Linux, macOS, and FreeBSD (current platform: %s/%s).",
			label, runtime.GOOS, runtime.GOARCH),
		Subject: &defRange,
	}}
}
