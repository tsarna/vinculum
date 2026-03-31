package httpserver

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	serverauth "github.com/tsarna/vinculum/servers/auth"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

type HttpServer struct {
	cfg.BaseServer
	Logger    *zap.Logger
	Server    *http.Server
	TLSConfig *tls.Config
}

type HttpServerDefinition struct {
	Listen      string                  `hcl:"listen"`
	TLS         *cfg.TLSConfig          `hcl:"tls,block"`
	Auth        *cfg.AuthConfig         `hcl:"auth,block"`
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

	// Validate server-level auth if present.
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

	existing, ok := httpServers[block.Labels[1]]
	if ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Http server already defined",
				Detail:   fmt.Sprintf("Http server %s already defined at %s", block.Labels[1], existing.GetDefRange()),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	serverName := block.Labels[1]
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
		handler := http.Handler(NewLoggingMiddleware(config.Logger,
			http.StripPrefix(path, http.FileServer(http.Dir(dir)))))
		handler, diags = wrapWithAuth(handler, effectiveAuth, serverName, config, &file.DefRange)
		if diags.HasErrors() {
			return nil, diags
		}
		mux.Handle(path, handler)
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
			inner = NewLoggingMiddleware(config.Logger, &httpAction{config: config, actionExpr: handlerDef.Action})
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
			inner = NewLoggingMiddleware(config.Logger, handlerServer.GetHandler())
		}

		effectiveAuth := resolveAuth(serverDef.Auth, handlerDef.Auth)
		wrapped, authDiags := wrapWithAuth(inner, effectiveAuth, serverName, config, &handlerDef.DefRange)
		if authDiags.HasErrors() {
			return nil, authDiags
		}
		mux.Handle(handlerDef.Route, wrapped)
	}

	server := &HttpServer{
		Logger: config.Logger,
		BaseServer: cfg.BaseServer{
			Name:     serverName,
			DefRange: serverDef.DefRange,
		},
		Server: &http.Server{
			Addr:    serverDef.Listen,
			Handler: mux,
		},
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

// resolveAuth determines the effective auth config for a route, implementing
// the scoping rules from the spec:
//   - block-level auth (any mode, including "none") takes precedence over server auth
//   - if no block-level auth, use server-level auth (may be nil = unauthenticated)
func resolveAuth(serverAuth, blockAuth *cfg.AuthConfig) *cfg.AuthConfig {
	if blockAuth != nil {
		return blockAuth
	}
	return serverAuth
}

// wrapWithAuth wraps handler with auth middleware if effectiveAuth is non-nil
// and not "none". Returns the original handler unchanged otherwise.
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

type loggingMiddleware struct {
	next   http.Handler
	logger *zap.Logger
}

func NewLoggingMiddleware(logger *zap.Logger, next http.Handler) http.Handler {
	return &loggingMiddleware{
		logger: logger,
		next:   next,
	}
}

func (l *loggingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO: Log status code
	l.logger.Info("Request", zap.String("method", r.Method), zap.String("url", r.URL.String()))
	l.next.ServeHTTP(w, r)
}

type httpAction struct {
	config     *cfg.Config
	actionExpr hcl.Expression
}

func (h *httpAction) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	evalCtx, err := getHttpActionEvalContext(h.config, r)
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

func getHttpActionEvalContext(config *cfg.Config, r *http.Request) (*hcl.EvalContext, error) {
	builder := hclutil.NewEvalContext(r.Context()).
		WithAttribute("request", types.BuildHTTPRequestObject(r))

	return builder.BuildEvalContext(config.EvalCtx())
}

func writeHTTPResponse(logger *zap.Logger, w http.ResponseWriter, val cty.Value) {
	// Explicit httpresponse value (from http_response, http_redirect, http_error, etc.)
	if resp, ok := types.GetHTTPResponseFromValue(val); ok {
		if resp.IsError {
			logger.Warn("HTTP action returned http_error",
				zap.Int("status", resp.Status),
				zap.String("body", string(resp.Body)))
		}
		writeResponseFromWrapper(w, resp)
		return
	}

	// null → 204 No Content
	if val.IsNull() {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// All other types: coerce to body bytes
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
