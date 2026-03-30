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
	return c.Features[name]
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
