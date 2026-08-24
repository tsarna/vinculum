package functions

import (
	richcty "github.com/tsarna/rich-cty-types"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("health", func(c *cfg.Config) map[string]function.Function {
		return GetHealthFunctions(c)
	})
}

// GetHealthFunctions returns the health:: data functions.
//
// The namespace is health:: rather than check:: — which would collide
// conceptually with the `vinculum check` command — and not a bare ready(),
// which is too generic a name to take from the global namespace. The functions
// that build an HTTP response out of the same data live in http:: with the
// other response builders.
func GetHealthFunctions(config *cfg.Config) map[string]function.Function {
	return map[string]function.Function{
		"health::ready":   healthProbeFunc(config, cfg.ProbeReady, false, false),
		"health::live":    healthProbeFunc(config, cfg.ProbeLive, false, false),
		"health::status":  healthProbeFunc(config, cfg.ProbeReady, true, false),
		"health::refresh": healthProbeFunc(config, cfg.ProbeReady, false, true),
	}
}

// healthProbeFunc builds one of the health:: accessors.
//
// all selects the full status list rather than the unready-only view; force
// bypasses the cache TTL.
//
// ctx leads and is required. Vinculum's two ctx conventions divide on whether a
// function does anything outside the process, and these belong with send() and
// http::get(): a refresh evaluates `check` inputs, which run arbitrary
// expressions and arbitrary I/O. Making it optional would create a path that
// silently loses trace parenting and the caller's deadline with nothing at the
// call site to suggest anything was lost — and it would let a health call in a
// load-time expression quietly answer "everything is ready" from a registry
// nothing has populated yet.
func healthProbeFunc(config *cfg.Config, probe string, all, force bool) function.Function {
	return function.New(&function.Spec{
		Description: healthDescription(probe, all, force),
		Params: []function.Parameter{
			{
				Name:             "ctx",
				Type:             cty.DynamicPseudoType,
				AllowDynamicType: true,
				Description:      "The handler context (carries tracing, deadlines, and request scope)",
			},
		},
		Type: function.StaticReturnType(cfg.HealthStatusListType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, err := richcty.GetContextFromValue(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			if all {
				return cfg.StatusesToCty(config.Health.Status(ctx, probe, force)), nil
			}
			return cfg.StatusesToCty(config.Health.Unready(ctx, probe, force)), nil
		},
	})
}

func healthDescription(probe string, all, force bool) string {
	switch {
	case force:
		return "Evaluates every readiness contributor now, ignoring the cache, and returns those that are not ready"
	case all:
		return "Returns every readiness contributor, ready ones included"
	case probe == cfg.ProbeLive:
		return "Returns the liveness contributors that are not live; empty means the process is live"
	default:
		return "Returns the readiness contributors that are not ready; empty means the process is ready"
	}
}
