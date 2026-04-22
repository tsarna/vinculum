package httpserver

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	serverauth "github.com/tsarna/vinculum/servers/auth"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type HttpServer struct {
	cfg.BaseServer
	Logger    *zap.Logger
	Server    *http.Server
	TLSConfig *tls.Config

	// otlpClient is resolved at config parse time; may be nil.
	otlpClient    cfg.OtlpClient
	meterProvider otelmetric.MeterProvider // nil = no HTTP metrics
}

type HttpServerDefinition struct {
	Listen      string                  `hcl:"listen"`
	TLS         *cfg.TLSConfig          `hcl:"tls,block"`
	Auth        *cfg.AuthConfig         `hcl:"auth,block"`
	Tracing     hcl.Expression          `hcl:"tracing,optional"`
	Metrics     hcl.Expression          `hcl:"metrics,optional"`
	DefRange    hcl.Range               `hcl:",def_range"`
	StaticFiles []staticFilesDefinition `hcl:"files,block"`
	Handlers    []handlerDefinition     `hcl:"handle,block"`
}

type staticFilesDefinition struct {
	UrlPath   string          `hcl:"urlpath,label"`
	Directory string          `hcl:"directory"`
	Auth      *cfg.AuthConfig `hcl:"auth,block"`
	Disabled  bool            `hcl:"disabled,optional"`
	DefRange  hcl.Range       `hcl:",def_range"`
}

