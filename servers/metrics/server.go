package metricsserver

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tsarna/vinculum-bus/o11y"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/promadapter"
	metricsauth "github.com/tsarna/vinculum/servers/auth"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
)

// MetricsServer implements a Prometheus/OpenMetrics exposition server.
// It can operate in standalone mode (owns its own HTTP listener) or mounted
// mode (implements HandlerServer so it can be attached to a server "http" block).
type MetricsServer struct {
	cfg.BaseServer
	registry   *prometheus.Registry
	provider   *promadapter.Provider
	handler    http.Handler
	listen     string         // empty = mounted mode only
	path       string         // default "/metrics"
	tlsConfig  *tls.Config    // nil = plain HTTP
	isDefault  bool
	otlpClient cfg.OtlpClient // nil = no explicit tracing
}

// GetHandler returns the HTTP handler for the metrics endpoint.
// Implements cfg.HandlerServer.
func (s *MetricsServer) GetHandler() http.Handler {
	return s.handler
}

// GetMetricsProvider returns the o11y.MetricsProvider backed by this server's registry.
// Implements cfg.MetricsRegistrar.
func (s *MetricsServer) GetMetricsProvider() o11y.MetricsProvider {
	return s.provider
}

// GetRegistry returns the underlying prometheus registry.
// Implements cfg.MetricsRegistrar.
func (s *MetricsServer) GetRegistry() *prometheus.Registry {
	return s.registry
}

// IsDefaultServer reports whether this server is the default metrics server.
// Implements cfg.MetricsRegistrar.
func (s *MetricsServer) IsDefaultServer() bool {
	return s.isDefault
}

// Start starts a standalone HTTP listener if listen is set; otherwise a no-op.
func (s *MetricsServer) Start() error {
	if s.listen == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(s.path, s.handler)

	// Wrap with otelhttp for trace context extraction (same pattern as server "http").
	var otelOpts []otelhttp.Option
	if s.otlpClient != nil {
		if tp := s.otlpClient.GetTracerProvider(); tp != nil {
			otelOpts = append(otelOpts, otelhttp.WithTracerProvider(tp))
		}
	}
	otelOpts = append(otelOpts,
		otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)),
		otelhttp.WithServerName(s.GetName()),
	)
	tracedMux := otelhttp.NewHandler(mux, "",
		append(otelOpts, otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}))...,
	)

	srv := &http.Server{
		Addr:    s.listen,
		Handler: tracedMux,
	}

	go func() {
		var err error
		if s.tlsConfig != nil {
			srv.TLSConfig = s.tlsConfig
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			_ = err
		}
	}()

	return nil
}

// newMetricsServer constructs a MetricsServer with a private registry.
func newMetricsServer(name string, defRange hcl.Range, listen, path string, isDefault, includeGoMetrics bool, tlsCfg *tls.Config, otlpClient cfg.OtlpClient) *MetricsServer {
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
		BaseServer: cfg.BaseServer{Name: name, DefRange: defRange},
		registry:   reg,
		provider:   provider,
		handler:    handler,
		listen:     listen,
		path:       path,
		tlsConfig:  tlsCfg,
		isDefault:  isDefault,
		otlpClient: otlpClient,
	}
}

// MetricsServerDefinition holds the decoded HCL attributes for a server "metrics" block.
type MetricsServerDefinition struct {
	Listen           *string         `hcl:"listen,optional"`
	Path             *string         `hcl:"path,optional"`
	Default          *bool           `hcl:"default,optional"`
	IncludeGoMetrics *bool           `hcl:"include_go_metrics,optional"`
	Tracing          hcl.Expression  `hcl:"tracing,optional"`
	TLS              *cfg.TLSConfig  `hcl:"tls,block"`
	Auth             *cfg.AuthConfig `hcl:"auth,block"`
	DefRange         hcl.Range       `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("metrics", ProcessMetricsServerBlock)
}

// ProcessMetricsServerBlock decodes and creates a MetricsServer from a block body.
func ProcessMetricsServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	def := MetricsServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
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

	var tlsCfg *tls.Config
	if def.TLS != nil {
		if listen == "" {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "TLS requires standalone mode",
					Detail:   "A tls block can only be used on a server \"metrics\" block that also has a listen address.",
					Subject:  &def.TLS.DefRange,
				},
			}
		}
		var err error
		tlsCfg, err = def.TLS.BuildTLSServerConfig(config.BaseDir)
		if err != nil {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid TLS configuration",
					Detail:   err.Error(),
					Subject:  &def.TLS.DefRange,
				},
			}
		}
	}

	// Validate auth block if present.
	if def.Auth != nil {
		if authDiags := cfg.ValidateAuthConfig(def.Auth); authDiags.HasErrors() {
			return nil, authDiags
		}
	}

	// Resolve tracing client.
	otlpClient, tracingDiags := config.ResolveOtlpClient(def.Tracing)
	if tracingDiags.HasErrors() {
		return nil, tracingDiags
	}

	srv := newMetricsServer(name, def.DefRange, listen, path, isDefault, includeGoMetrics, tlsCfg, otlpClient)

	// Wrap the metrics handler with auth middleware if configured.
	if def.Auth != nil && def.Auth.Mode != "none" {
		authenticator, err := metricsauth.BuildAuthenticator(def.Auth, name, config.EvalCtx())
		if err != nil {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to build auth",
					Detail:   err.Error(),
					Subject:  &def.Auth.DefRange,
				},
			}
		}
		if authenticator != nil {
			srv.handler = metricsauth.NewAuthMiddleware(authenticator, config.EvalCtx(), config.Logger, srv.handler)
		}
	}

	if config.MetricsServers == nil {
		config.MetricsServers = make(map[string]cfg.MetricsRegistrar)
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
func GetMetricsServerFromExpression(config *cfg.Config, expr hcl.Expression) (*MetricsServer, hcl.Diagnostics) {
	registrar, diags := cfg.GetMetricsRegistrarFromExpression(config, expr)
	if diags.HasErrors() {
		return nil, diags
	}

	ms, ok := registrar.(*MetricsServer)
	if !ok {
		exprRange := expr.Range()
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Server is not a metrics server",
				Detail:   fmt.Sprintf("Expected a server \"metrics\" block, got server %q of a different type", registrar.GetName()),
				Subject:  &exprRange,
			},
		}
	}

	return ms, nil
}
