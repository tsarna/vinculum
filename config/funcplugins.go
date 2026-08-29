package config

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty/function"
)

// FunctionPlugin returns a map of functions for the given config.
// Plugins that don't need config can ignore the cfg parameter.
// Conditional plugins (e.g. filesystem, filewrite) return nil when their
// required feature is not enabled.
type FunctionPlugin func(cfg *Config) map[string]function.Function

type functionPluginEntry struct {
	name   string
	getter FunctionPlugin
}

var functionPlugins []functionPluginEntry

// RegisterFunctionPlugin registers a named function plugin.
// Sub-packages call this from their init() function.
//
// Names that collide with another registered plugin's produce a fatal
// diagnostic at Build() time, naming both plugins and the conflicting function
// name. Registration order is init() order, which is neither declared nor
// stable, so silently letting one win would make which implementation runs an
// accident of the link.
func RegisterFunctionPlugin(name string, getter FunctionPlugin) {
	recordPlugin("functions." + name)
	functionPlugins = append(functionPlugins, functionPluginEntry{name, getter})
}

// buildPluginFunctions collects functions from every registered function
// plugin, checks for collisions between plugins, and returns the merged map.
//
// The counterpart to buildTransformPluginFunctions, and deliberately shaped
// like it, with one difference it cannot share: transforms have a built-in set
// to check against, whereas every function reaches this map through a plugin,
// in-tree ones included. So there is one rule here rather than two.
//
// Ownership is decided over the same all-features probe possibleFunctionNames
// uses, because a collision is a property of the binary's plugin set rather
// than of the flags it was launched with. `basename` was contributed by both
// stdlib and filesystem for as long as both existed, but only a run with
// --file-path could have noticed — and the run that would have noticed is not
// the run that needs telling. The returned map still holds only what this
// invocation actually has.
func (c *Config) buildPluginFunctions() (map[string]function.Function, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	probe := *c
	probe.probeAllFeatures = true

	owner := make(map[string]string)
	for _, p := range functionPlugins {
		for fname := range p.getter(&probe) {
			if prev, exists := owner[fname]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Function name collides between plugins",
					Detail: fmt.Sprintf(
						"Function plugins %q and %q both contribute a function named %q. "+
							"Which one a config would call would depend on init() order, so neither is used.",
						prev, p.name, fname),
				})
				continue
			}
			owner[fname] = p.name
		}
	}

	// Second pass for the values, since only this invocation's features decide
	// which of them exist. Skipping the names a collision disowned keeps the
	// map deterministic; the diagnostic is an error, so a caller reaching it at
	// all has already failed the build.
	merged := make(map[string]function.Function)
	for _, p := range functionPlugins {
		for fname, fn := range p.getter(c) {
			if owner[fname] == p.name {
				merged[fname] = fn
			}
		}
	}

	return merged, diags
}

// GetFeature returns the value associated with a named feature flag,
// or empty string if the feature is not enabled.
func (c *Config) GetFeature(name string) string {
	if v := c.Features[name]; v != "" {
		return v
	}
	if c.probeAllFeatures {
		return featureProbeValue
	}
	return ""
}

// featureProbeValue is what a feature-name probe answers for a feature this
// invocation did not enable. Any non-empty value does: the probe keeps the
// names the plugins return and throws the functions themselves away.
const featureProbeValue = "<feature probe>"

// possibleFunctionNames returns the name of every function this binary could
// provide: the ones this invocation has, plus the ones it would have been given
// different feature flags.
//
// The deferred-reference check needs the wider set. `file()` exists only with
// --file-path and `kill()` only with --allow-kill, so whether they are in
// c.Functions is a property of how the process was launched rather than of the
// configuration in front of it — and `vinculum check` has no --allow-kill to be
// given. Checking against this invocation's own map would report a config that
// runs perfectly well.
//
// The probe is a shallow copy of the config that answers every feature, so a
// feature added later is covered without a list here to keep in step.
func (c *Config) possibleFunctionNames() map[string]bool {
	names := make(map[string]bool, len(c.Functions))
	for name := range c.Functions {
		names[name] = true
	}

	probe := *c
	probe.probeAllFeatures = true
	for _, p := range functionPlugins {
		for name := range p.getter(&probe) {
			names[name] = true
		}
	}

	return names
}

// EnabledFeatureNames returns the names of all enabled features, sorted.
func (c *Config) EnabledFeatureNames() []string {
	names := make([]string, 0, len(c.Features))
	for k := range c.Features {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
