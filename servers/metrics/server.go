package metricsserver

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	cfg "github.com/tsarna/vinculum/config"
	metricsauth "github.com/tsarna/vinculum/servers/auth"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// MetricsServer implements a Prometheus/OpenMetrics exposition server.
// It can operate in standalone mode (owns its own HTTP listener) or mounted
// mode (implements HandlerServer so it can be attached to a server "http" block).
type MetricsServer struct {
	cfg.BaseServer
	registry      *prometheus.Registry
	meterProvider *sdkmetric.MeterProvider
	handler       http.Handler
	listen        string      // empty = mounted mode only
	path          string      // default "/metrics"
	tlsConfig     *tls.Config // nil = plain HTTP
	isDefault     bool
	otlpClient    cfg.OtlpClient // nil = no explicit tracing
}

// GetHandler returns the HTTP handler for the metrics endpoint.
// Implements cfg.HandlerServer.
func (s *MetricsServer) GetHandler() http.Handler {
	return s.handler
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

// GetMeterProvider returns the OTel MeterProvider bridged to this server's Prometheus registry.
// Implements cfg.MetricsRegistrar.
func (s *MetricsServer) GetMeterProvider() otelmetric.MeterProvider {
	return s.meterProvider
}

// IsDefaultMetricsBackend reports whether this server is the default metrics backend.
// Implements cfg.MetricsRegistrar.
func (s *MetricsServer) IsDefaultMetricsBackend() bool {
	return s.isDefault
}

// Start starts a standalone HTTP listener if listen is set; otherwise a no-op.
func (s *MetricsServer) Start() error {
	if s.listen == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(s.path, s.handler)

	// Wrap with otelhttp for trace context extraction and HTTP metrics.
	var otelOpts []otelhttp.Option
	if s.otlpClient != nil {
		if tp := s.otlpClient.GetTracerProvider(); tp != nil {
			otelOpts = append(otelOpts, otelhttp.WithTracerProvider(tp))
		}
	}
	if s.meterProvider != nil {
		otelOpts = append(otelOpts, otelhttp.WithMeterProvider(s.meterProvider))
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

// newMetricsServer constructs a MetricsServer with a private registry and an
// OTel MeterProvider bridged to it via the OTel→Prometheus exporter.
func newMetricsServer(name string, defRange hcl.Range, listen, path string, isDefault, includeGoMetrics bool, tlsCfg *tls.Config, otlpClient cfg.OtlpClient) (*MetricsServer, error) {
	reg := prometheus.NewRegistry()

	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("server \"metrics\" %q: prometheus exporter: %w", name, err)
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	if includeGoMetrics {
		if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
			return nil, fmt.Errorf("server \"metrics\" %q: runtime metrics: %w", name, err)
		}
	}

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	return &MetricsServer{
		BaseServer:    cfg.BaseServer{Name: name, DefRange: defRange},
		registry:      reg,
		meterProvider: mp,
		handler:       handler,
		listen:        listen,
		path:          path,
		tlsConfig:     tlsCfg,
		isDefault:     isDefault,
		otlpClient:    otlpClient,
	}, nil
}

// MetricsServerDefinition holds the decoded HCL attributes for a server "metrics" block.
type MetricsServerDefinition struct {
	Listen           *string         `hcl:"listen,optional"`
	Path             *string         `hcl:"path,optional"`
	DefaultMetrics   *bool           `hcl:"default_metrics,optional"`
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
	if def.DefaultMetrics != nil {
		isDefault = *def.DefaultMetrics
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

	srv, err := newMetricsServer(name, def.DefRange, listen, path, isDefault, includeGoMetrics, tlsCfg, otlpClient)
	if err != nil {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to create metrics server",
				Detail:   err.Error(),
				Subject:  def.DefRange.Ptr(),
			},
		}
	}

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
