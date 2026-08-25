package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tsarna/vinculum/types"
)

// The endpoints a health listener serves. /healthz is an alias for /livez: it
// is the legacy Kubernetes name for the same question, and giving it a third
// meaning would be a trap.
const (
	ReadyzPath  = "/readyz"
	LivezPath   = "/livez"
	HealthzPath = "/healthz"
)

// HealthEndpointPaths are the three paths, in the order they are registered.
var HealthEndpointPaths = []string{ReadyzPath, LivezPath, HealthzPath}

// What a `server "http"` block's health_endpoints attribute may say.
const (
	HealthEndpointsOff     = "off"
	HealthEndpointsOn      = "on"
	HealthEndpointsVerbose = "verbose"
)

// HealthRender is how one probe response should be rendered.
//
// Verbose names every component and quotes its failure reason. That is fine on
// an internal listener and an information leak on a public one, so it is never
// what a caller gets without the endpoint's owner having allowed it.
type HealthRender struct {
	Verbose bool
	JSON    bool
	// Head suppresses the body, for a HEAD request.
	Head bool
}

// probeEndpoint pairs a probe with the endpoint name used in its verbose
// trailer and JSON verdict key.
type probeEndpoint struct {
	probe    string
	endpoint string
	// verdictKey is the JSON attribute naming the probe's own answer:
	// "ready" for readiness, "live" for liveness. The per-component entries
	// keep the `ready` field either way, because they are the serialization of
	// health::status and the two must not drift.
	verdictKey string
}

func endpointFor(path string) probeEndpoint {
	switch path {
	case LivezPath:
		return probeEndpoint{probe: ProbeLive, endpoint: "livez", verdictKey: "live"}
	case HealthzPath:
		return probeEndpoint{probe: ProbeLive, endpoint: "healthz", verdictKey: "live"}
	default:
		return probeEndpoint{probe: ProbeReady, endpoint: "readyz", verdictKey: "ready"}
	}
}

// HealthResponse renders one probe as an HTTP response.
//
// There is exactly one implementation of /readyz, /livez, and /healthz, and
// every way of reaching them goes through here: the standalone --health-listen
// listener, a server "http" with health_endpoints set, and the http::readyz /
// http::livez functions. The semantics cannot drift between them because there
// is nothing to drift.
func (c *Config) HealthResponse(ctx context.Context, path string, render HealthRender) *types.HTTPResponseWrapper {
	ep := endpointFor(path)
	statuses := c.Health.Status(ctx, ep.probe, false)
	passing := true
	for _, s := range statuses {
		if !s.Ready {
			passing = false
			break
		}
	}

	// 503, not a 4xx: nothing is wrong with the request, the server is
	// temporarily unable to handle it — which is exactly what 503 means and
	// what every probe tool and load balancer expects.
	status := http.StatusOK
	if !passing {
		status = http.StatusServiceUnavailable
	}

	body, contentType := renderHealthBody(ep, statuses, passing, render)
	if render.Head {
		body = nil
	}

	return &types.HTTPResponseWrapper{
		Status:      status,
		ContentType: contentType,
		Body:        body,
		Headers:     http.Header{"Cache-Control": []string{"no-store"}},
	}
}

func renderHealthBody(ep probeEndpoint, statuses []ComponentStatus, passing bool, render HealthRender) ([]byte, string) {
	if render.JSON {
		return renderHealthJSON(ep, statuses, passing, render.Verbose), "application/json"
	}
	if render.Verbose {
		return renderHealthVerbose(ep, statuses, passing), "text/plain; charset=utf-8"
	}
	if passing {
		return []byte("ok\n"), "text/plain; charset=utf-8"
	}
	return []byte("not " + ep.verdictKey + "\n"), "text/plain; charset=utf-8"
}

