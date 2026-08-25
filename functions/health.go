package functions

import (
	_ "embed"
	"fmt"

	richcty "github.com/tsarna/rich-cty-types"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// healthExterns declares the real signatures of health::status and
// health::failing. Their trailing `probe` is optional, which cty can only
// approximate with a variadic — reflecting as `*probe`, reading as "any number
// of probes" when at most one is accepted.
//
//go:embed externs/health.cty
var healthExterns []byte

func init() {
	cfg.RegisterFunctionPlugin("health", func(c *cfg.Config) map[string]function.Function {
		return GetHealthFunctions(c)
	})
	cfg.RegisterFunctyExterns("vinculum/health.cty", healthExterns)
}

// GetHealthFunctions returns the health:: data functions.
//
// The namespace is health:: rather than check:: — which would collide
// conceptually with the `vinculum check` command — and not a bare ready(),
// which is too generic a name to take from the global namespace. The functions
// that build an HTTP response out of the same data live in http:: with the
// other response builders.
//
// Each name answers its own question: health::ready and health::live are the
// booleans they sound like, and the contributor detail is health::status (all
// of them) or health::failing (only the ones with something wrong).
func GetHealthFunctions(config *cfg.Config) map[string]function.Function {
	return map[string]function.Function{
		"health::ready":   healthBoolFunc(config, cfg.ProbeReady),
		"health::live":    healthBoolFunc(config, cfg.ProbeLive),
		"health::status":  healthListFunc(config, false),
		"health::failing": healthListFunc(config, true),
		"health::refresh": healthRefreshFunc(config),
	}
}

// ctxParam is the leading context every health:: function requires.
//
// Vinculum's two ctx conventions divide on whether a function does anything
// outside the process, and these belong with send() and http::get(): a refresh
// evaluates `check` inputs, which run arbitrary expressions and arbitrary I/O.
// Making it optional would create a path that silently loses trace parenting
// and the caller's deadline with nothing at the call site to suggest anything
// was lost — and it would let a health call in a load-time expression quietly
// answer "everything is ready" from a registry nothing has populated yet.
var ctxParam = function.Parameter{
	Name:             "ctx",
	Type:             cty.DynamicPseudoType,
	AllowDynamicType: true,
	Description:      "The handler context (carries tracing, deadlines, and request scope)",
}

// probeVarParam is the optional trailing probe selector on the detail
// functions. cty can only make a trailing parameter optional by making it
// variadic, so arity is checked in the implementation.
var probeVarParam = function.Parameter{
	Name:        "probe",
	Type:        cty.String,
	Description: `Which probe to report on: "ready" (the default) or "live"`,
}

// healthBoolFunc builds health::ready or health::live.
func healthBoolFunc(config *cfg.Config, probe string) function.Function {
	description := "Reports whether the process is ready to serve traffic"
	if probe == cfg.ProbeLive {
		description = "Reports whether the process is live — false only when a check declared probe = \"live\" is failing"
	}
	return function.New(&function.Spec{
		Description: description,
		Params:      []function.Parameter{ctxParam},
		Type:        function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, err := richcty.GetContextFromValue(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			return cty.BoolVal(len(config.Health.Failing(ctx, probe, false)) == 0), nil
		},
	})
}

// healthListFunc builds health::status or health::failing.
func healthListFunc(config *cfg.Config, failingOnly bool) function.Function {
	description := "Returns every contributor to a health probe, passing ones included"
	if failingOnly {
		description = "Returns the contributors to a health probe that are failing, with the reason for each"
	}
	return function.New(&function.Spec{
		Description: description,
		Params:      []function.Parameter{ctxParam},
		VarParam:    &probeVarParam,
		Type:        function.StaticReturnType(cfg.HealthStatusListType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, err := richcty.GetContextFromValue(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			probe, err := probeArg(args[1:])
			if err != nil {
				return cty.NilVal, err
			}
			if failingOnly {
				return cfg.StatusesToCty(config.Health.Failing(ctx, probe, false)), nil
			}
			return cfg.StatusesToCty(config.Health.Status(ctx, probe, false)), nil
		},
	})
}

// healthRefreshFunc builds health::refresh.
func healthRefreshFunc(config *cfg.Config) function.Function {
	return function.New(&function.Spec{
		Description: "Re-evaluates every health check now, ignoring the cache, and reports whether the process is ready",
		Params:      []function.Parameter{ctxParam},
		Type:        function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, err := richcty.GetContextFromValue(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			// One evaluation covers both probes, so a refresh is not
			// probe-specific; readiness is the answer worth returning.
			return cty.BoolVal(len(config.Health.Failing(ctx, cfg.ProbeReady, true)) == 0), nil
		},
	})
}

// probeArg reads the optional trailing probe selector.
func probeArg(rest []cty.Value) (string, error) {
	switch len(rest) {
	case 0:
		return cfg.ProbeReady, nil
	case 1:
		if rest[0].IsNull() {
			return cfg.ProbeReady, nil
		}
		probe := rest[0].AsString()
		if !cfg.ValidProbe(probe) {
			return "", fmt.Errorf("probe must be %q or %q; got %q",
				cfg.ProbeReady, cfg.ProbeLive, probe)
		}
		return probe, nil
	default:
		return "", fmt.Errorf("expected at most one probe argument, got %d", len(rest))
	}
}
