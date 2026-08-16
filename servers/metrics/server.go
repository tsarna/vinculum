package metricsserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

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
	"go.uber.org/zap"
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
	logger        *zap.Logger

	// shutdownTimeout bounds how long Drain waits for in-flight scrapes.
	shutdownTimeout time.Duration
	// httpSrv is the standalone listener, set by Start and read by Drain.
	// Nil in mounted mode, where the server "http" block owns the listener.
	httpSrv *http.Server
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
	// Retained so Drain can close it on shutdown.
	s.httpSrv = srv

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

// Drain stops accepting new scrapes and waits for those in flight, up to the
// configured shutdown_timeout. A mounted server has no listener of its own, so
// this is a no-op there and the hosting server "http" block drains.
func (s *MetricsServer) Drain(ctx context.Context) error {
	return cfg.DrainHTTPServer(ctx, s.httpSrv, s.shutdownTimeout, s.logger, "metrics", s.GetName())
}

// newMetricsServer constructs a MetricsServer with a private registry and an
// OTel MeterProvider bridged to it via the OTel→Prometheus exporter.
func newMetricsServer(name string, defRange hcl.Range, listen, path string, isDefault, includeGoMetrics bool, tlsCfg *tls.Config, otlpClient cfg.OtlpClient, shutdownTimeout time.Duration, logger *zap.Logger) (*MetricsServer, error) {
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
		logger:        logger,

		shutdownTimeout: shutdownTimeout,
	}, nil
}

// MetricsServerDefinition holds the decoded HCL attributes for a server "metrics" block.
type MetricsServerDefinition struct {
	Listen           *string         `hcl:"listen,optional"`
	Path             *string         `hcl:"path,optional"`
	DefaultMetrics   *bool           `hcl:"default_metrics,optional"`
	IncludeGoMetrics *bool           `hcl:"include_go_metrics,optional"`
	Tracing          hcl.Expression  `hcl:"tracing,optional"`
	ShutdownTimeout  hcl.Expression  `hcl:"shutdown_timeout,optional"`
	TLS              *cfg.TLSConfig  `hcl:"tls,block"`
	Auth             *cfg.AuthConfig `hcl:"auth,block"`
	DefRange         hcl.Range       `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("metrics", ProcessMetricsServerBlock, cfg.WithSchema(metricsServerSchema))
}

var metricsServerSchema = cfg.TypeSchema{
	Sample:  &MetricsServerDefinition{},
	Summary: "A Prometheus-style metrics endpoint.",
	DocPage: "server-metrics.md",
	Doc: `Exposes metrics for scraping, and acts as a metrics backend that ` + "`metric`" + `
blocks and instrumented blocks report through.

With ` + "`listen`" + ` it runs its own HTTP server; without one, mount it on a route of a
` + "`server \"http\"`" + ` block with ` + "`handler = server.<name>`" + `.`,
	Attrs: map[string]cfg.AttrMeta{
		"listen": {
			Summary: "Address to serve metrics on, as a standalone server.",
			Doc:     "Omit to mount this server into a `server \"http\"` route instead.",
			Hint:    cfg.HintListenAddr,
		},
		"path": {
			Summary: "Path the metrics are served at.",
			Doc:     "Standalone mode only. A mounted server is reached at the route its `handle` block declares, and setting this alongside a mount warns.",
			Default: "/metrics",
		},
		"default_metrics": {
			Summary: "Make this the default metrics backend.",
			Doc: `Blocks that report metrics without naming a backend use the default. A
single metrics-capable block — this or a ` + "`client \"otlp\"`" + ` — is the default
automatically; with several, exactly one may set this.`,
			Hint: cfg.HintBool,
		},
		"include_go_metrics": {
			Summary: "Register Go runtime metrics.",
			Doc:     "Set false to omit the goroutine, memory, and GC instrumentation.",
			Hint:    cfg.HintBool,
			Default: "true",
		},
		"tracing": {
			Summary: "Where to report traces for scrape requests.",
			Doc:     "A `client \"otlp\"` block. Auto-wires to the default when omitted.",
			Hint:    cfg.HintTracingRef,
		},
		"shutdown_timeout": cfg.ShutdownTimeoutAttr.WithDoc(
			"On shutdown the server stops accepting new scrapes before anything else is torn down, " +
				"then waits this long for those already in flight. Whatever is still running when the " +
				"time is up is closed out from under it. `0` waits indefinitely. " +
				"Standalone mode only — a mounted server drains with the `server \"http\"` block hosting it."),
	},
}

// ProcessMetricsServerBlock decodes and creates a MetricsServer from a block body.
func ProcessMetricsServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	def := MetricsServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

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

	shutdownTimeout, timeoutDiags := config.ParseDurationOrDefault(def.ShutdownTimeout, cfg.DefaultShutdownTimeout)
	if timeoutDiags.HasErrors() {
		return nil, timeoutDiags
	}

	srv, err := newMetricsServer(name, def.DefRange, listen, path, isDefault, includeGoMetrics, tlsCfg, otlpClient, shutdownTimeout, config.Logger)
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

	// Only a standalone server owns a listener; a mounted one is drained by the
	// server "http" block that hosts it.
	if listen != "" {
		config.Startables = append(config.Startables, srv)
		config.Drainables = append(config.Drainables, srv)
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
