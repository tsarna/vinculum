package config

import "github.com/zclconf/go-cty/cty"

// AmbientProvider is a function that contributes a named value to the global
// HCL evaluation context. Providers register themselves via RegisterAmbientProvider
// in their init() functions; config.Build() loops over all registered providers
// to populate config.Constants before evaluating any blocks.
type AmbientProvider func(cfg *Config) (name string, value cty.Value)

var ambientProviders []AmbientProvider

// RegisterAmbientProvider registers a provider that contributes a top-level
// value to the HCL evaluation context. Sub-packages call this from init().
func RegisterAmbientProvider(p AmbientProvider) {
	ambientProviders = append(ambientProviders, p)
}
