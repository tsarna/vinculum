package httpserver

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
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

	// realIP rewrites RemoteAddr from a forwarded header when set; may be nil.
	realIP *realIPResolver

	// baggageFilter strips/limits inbound baggage when set; may be nil.
	baggageFilter *hclutil.BaggageFilterConfig

	// shutdownTimeout bounds how long Drain waits for in-flight requests
	// before forcing the remaining connections closed. Zero waits forever.
	shutdownTimeout time.Duration
}

type HttpServerDefinition struct {
	Listen          string                       `hcl:"listen"`
	ShutdownTimeout hcl.Expression               `hcl:"shutdown_timeout,optional"`
	TLS             *cfg.TLSConfig               `hcl:"tls,block"`
	Auth            hcl.Expression               `hcl:"auth,optional"`
	RealIP          *realIPConfig                `hcl:"real_ip,block"`
	Tracing         hcl.Expression               `hcl:"tracing,optional"`
	Metrics         hcl.Expression               `hcl:"metrics,optional"`
	Baggage         *hclutil.BaggageFilterConfig `hcl:"baggage,block"`
	DefRange        hcl.Range                    `hcl:",def_range"`
	StaticFiles     []staticFilesDefinition      `hcl:"files,block"`
	Handlers        []handlerDefinition          `hcl:"handle,block"`
}

type staticFilesDefinition struct {
	UrlPath   string         `hcl:"urlpath,label"`
	Directory string         `hcl:"directory"`
	Auth      hcl.Expression `hcl:"auth,optional"`
	Disabled  bool           `hcl:"disabled,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type handlerDefinition struct {
	Route    string         `hcl:"route,label"`
	Auth     hcl.Expression `hcl:"auth,optional"`
	Action   hcl.Expression `hcl:"action,optional"`
	Handler  hcl.Expression `hcl:"handler,optional"`
	Disabled bool           `hcl:"disabled,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("http", ProcessHttpServerBlock, cfg.WithSchema(httpServerSchema))
}

