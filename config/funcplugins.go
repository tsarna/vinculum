package config

import (
	"sort"

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
func RegisterFunctionPlugin(name string, getter FunctionPlugin) {
	recordPlugin("functions." + name)
	functionPlugins = append(functionPlugins, functionPluginEntry{name, getter})
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
