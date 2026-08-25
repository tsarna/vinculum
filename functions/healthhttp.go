package functions

import (
	_ "embed"
	"fmt"

	richcty "github.com/tsarna/rich-cty-types"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// healthHTTPExterns declares the real signatures of http::readyz and
// http::livez, whose optional trailing argument cty can only approximate with a
// variadic.
//
//go:embed externs/http.cty
var healthHTTPExterns []byte

func init() {
	cfg.RegisterFunctionPlugin("healthhttp", func(c *cfg.Config) map[string]function.Function {
		return GetHealthHTTPFunctions(c)
	})
	cfg.RegisterFunctyExterns("vinculum/http.cty", healthHTTPExterns)
}

// GetHealthHTTPFunctions returns the probe-response builders.
//
// They live in http:: with the other response builders — what they return is an
// http_response object — while health:: holds the data functions.
func GetHealthHTTPFunctions(config *cfg.Config) map[string]function.Function {
	return map[string]function.Function{
		"http::readyz": probeResponseFunc(config, cfg.ReadyzPath, "readiness"),
		"http::livez":  probeResponseFunc(config, cfg.LivezPath, "liveness"),
	}
}

// probeResponseFunc builds http::readyz or http::livez.
//
// ctx is the Go context, required, and used for the probe alone: trace parent,
// deadline, baggage. It is deliberately *not* where the request comes from. A
// function that reached into its ctx for ctx.request would be the only one in
// the language that digs a request out of a context — http::basic_auth,
// http::set_cookie, and http::response all take explicit values — and it would
// silently degrade wherever that ctx did not come from an HTTP handler, however
// reliably that happens to hold.
func probeResponseFunc(config *cfg.Config, path, probeName string) function.Function {
	return function.New(&function.Spec{
		Description: fmt.Sprintf(
			"Builds a %s probe response: 200 when passing, 503 when not, with Cache-Control: no-store",
			probeName),
		Params: []function.Parameter{ctxParam},
		VarParam: &function.Parameter{
			Name:             "request",
			Type:             cty.DynamicPseudoType,
			AllowNull:        true,
			AllowDynamicType: true,
			Description: "Optional: ctx.request, to honor ?verbose, ?format=json and Accept; " +
				"or an options object such as {verbose = true} or {format = \"json\"}",
		},
		Type: function.StaticReturnType(types.HTTPResponseObjectType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, err := richcty.GetContextFromValue(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			render, err := probeRender(args[1:])
			if err != nil {
				return cty.NilVal, err
			}
			return types.BuildHTTPResponseObject(config.HealthResponse(ctx, path, render)), nil
		},
	})
}

// probeRender reads the optional second argument, which may be a request or an
// options object — the same value polymorphism get() and the extract* helpers
// already use.
//
// Omitted, the body is terse and nothing is negotiated. A hand-written handle
// that passes ctx.request always honors ?verbose: that is the author's choice
// to make, unlike the built-in endpoints, where terse is a property of an
// unauthenticated endpoint nobody wrote by hand.
func probeRender(rest []cty.Value) (cfg.HealthRender, error) {
	if len(rest) == 0 || rest[0].IsNull() {
		return cfg.HealthRender{}, nil
	}
	if len(rest) > 1 {
		return cfg.HealthRender{}, fmt.Errorf("expected at most one request or options argument, got %d", len(rest))
	}

	arg := rest[0]

	// A request: negotiate from it, and take the method so a HEAD gets the
	// status with no body without relying on the server to strip it.
	if req, err := types.GetHTTPRequestFromValue(arg); err == nil {
		return cfg.NegotiateHealthRender(req, true), nil
	}

	if !arg.Type().IsObjectType() {
		return cfg.HealthRender{}, fmt.Errorf(
			"expected ctx.request or an options object, got %s", arg.Type().FriendlyName())
	}

	var render cfg.HealthRender
	for name, val := range arg.AsValueMap() {
		if val.IsNull() {
			continue
		}
		switch name {
		case "verbose":
			if val.Type() != cty.Bool {
				return render, fmt.Errorf("verbose must be a bool, got %s", val.Type().FriendlyName())
			}
			render.Verbose = val.True()
		case "format":
			if val.Type() != cty.String {
				return render, fmt.Errorf("format must be a string, got %s", val.Type().FriendlyName())
			}
			if format := val.AsString(); format == "json" {
				render.JSON = true
			} else if format != "" && format != "text" {
				return render, fmt.Errorf(`format must be "text" or "json", got %q`, format)
			}
		default:
			return render, fmt.Errorf("unknown option %q; expected verbose or format", name)
		}
	}
	return render, nil
}