var httpServerSchema = cfg.TypeSchema{
	Sample:  &HttpServerDefinition{},
	Summary: "An HTTP server exposing request handlers and static files.",
	DocPage: "server-http.md",
	Doc: `Serves ` + "`handle`" + ` routes and ` + "`files`" + ` trees over HTTP, and is available in
expressions as ` + "`server.<name>`" + `. Other servers that expose an HTTP handler — MCP,
metrics — can be mounted into a route with ` + "`handler = server.<name>`" + `.`,
	Attrs: map[string]cfg.AttrMeta{
		"listen": {
			Summary: "Address and port to listen on.",
			Doc:     "For example `\":8080\"` or `\"127.0.0.1:9090\"`.",
			Hint:    cfg.HintListenAddr,
		},
		"tracing":          cfg.TracingAttr,
		"metrics":          cfg.MetricsAttr,
		"shutdown_timeout": cfg.ShutdownTimeoutAttr,
		"auth": cfg.AuthAttr.WithDoc(
			"An `auth.<name>` reference, or a list of them. Every `handle` and `files` " +
				"block inherits it unless it sets its own `auth`, and a block that sets " +
				"one replaces this rather than adding to it — including `auth = auth.anonymous`, " +
				"which is how a route opts out. Omitted, nothing is required anywhere."),
	},
	Blocks: map[string]cfg.TypeSchema{
		"real_ip": {
			Summary: "Recover the client's real IP from a forwarded header.",
			Doc:     "Without this block, `ctx.request.remote_addr` is the immediate peer, which behind a proxy is the proxy itself.",
			Attrs: map[string]cfg.AttrMeta{
				"trusted_proxies": {
					Summary: "CIDRs or bare IPs whose forwarded headers are believed.",
					Doc: "A header arriving from any other address is ignored, which is what " +
						"stops a client spoofing its own IP by sending one. nginx spells this " +
						"`set_real_ip_from`.",
				},
				"header": {
					Summary: "Header to read the client address from.",
					Doc: "Any header works; a single-valued one such as `X-Real-IP` is just " +
						"the one-element case. nginx spells this `real_ip_header`.",
					Default: "X-Forwarded-For",
				},
				"recursive": {
					Summary: "Walk the header right to left, skipping trusted proxies.",
					Doc: "The first untrusted address found is the client. Use it when a " +
						"chain of proxies each append an address; without it the rightmost " +
						"entry is taken, which is right for a single hop. nginx spells this " +
						"`real_ip_recursive`.",
					Hint:    cfg.HintBool,
					Default: "false",
				},
				"disabled": {
					Summary: "Skip this block entirely.",
					Hint:    cfg.HintBool,
				},
			},
		},
		"handle": {
			Summary: "A route handler.",
			Doc: `The label is a Go 1.22 ` + "`http.ServeMux`" + ` pattern — ` + "`[METHOD ][HOST]/[PATH]`" + `.
` + "`GET /api/status`" + ` matches one method and path, ` + "`/api/`" + ` matches a subtree,
` + "`/{$}`" + ` matches only ` + "`/`" + `, and ` + "`/items/{id}`" + ` captures a segment readable as
` + "`ctx.request.path.id`" + `.`,
			Attrs: map[string]cfg.AttrMeta{
				"action": {
					Summary: "Expression evaluated for each matching request.",
					Doc: `Its value becomes the response: a string is sent as ` + "`text/plain`" + `, a
bytes object with its own content type, anything else as JSON, and ` + "`null`" + ` as
204. Use ` + "`http::response()`" + ` or ` + "`http::error()`" + ` to control the status.`,
					Hint:    cfg.HintActionExpression,
					Context: "http-request",
				},
				"handler": {
					Summary: "Another server to delegate this route to.",
					Doc:     "Mounts a server that exposes an HTTP handler, such as `server \"mcp\"` or `server \"metrics\"`.",
					Hint:    cfg.HintServerRef,
				},
				"disabled": {
					Summary: "Skip this route entirely.",
					Hint:    cfg.HintBool,
				},
				"auth": cfg.AuthAttr.WithDoc(
					"An `auth.<name>` reference, or a list of them, replacing whatever the " +
						"server requires. `auth = auth.anonymous` opts this route out of the " +
						"server's authentication — for a health check, or a login endpoint " +
						"that cannot require the credential it issues."),
			},
			Constraints: []cfg.Constraint{
				cfg.MutuallyExclusive("action", "handler"),
				cfg.AtLeastOneOf("action", "handler").
					WithMessage("A route needs either an action to evaluate or a handler to delegate to."),
			},
		},
		"files": {
			Summary: "Serves a directory tree of static files.",
			Doc:     "The label is the URL path the tree is mounted at.",
			Attrs: map[string]cfg.AttrMeta{
				"directory": {
					Summary: "Directory to serve files from.",
					Doc: "A relative path resolves against the `--file-path` base directory, " +
						"which `vinculum serve` requires whenever any `files` block is active.",
				},
				"disabled": {
					Summary: "Skip this tree entirely.",
					Hint:    cfg.HintBool,
				},
				"auth": cfg.AuthAttr.WithDoc(
					"An `auth.<name>` reference, or a list of them, replacing whatever the " +
						"server requires. `auth = auth.anonymous` serves this tree to anyone."),
			},
		},
	},
}

func ProcessHttpServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	serverDef := HttpServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &serverDef)
	if diags.HasErrors() {
		return nil, diags
	}
	serverDef.DefRange = block.DefRange

	// The server-level policy, inherited by every route that does not set its own.
	serverAuth, authDiags := cfg.ResolveAuth(config, serverDef.Auth)
	if authDiags.HasErrors() {
		return nil, authDiags
	}

	if baggageDiags := serverDef.Baggage.Validate(); baggageDiags.HasErrors() {
		return nil, baggageDiags
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

	// Compile the real_ip block (trusted-proxy / forwarded-header handling).
	// A disabled block is inert — skip compilation so its required-field checks
	// (e.g. trusted_proxies) don't fire when the feature is env-toggled off.
	var realIP *realIPResolver
	if serverDef.RealIP != nil && !serverDef.RealIP.Disabled {
		var realIPDiags hcl.Diagnostics
		realIP, realIPDiags = compileRealIP(serverDef.RealIP)
		if realIPDiags.HasErrors() {
			return nil, realIPDiags
		}
	}

	shutdownTimeout, diags := config.ParseDurationOrDefault(serverDef.ShutdownTimeout, cfg.DefaultShutdownTimeout)
	if diags.HasErrors() {
		return nil, diags
	}

	server := &HttpServer{
		Logger: config.Logger,
		BaseServer: cfg.BaseServer{
			Name:     serverName,
			DefRange: serverDef.DefRange,
		},
		otlpClient:      otlpClient,
		meterProvider:   mp,
		realIP:          realIP,
		baggageFilter:   serverDef.Baggage,
		shutdownTimeout: shutdownTimeout,
	}

	mux := http.NewServeMux()

	for _, file := range serverDef.StaticFiles {
		if file.Disabled {
			continue
		}

		method, host, rawPath, ok := splitPattern(file.UrlPath)
		if !ok {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid URL path",
					Detail:   fmt.Sprintf("files urlpath must contain a path starting with \"/\": %s", file.UrlPath),
					Subject:  &file.DefRange,
				},
			}
		}
		if method != "" {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Method not allowed on files block",
					Detail:   fmt.Sprintf("a method is not allowed on a files block (a file server serves GET/HEAD only): %s", file.UrlPath),
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

		// StripPrefix uses the path portion only, since the request URL never
		// contains the host. The mux is registered with the host-qualified
		// pattern so ServeMux scopes the route to that host; a host-less block
		// (host == "") registers a path-only pattern that matches all hosts.
		urlPath := strings.TrimSuffix(rawPath, "/") + "/"
		pattern := host + urlPath

		effectiveAuth, authDiags := routeAuth(config, serverAuth, file.Auth)
		if authDiags.HasErrors() {
			return nil, authDiags
		}
		reportAnonymous(config, effectiveAuth, pattern, file.DefRange)

		var inner http.Handler = http.StripPrefix(urlPath, http.FileServer(http.Dir(dir)))
		inner = serverauth.NewAuthMiddleware(effectiveAuth, config.EvalCtx(), config.Logger, inner)
		inner = newLoggingMiddleware(config.Logger, pattern, inner)
		if diags := safeMuxHandle(mux, pattern, inner, file.DefRange); diags.HasErrors() {
			return nil, diags
		}
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

		effectiveAuth, authDiags := routeAuth(config, serverAuth, handlerDef.Auth)
		if authDiags.HasErrors() {
			return nil, authDiags
		}
		reportAnonymous(config, effectiveAuth, handlerDef.Route, handlerDef.DefRange)

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

		inner = serverauth.NewAuthMiddleware(effectiveAuth, config.EvalCtx(), config.Logger, inner)
		inner = newLoggingMiddleware(config.Logger, handlerDef.Route, inner)
		if diags := safeMuxHandle(mux, handlerDef.Route, inner, handlerDef.DefRange); diags.HasErrors() {
			return nil, diags
		}
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
	config.Drainables = append(config.Drainables, server)

	return server, nil
}

// routeAuth resolves a route's own `auth`, falling back to the server's.
//
// A route that says nothing inherits; one that names anything at all — a
// mechanism, a list, or auth.anonymous — replaces rather than adds to what it
// inherited, so what protects a route is readable from the route.
func routeAuth(config *cfg.Config, serverAuth *cfg.AuthPolicy, routeExpr hcl.Expression) (*cfg.AuthPolicy, hcl.Diagnostics) {
	policy, diags := cfg.ResolveAuth(config, routeExpr)
	if diags.HasErrors() {
		return nil, diags
	}
	if policy == nil {
		return serverAuth, nil
	}
	return policy, nil
}