// renderHealthVerbose mirrors kube-apiserver's /readyz?verbose, so operator
// habits transfer.
func renderHealthVerbose(ep probeEndpoint, statuses []ComponentStatus, passing bool) []byte {
	var b strings.Builder
	for _, s := range statuses {
		if s.Ready {
			fmt.Fprintf(&b, "[+]%s ok\n", s.Component)
			continue
		}
		fmt.Fprintf(&b, "[-]%s failed: %s\n", s.Component, s.Reason)
	}
	if passing {
		fmt.Fprintf(&b, "%s check passed\n", ep.endpoint)
	} else {
		fmt.Fprintf(&b, "%s check failed\n", ep.endpoint)
	}
	return []byte(b.String())
}

// renderHealthJSON emits the verdict, and the component list only where the
// endpoint allows detail. A terse endpoint must stay terse however the caller
// asks: negotiating your way past the setting would defeat it.
func renderHealthJSON(ep probeEndpoint, statuses []ComponentStatus, passing, verbose bool) []byte {
	type entry struct {
		Component string `json:"component"`
		Type      string `json:"type"`
		Ready     bool   `json:"ready"`
		Reason    string `json:"reason"`
	}
	doc := map[string]any{ep.verdictKey: passing}
	if verbose {
		// The same values health::status returns, so the two views cannot
		// disagree about what is wrong.
		checks := make([]entry, len(statuses))
		for i, s := range statuses {
			checks[i] = entry{Component: s.Component, Type: s.Type, Ready: s.Ready, Reason: s.Reason}
		}
		doc["checks"] = checks
	}
	out, err := json.Marshal(doc)
	if err != nil {
		// The document is built from strings and bools; this cannot fail.
		return []byte(`{"` + ep.verdictKey + `":false}`)
	}
	return append(out, '\n')
}

// NegotiateHealthRender reads the rendering a request asked for.
//
// allowVerbose is the endpoint's own setting, not the caller's: `?verbose` is
// honored only where the endpoint's owner said it may be. Where it may not,
// JSON carries the verdict alone.
func NegotiateHealthRender(r *http.Request, allowVerbose bool) HealthRender {
	render := HealthRender{Head: r.Method == http.MethodHead}
	q := r.URL.Query()

	if q.Get("format") == "json" || acceptsJSON(r.Header.Get("Accept")) {
		render.JSON = true
	}
	if allowVerbose && queryFlag(q, "verbose") {
		render.Verbose = true
	}
	return render
}

// queryFlag reads a valueless query flag: `?verbose` and `?verbose=true` are
// both on, `?verbose=false` and `?verbose=0` are off.
func queryFlag(q map[string][]string, name string) bool {
	vals, ok := q[name]
	if !ok {
		return false
	}
	for _, v := range vals {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func acceptsJSON(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(media), "application/json") {
			return true
		}
	}
	return false
}

// HealthHandler serves one health endpoint.
//
// It is not wrapped in any auth: a kubelet cannot authenticate. That is why the
// body reveals nothing unless allowVerbose says it may.
func (c *Config) HealthHandler(path string, allowVerbose bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := c.HealthResponse(r.Context(), path, NegotiateHealthRender(r, allowVerbose))
		WriteHealthResponse(w, resp)
	})
}

// WriteHealthResponse writes a rendered probe response.
func WriteHealthResponse(w http.ResponseWriter, resp *types.HTTPResponseWrapper) {
	for name, vals := range resp.Headers {
		for _, v := range vals {
			w.Header().Add(name, v)
		}
	}
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.WriteHeader(resp.Status)
	if len(resp.Body) > 0 {
		_, _ = w.Write(resp.Body)
	}
}

// HealthMux builds a ServeMux serving all three endpoints, for a listener that
// owns nothing else.
func (c *Config) HealthMux(allowVerbose bool) *http.ServeMux {
	mux := http.NewServeMux()
	for _, path := range HealthEndpointPaths {
		mux.Handle(path, c.HealthHandler(path, allowVerbose))
	}
	return mux
}
