package config

import (
	"sort"

	"github.com/zclconf/go-cty/cty"
)

// --- Global plugin name registry ---

var registeredPlugins []string

// recordPlugin records a plugin name in the global registry.
// Called from each Register* function in this package.
func recordPlugin(name string) {
	registeredPlugins = append(registeredPlugins, name)
}

// RegisteredPlugins returns a sorted copy of all registered plugin names,
// e.g. "ambient.sys", "client.kafka", "functions.stdlib", "server.mcp", "trigger.start".
func RegisteredPlugins() []string {
	sorted := make([]string, len(registeredPlugins))
	copy(sorted, registeredPlugins)
	sort.Strings(sorted)
	return sorted
}

// --- Ambient provider registry ---

// AmbientProvider is a function that contributes a named value to the global
// HCL evaluation context. Providers register themselves via RegisterAmbientProvider
// in their init() functions; config.Build() loops over all registered providers
// to populate config.Constants before evaluating any blocks.
type AmbientProvider func(cfg *Config) cty.Value

type ambientEntry struct {
	name string
	p    AmbientProvider
}

var ambientProviders []ambientEntry

// RegisterAmbientProvider registers a named provider that contributes a top-level
// value to the HCL evaluation context. Sub-packages call this from init().
func RegisterAmbientProvider(name string, p AmbientProvider) {
	recordPlugin("ambient." + name)
	ambientProviders = append(ambientProviders, ambientEntry{name, p})
}