// reportAnonymous warns when a route ends up unauthenticated by consequence
// rather than by intent — every mechanism it named turned out to be disabled.
//
// Disabling one of several mechanisms leaves a route protected and says nothing.
// Writing auth.anonymous says nothing either: that is the deliberate,
// unconditional opt-out, and warning about it would train people to ignore the
// warning. What is worth a line is the case where a toggle nobody was thinking
// about took the last mechanism away.
func reportAnonymous(config *cfg.Config, policy *cfg.AuthPolicy, route string, defRange hcl.Range) {
	if policy == nil || policy.Enforced() || len(policy.Names) == 0 {
		return
	}
	for _, name := range policy.Names {
		if name == cfg.AuthAnonymousName {
			return
		}
	}

	config.UserLogger.Warn("Route is unauthenticated because every auth block it names is disabled",
		zap.String("route", route),
		zap.Strings("auth", policy.Names),
		zap.String("at", defRange.String()),
	)
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

	// Wrap the mux with the baggage filter (if configured) INSIDE otelhttp, so it
	// runs after the W3C propagator extracts baggage but before any handler.
	inner := h.baggageFilter.Middleware(h.Logger, h.Server.Handler)

	// Wrap the entire mux with otelhttp for tracing.
	tracedHandler := otelhttp.NewHandler(inner, "",
		append(otelOpts, otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}))...,
	)
	h.Server.Handler = tracedHandler

	// Resolve the real client IP from a forwarded header before anything else
	// runs, so tracing, logging, auth, and ctx.request.remote_addr all see the
	// corrected address.
	if h.realIP != nil {
		h.Server.Handler = h.realIP.wrap(h.Server.Handler)
	}

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

// Drain stops accepting new connections and waits for in-flight requests to
// finish, up to the configured shutdown_timeout. It runs before any client or
// bus is stopped, so a request still being served keeps the runtime it needs.
//
// Hijacked connections (WebSocket upgrades) are not covered — Shutdown leaves
// them alone by design. The vws and websocket servers drain their own
// connections, after this, as their own Drainables.
func (h *HttpServer) Drain(ctx context.Context) error {
	return cfg.DrainHTTPServer(ctx, h.Server, h.shutdownTimeout, h.Logger, "http", h.Name)
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

// Hijack delegates to the underlying ResponseWriter if it supports hijacking,
// so that protocol upgrades (e.g. WebSocket) work through this middleware.
func (w *statusCapturingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("ResponseWriter does not implement http.Hijacker")
}

// Flush delegates to the underlying ResponseWriter if it supports flushing,
// so that streaming responses (e.g. SSE) work through this middleware.
func (w *statusCapturingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
		zap.String("remote_addr", r.RemoteAddr),
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
		h.config.UserLogger.Error("Error building evaluation context", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	val, diags := h.actionExpr.Value(evalCtx)
	if diags.HasErrors() {
		h.config.UserLogger.Error("Error executing action", h.config.ActionError(diags))
		http.Error(w, diags.Error(), http.StatusInternalServerError)
		return
	}

	writeHTTPResponse(h.config.Logger, w, val)
}

func init() {
	// getHttpActionEvalContext below builds this, and so does each auth mode
	// that evaluates an action (servers/auth), so a handler and an `auth`
	// action see the same shape.
	cfg.RegisterContextSchema("http-request", cfg.ContextSchema{
		Summary: "Evaluated once per HTTP request.",
		Fields: []cfg.ContextField{
			{
				Name: "request", Type: cfg.CtxTypeObject,
				Summary: "The inbound request.",
				Doc: "Carries `method`, `url`, `host`, `remote_addr`, `proto`, the basic-auth " +
					"`user`/`password`/`password_set`, the route's `path` parameters, and `form`. " +
					"Read headers, cookies, and the body through the request functions — see " +
					"doc/server-http.md.",
			},
		},
	})
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
