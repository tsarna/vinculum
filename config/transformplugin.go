package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty/function"
)

type transformPluginEntry struct {
	name   string
	getter FunctionPlugin
}

var transformPlugins []transformPluginEntry

// RegisterTransformPlugin registers a named getter that contributes
// transform-context functions. Each function in the returned map must
// produce a value of type MessageTransformCapsuleType.
//
// Sub-packages (and Go plugins) call this from their init() function, or
// from a plugin's VinculumPluginInit. The getter is invoked when Config
// builds the transform-only eval context.
//
// Names that collide with a built-in transform or with another registered
// transform plugin produce a fatal diagnostic at Build() time, naming the
// offending plugin and the conflicting function name.
//
// # A transform must be safe to run concurrently
//
// The pipeline runs inside the queue rather than in front of it, so a
// subscriber with `partitions` set applies its transforms on as many goroutines
// as it has partitions, to different messages at once. A transform holding
// state across messages is therefore a data race, and one that nothing
// diagnoses: every built-in transform is a pure function of the message it is
// given, and a plugin's must be too.
//
// State that belongs to a stream rather than to a message belongs in a
// subscriber, where the queue has already decided what may run at once.
func RegisterTransformPlugin(name string, getter FunctionPlugin) {
	recordPlugin("transforms." + name)
	transformPlugins = append(transformPlugins, transformPluginEntry{name, getter})
}

// buildTransformPluginFunctions collects functions from every registered
// transform plugin, checks for collisions against built-ins (derived from
// builtinTransforms — single source of truth) and against each other, and
// returns the merged map.
func (c *Config) buildTransformPluginFunctions() (map[string]function.Function, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	merged := make(map[string]function.Function)
	owner := make(map[string]string)
	builtins := c.builtinTransforms()

	for _, p := range transformPlugins {
		for fname, fn := range p.getter(c) {
			if _, isBuiltin := builtins[fname]; isBuiltin {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Transform plugin function name collides with built-in",
					Detail: fmt.Sprintf(
						"Transform plugin %q contributes a function named %q, which collides with a built-in transform of the same name.",
						p.name, fname),
				})
				continue
			}
			if prev, exists := owner[fname]; exists {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Transform plugin function name collides between plugins",
					Detail: fmt.Sprintf(
						"Transform plugins %q and %q both contribute a function named %q.",
						prev, p.name, fname),
				})
				continue
			}
			merged[fname] = fn
			owner[fname] = p.name
		}
	}

	return merged, diags
}