type handlerDefinition struct {
	Route    string          `hcl:"route,label"`
	Auth     *cfg.AuthConfig `hcl:"auth,block"`
	Action   hcl.Expression  `hcl:"action,optional"`
	Handler  hcl.Expression  `hcl:"handler,optional"`
	Disabled bool            `hcl:"disabled,optional"`
	DefRange hcl.Range       `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("http", ProcessHttpServerBlock)
}

func ProcessHttpServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	serverDef := HttpServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &serverDef)
	if diags.HasErrors() {
		return nil, diags
	}
	serverDef.DefRange = block.DefRange

	if serverDef.Auth != nil {
		if authDiags := cfg.ValidateAuthConfig(serverDef.Auth); authDiags.HasErrors() {
			return nil, authDiags
		}
	}

	httpServers, ok := config.Servers["http"]
	if !ok {
		httpServers = make(map[string]cfg.Listener)
		config.Servers["http"] = httpServers
	}

	serverName := block.Labels[1]

	existing, ok := httpServers[serverName]
	if ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Http server already defined",
				Detail:   fmt.Sprintf("Http server %s already defined at %s", serverName, existing.GetDefRange()),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	// Resolve tracing client at config parse time.
	otlpClient, tracingDiags := config.ResolveOtlpClient(serverDef.Tracing)
	if tracingDiags.HasErrors() {
		return nil, tracingDiags
	}

	// Resolve metrics backend at config parse time.
	mp, metricsDiags := cfg.ResolveMeterProvider(config, serverDef.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}

	server := &HttpServer{
		Logger: config.Logger,
		BaseServer: cfg.BaseServer{
			Name:     serverName,
			DefRange: serverDef.DefRange,
		},
		otlpClient:    otlpClient,
		meterProvider: mp,
	}

	mux := http.NewServeMux()

	for _, file := range serverDef.StaticFiles {
		if file.Disabled {
			continue
		}

		if strings.Contains(file.UrlPath, " ") {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid URL path",
					Detail:   fmt.Sprintf("Invalid urlpath: %s", file.UrlPath),
					Subject:  &file.DefRange,
				},
			}
		}

		if config.BaseDir == "" {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "files block requires --file-path",
					Detail:   "A files block requires vinculum serve to be started with --file-path",
					Subject:  &file.DefRange,
				},
			}
		}

		dir := file.Directory
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(config.BaseDir, dir)
		}

		path := strings.TrimSuffix(file.UrlPath, "/") + "/"

		if file.Auth != nil {
			if authDiags := cfg.ValidateAuthConfig(file.Auth); authDiags.HasErrors() {
				return nil, authDiags
			}
		}

		effectiveAuth := resolveAuth(serverDef.Auth, file.Auth)
		var inner http.Handler = http.StripPrefix(path, http.FileServer(http.Dir(dir)))
		inner, diags = wrapWithAuth(inner, effectiveAuth, serverName, config, &file.DefRange)
		if diags.HasErrors() {
			return nil, diags
		}
		inner = newLoggingMiddleware(config.Logger, path, inner)
		mux.Handle(path, inner)
	}

	for _, handlerDef := range serverDef.Handlers {
		if handlerDef.Disabled {
			continue
		}

		if cfg.IsExpressionProvided(handlerDef.Handler) && cfg.IsExpressionProvided(handlerDef.Action) || !cfg.IsExpressionProvided(handlerDef.Handler) && !cfg.IsExpressionProvided(handlerDef.Action) {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Exactly one of handler or action must be specified",
					Subject:  &handlerDef.DefRange,
				},
			}
		}

		if handlerDef.Auth != nil {
			if authDiags := cfg.ValidateAuthConfig(handlerDef.Auth); authDiags.HasErrors() {
				return nil, authDiags
			}
		}

		var inner http.Handler
		if cfg.IsExpressionProvided(handlerDef.Action) {
			inner = &httpAction{
				config:         config,
				actionExpr:     handlerDef.Action,
				pathParamNames: types.ExtractPathParams(handlerDef.Route),
			}
		} else {
			handler, handlerDiags := cfg.GetServerFromExpression(config, handlerDef.Handler)
			if handlerDiags.HasErrors() {
				return nil, handlerDiags
			}

			handlerServer, ok := handler.(cfg.HandlerServer)
			if !ok {
				return nil, hcl.Diagnostics{
					&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Provided handler does not implement Handler interface",
						Subject:  &handlerDef.DefRange,
					},
				}
			}
			inner = handlerServer.GetHandler()
		}

		effectiveAuth := resolveAuth(serverDef.Auth, handlerDef.Auth)
		inner, diags = wrapWithAuth(inner, effectiveAuth, serverName, config, &handlerDef.DefRange)
		if diags.HasErrors() {
			return nil, diags
		}
		inner = newLoggingMiddleware(config.Logger, handlerDef.Route, inner)
		mux.Handle(handlerDef.Route, inner)
	}

	server.Server = &http.Server{
		Addr:    serverDef.Listen,
		Handler: mux,
	}

	if serverDef.TLS != nil {
		tlsCfg, err := serverDef.TLS.BuildTLSServerConfig(config.BaseDir)
		if err != nil {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid TLS configuration",
					Detail:   err.Error(),
					Subject:  &serverDef.TLS.DefRange,
				},
			}
		}
		server.TLSConfig = tlsCfg
	}

	config.Startables = append(config.Startables, server)

	return server, nil
}

func resolveAuth(serverAuth, blockAuth *cfg.AuthConfig) *cfg.AuthConfig {
	if blockAuth != nil {
		return blockAuth
	}
	return serverAuth
}

func wrapWithAuth(handler http.Handler, effectiveAuth *cfg.AuthConfig, serverName string, config *cfg.Config, defRange *hcl.Range) (http.Handler, hcl.Diagnostics) {
	if effectiveAuth == nil || effectiveAuth.Mode == "none" {
		return handler, nil
	}

	authenticator, err := serverauth.BuildAuthenticator(effectiveAuth, serverName, config.EvalCtx())
	if err != nil {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to build auth",
				Detail:   err.Error(),
				Subject:  defRange,
			},
		}
	}
	if authenticator == nil {
		return handler, nil
	}

	return serverauth.NewAuthMiddleware(authenticator, config.EvalCtx(), config.Logger, handler), nil
}

func (h *HttpServer) Start() error {
	// Build otelhttp options. Use the explicitly configured (or default) OTLP
	// client's TracerProvider when available; otherwise fall back to the global
	// NOOP provider so otelhttp is safe to use even without a tracing client.
	var otelOpts []otelhttp.Option
	if h.otlpClient != nil {
		if tp := h.otlpClient.GetTracerProvider(); tp != nil {
			otelOpts = append(otelOpts, otelhttp.WithTracerProvider(tp))
		}
	}
	if h.meterProvider != nil {
		otelOpts = append(otelOpts, otelhttp.WithMeterProvider(h.meterProvider))
	}
	otelOpts = append(otelOpts,
		// Always use W3C TraceContext + Baggage propagation regardless of whether
		// a client "otlp" is configured. This means traceparent headers are always
		// extracted and ctx.trace_id is populated even with no tracing backend.
		otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)),
		otelhttp.WithServerName(h.Name),
	)

	// Wrap the entire mux with otelhttp for tracing.
	tracedHandler := otelhttp.NewHandler(h.Server.Handler, "",
		append(otelOpts, otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}))...,
	)
	h.Server.Handler = tracedHandler

	go func() {
		h.Logger.Info("Starting HTTP server", zap.String("name", h.Name), zap.String("addr", h.Server.Addr))
		var err error
		if h.TLSConfig != nil {
			h.Server.TLSConfig = h.TLSConfig
			err = h.Server.ListenAndServeTLS("", "")
		} else {
			err = h.Server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			h.Logger.Error("Failed to start HTTP server", zap.Error(err))
		}
	}()

	return nil
}

// ─── loggingMiddleware ────────────────────────────────────────────────────────

type statusCapturingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusCapturingResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusCapturingResponseWriter) effectiveStatus() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

type loggingMiddleware struct {
	next   http.Handler
	logger *zap.Logger
	route  string
}

func newLoggingMiddleware(logger *zap.Logger, route string, next http.Handler) http.Handler {
	return &loggingMiddleware{logger: logger, route: route, next: next}
}

func (l *loggingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusCapturingResponseWriter{ResponseWriter: w}
	l.next.ServeHTTP(sw, r)

	status := sw.effectiveStatus()
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0

	fields := []zap.Field{
		zap.String("method", r.Method),
		zap.String("route", l.route),
		zap.String("path", r.URL.Path),
		zap.Int("status", status),
		zap.Float64("duration_ms", durationMs),
		zap.Int("bytes", sw.bytes),
	}

	// Include trace_id for log-trace correlation when a span is active.
	if span := trace.SpanFromContext(r.Context()); span.SpanContext().IsValid() {
		fields = append(fields, zap.String("trace_id", span.SpanContext().TraceID().String()))
	}

	l.logger.Info("Request", fields...)
}

// ─── httpAction ───────────────────────────────────────────────────────────────

type httpAction struct {
	config         *cfg.Config
	actionExpr     hcl.Expression
	pathParamNames []string
}

func (h *httpAction) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	evalCtx, err := getHttpActionEvalContext(h.config, r, h.pathParamNames)
	if err != nil {
		h.config.Logger.Error("Error building evaluation context", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	val, diags := h.actionExpr.Value(evalCtx)
	if diags.HasErrors() {
		h.config.Logger.Error("Error executing action", zap.Error(diags))
		http.Error(w, diags.Error(), http.StatusInternalServerError)
		return
	}

	writeHTTPResponse(h.config.Logger, w, val)
}

func getHttpActionEvalContext(config *cfg.Config, r *http.Request, pathParamNames []string) (*hcl.EvalContext, error) {
	builder := hclutil.NewEvalContext(r.Context()).
		WithAttribute("request", types.BuildHTTPRequestObject(r, pathParamNames))

	return builder.BuildEvalContext(config.EvalCtx())
}

func writeHTTPResponse(logger *zap.Logger, w http.ResponseWriter, val cty.Value) {
	if resp, ok := types.GetHTTPResponseFromValue(val); ok {
		if resp.IsError {
			logger.Warn("HTTP action returned http_error",
				zap.Int("status", resp.Status),
				zap.String("body", string(resp.Body)))
		}
		writeResponseFromWrapper(w, resp)
		return
	}

	if val.IsNull() {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, contentType, err := types.CoerceBodyToBytes(val)
	if err != nil {
		logger.Error("Failed to coerce action return value to HTTP response", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	if len(body) > 0 {
		w.Write(body)
	}
}

func writeResponseFromWrapper(w http.ResponseWriter, resp *types.HTTPResponseWrapper) {
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
		w.Write(resp.Body)
	}
}
