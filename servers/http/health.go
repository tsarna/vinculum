package httpserver

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	cfg "github.com/tsarna/vinculum/config"
)

// mountHealthEndpoints registers /readyz, /livez, and /healthz on this server
// when the block asked for them, and reports which paths it took.
//
// They are off by default. Turning them on by default was considered and
// rejected twice over: it would silently hijack three paths in configurations
// that already work — a `handle "/"` reverse-proxying an upstream that serves
// its own /healthz would find exactly those intercepted on upgrade, with
// nothing changed and nothing warned — and it would put an unauthenticated
// endpoint on whichever listener the author declared, which is frequently the
// public one.
//
// The handlers are mounted on the bare mux, outside NewAuthMiddleware and
// outside the logging middleware. Not an oversight: a kubelet cannot
// authenticate, which is exactly why the default body reveals nothing, and a
// probe every ten seconds across three endpoints would dominate the request
// log. An author who wants authenticated health leaves this off and writes a
// `handle` with an `auth` block.
func mountHealthEndpoints(config *cfg.Config, mux *http.ServeMux, def *HttpServerDefinition) ([]string, hcl.Diagnostics) {
	allowVerbose := false
	switch def.HealthEndpoints {
	case "", cfg.HealthEndpointsOff:
		return nil, nil
	case cfg.HealthEndpointsOn:
	case cfg.HealthEndpointsVerbose:
		allowVerbose = true
	default:
		subject := &def.DefRange
		if def.HealthEndpointsRange.Filename != "" {
			subject = &def.HealthEndpointsRange
		}
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid health_endpoints",
			Detail: fmt.Sprintf("health_endpoints must be %q, %q, or %q; got %q.",
				cfg.HealthEndpointsOff, cfg.HealthEndpointsOn, cfg.HealthEndpointsVerbose,
				def.HealthEndpoints),
			Subject: subject,
		}}
	}

	claimed := declaredPaths(def)

	var mounted []string
	for _, path := range cfg.HealthEndpointPaths {
		// An explicit route always wins. Registration happens after the
		// author's routes are collected, and compares path only.
		if claimed[path] {
			continue
		}
		handler := config.HealthHandler(path, allowVerbose)
		if diags := safeMuxHandle(mux, path, handler, def.DefRange); diags.HasErrors() {
			return nil, diags
		}
		mounted = append(mounted, path)
	}
	return mounted, nil
}

// declaredPaths is the set of paths this block's own routes claim.
//
// Compared by path alone, because `handle "GET /readyz"` is a different
// ServeMux pattern from `/readyz` — the two would not collide at registration,
// and the exact pattern would win for GET while ours answered every other
// method. That is a confusing split of one path across two owners, so the
// author's route takes the whole path.
func declaredPaths(def *HttpServerDefinition) map[string]bool {
	claimed := make(map[string]bool)
	for _, h := range def.Handlers {
		if h.Disabled {
			continue
		}
		if _, _, path, ok := splitPattern(h.Route); ok {
			claimed[path] = true
		}
	}
	for _, f := range def.StaticFiles {
		if f.Disabled {
			continue
		}
		if _, _, path, ok := splitPattern(f.UrlPath); ok {
			claimed[path] = true
		}
	}
	return claimed
}
