package config

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tsarna/vinculum-bus/o11y"
	"github.com/tsarna/vinculum/internal/promadapter"
)

// MetricsServer implements a Prometheus/OpenMetrics exposition server.
// It can operate in standalone mode (owns its own HTTP listener) or mounted
// mode (implements HandlerServer so it can be attached to a server "http" block).
type MetricsServer struct {
	BaseServer
	registry  *prometheus.Registry
	provider  *promadapter.Provider
	handler   http.Handler
	listen    string // empty = mounted mode only
	path      string // default "/metrics"
	IsDefault bool
}

// GetHandler returns the HTTP handler for the metrics endpoint.
// Implements HandlerServer.
func (s *MetricsServer) GetHandler() http.Handler {
	return s.handler
}

// GetMetricsProvider returns the o11y.MetricsProvider backed by this server's registry.
func (s *MetricsServer) GetMetricsProvider() o11y.MetricsProvider {
	return s.provider
}

// Start starts a standalone HTTP listener if listen is set; otherwise a no-op.
func (s *MetricsServer) Start() error {
	if s.listen == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(s.path, s.handler)
	srv := &http.Server{
		Addr:    s.listen,
		Handler: mux,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Log but don't crash — server lifecycle is managed externally
			_ = err
		}
	}()

	return nil
}

// GetRegistry returns the underlying prometheus registry, used by metric blocks
// to register their collectors.
func (s *MetricsServer) GetRegistry() *prometheus.Registry {
	return s.registry
}

// newMetricsServer constructs a MetricsServer with a private registry.
// When includeGoMetrics is true (the default), the Go runtime and process
// collectors are registered automatically.
func newMetricsServer(name string, defRange hcl.Range, listen, path string, isDefault, includeGoMetrics bool) *MetricsServer {
	reg := prometheus.NewRegistry()
	if includeGoMetrics {
		reg.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)
	}
	provider := promadapter.New(reg)
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	return &MetricsServer{
		BaseServer: BaseServer{Name: name, DefRange: defRange},
		registry:   reg,
		provider:   provider,
		handler:    handler,
		listen:     listen,
		path:       path,
		IsDefault:  isDefault,
	}
}

// MetricsServerDefinition holds the decoded HCL attributes for a server "metrics" block.
type MetricsServerDefinition struct {
	Listen           *string   `hcl:"listen,optional"`
	Path             *string   `hcl:"path,optional"`
	Default          *bool     `hcl:"default,optional"`
	IncludeGoMetrics *bool     `hcl:"include_go_metrics,optional"`
	DefRange         hcl.Range `hcl:",def_range"`
}

func init() {
	RegisterServerType("metrics", ProcessMetricsServerBlock)
}

// ProcessMetricsServerBlock decodes and creates a MetricsServer from a block body.
func ProcessMetricsServerBlock(config *Config, block *hcl.Block, remainingBody hcl.Body) (Listener, hcl.Diagnostics) {
	def := MetricsServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.evalCtx, &def)
	if diags.HasErrors() {
		return nil, diags
	}

	name := block.Labels[1]

	listen := ""
	if def.Listen != nil {
		listen = *def.Listen
	}

	path := "/metrics"
	if def.Path != nil {
		path = *def.Path
	}

	isDefault := false
	if def.Default != nil {
		isDefault = *def.Default
	}

	includeGoMetrics := true
	if def.IncludeGoMetrics != nil {
		includeGoMetrics = *def.IncludeGoMetrics
	}

	srv := newMetricsServer(name, def.DefRange, listen, path, isDefault, includeGoMetrics)

	if config.MetricsServers == nil {
		config.MetricsServers = make(map[string]*MetricsServer)
	}
	config.MetricsServers[name] = srv

	if listen != "" {
		config.Startables = append(config.Startables, srv)
	}

	return srv, nil
}

// GetMetricsServerFromExpression evaluates an HCL expression expecting a server capsule
// and returns the underlying *MetricsServer. Returns an error if the server is not a
// metrics server.
func GetMetricsServerFromExpression(config *Config, expr hcl.Expression) (*MetricsServer, hcl.Diagnostics) {
	server, diags := GetServerFromExpression(config, expr)
	if diags.HasErrors() {
		return nil, diags
	}

	ms, ok := server.(*MetricsServer)
	if !ok {
		exprRange := expr.Range()
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Server is not a metrics server",
				Detail:   fmt.Sprintf("Expected a server \"metrics\" block, got server %q of a different type", server.GetName()),
				Subject:  &exprRange,
			},
		}
	}

	return ms, nil
}

// ResolveMetricsProvider resolves an o11y.MetricsProvider from an optional HCL
// expression. If the expression is provided it must reference a server "metrics"
// block. If omitted, the default metrics provider is used. Returns nil, nil when
// no metrics server is configured at all (metrics are simply disabled).
func ResolveMetricsProvider(config *Config, expr hcl.Expression) (o11y.MetricsProvider, hcl.Diagnostics) {
	if IsExpressionProvided(expr) {
		ms, diags := GetMetricsServerFromExpression(config, expr)
		if diags.HasErrors() {
			return nil, diags
		}
		return ms.GetMetricsProvider(), nil
	}
	return config.GetDefaultMetricsProvider()
}

// GetDefaultMetricsProvider resolves the default metrics provider per these rules:
//  1. Exactly one metrics server → use it
//  2. Multiple, exactly one with IsDefault=true → use it
//  3. Multiple with IsDefault=true → config error
//  4. Multiple, none default → return nil (explicit wiring required)
//  5. Zero → return nil
func (c *Config) GetDefaultMetricsProvider() (o11y.MetricsProvider, hcl.Diagnostics) {
	if len(c.MetricsServers) == 0 {
		return nil, nil
	}

	if len(c.MetricsServers) == 1 {
		for _, ms := range c.MetricsServers {
			return ms.GetMetricsProvider(), nil
		}
	}

	// Multiple servers: find the one(s) marked default
	var defaults []*MetricsServer
	for _, ms := range c.MetricsServers {
		if ms.IsDefault {
			defaults = append(defaults, ms)
		}
	}

	if len(defaults) == 1 {
		return defaults[0].GetMetricsProvider(), nil
	}

	if len(defaults) > 1 {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Multiple default metrics servers",
				Detail:   "More than one server \"metrics\" block has default = true. At most one can be the default.",
			},
		}
	}

	// Multiple servers, none marked default
	return nil, nil
}

