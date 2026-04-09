package mcp

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	cfg "github.com/tsarna/vinculum/config"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
)

// ServerConfig holds all parsed configuration needed to create an MCP Server.
type ServerConfig struct {
	Name          string
	DefRange      hcl.Range
	Listen        string
	Path          string
	ServerName    string
	ServerVersion string
	TLSConfig     *tls.Config
	Auth          *cfg.AuthConfig
	OtlpClient    cfg.OtlpClient
	MeterProvider otelmetric.MeterProvider
	ParentEvalCtx *hcl.EvalContext
	Logger        *zap.Logger
	Resources     []ResourceDef
	Tools         []ToolDef
	Prompts       []PromptDef
}

// Server is a vinculum MCP server. It wraps the MCP SDK server and handles
// action-based (synchronous) resource, tool, and prompt requests by evaluating
// HCL expressions with per-request eval contexts.
type Server struct {
	name          string
	listen        string
	path          string
	tlsConfig     *tls.Config
	sdkServer     *sdkmcp.Server
	httpHandler   http.Handler
	logger        *zap.Logger
	parentEvalCtx *hcl.EvalContext
}

// New creates a new MCP server from the given configuration.
func New(scfg ServerConfig) (*Server, error) {
	serverName := scfg.ServerName
	if serverName == "" {
		serverName = scfg.Name
	}
	serverVersion := scfg.ServerVersion
	if serverVersion == "" {
		serverVersion = "0.0.0"
	}

	path := scfg.Path
	if path == "" {
		path = "/"
	}

	sdkSrv := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	s := &Server{
		name:          scfg.Name,
		listen:        scfg.Listen,
		path:          path,
		tlsConfig:     scfg.TLSConfig,
		sdkServer:     sdkSrv,
		logger:        scfg.Logger,
		parentEvalCtx: scfg.ParentEvalCtx,
	}

	registerResources(s, scfg.Resources)

	if err := registerTools(s, scfg.Tools); err != nil {
		return nil, fmt.Errorf("registering tools: %w", err)
	}

	registerPrompts(s, scfg.Prompts)

	// Build the HTTP handler: StreamableHTTPHandler wrapped at the configured path.
	// For standalone servers with auth configured, wrap with auth middleware and
	// (for OIDC) serve the OAuth2 discovery document.
	streamHandler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server {
		return s.sdkServer
	}, nil)

	if err := s.buildHTTPHandler(path, streamHandler, scfg.Auth, scfg.ParentEvalCtx); err != nil {
		return nil, err
	}

	// For standalone servers, wrap the assembled handler with otelhttp so that
	// incoming W3C trace context is extracted and a server span is created.
	// Mounted servers are left unwrapped — the parent server "http" handles it.
	if scfg.Listen != "" {
		var otelOpts []otelhttp.Option
		if scfg.OtlpClient != nil {
			if tp := scfg.OtlpClient.GetTracerProvider(); tp != nil {
				otelOpts = append(otelOpts, otelhttp.WithTracerProvider(tp))
			}
		}
		if scfg.MeterProvider != nil {
			otelOpts = append(otelOpts, otelhttp.WithMeterProvider(scfg.MeterProvider))
		}
		otelOpts = append(otelOpts,
			otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator(
				propagation.TraceContext{},
				propagation.Baggage{},
			)),
			otelhttp.WithServerName(scfg.Name),
		)
		s.httpHandler = otelhttp.NewHandler(s.httpHandler, "",
			append(otelOpts, otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return r.Method + " " + r.URL.Path
			}))...,
		)
	}

	return s, nil
}

// buildHTTPHandler assembles s.httpHandler from the stream handler, optional auth
// middleware, and (for standalone OIDC servers) the OAuth2 discovery endpoint.
func (s *Server) buildHTTPHandler(path string, streamHandler http.Handler, authCfg *cfg.AuthConfig, evalCtx *hcl.EvalContext) error {
	// If auth is configured, build an authenticator and wrap the stream handler.
	var protected http.Handler = streamHandler
	var oidcMeta *oidcMetadataHandler

	if authCfg != nil && authCfg.Mode != "none" {
		authenticator, meta, err := buildMCPAuthenticator(authCfg, s.name, evalCtx)
		if err != nil {
			return err
		}
		if authenticator != nil {
			protected = newMCPAuthMiddleware(authenticator, evalCtx, s.logger, streamHandler)
		}
		// For standalone OIDC servers that used discovery, expose the metadata document.
		if meta != nil && s.listen != "" {
			oidcMeta = &oidcMetadataHandler{meta: meta}
		}
	}

	if oidcMeta == nil && path == "/" {
		s.httpHandler = protected
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(path, protected)
	if oidcMeta != nil {
		mux.Handle("GET /.well-known/oauth-authorization-server", oidcMeta)
	}
	s.httpHandler = mux
	return nil
}

// Start begins listening for connections on the configured address.
func (s *Server) Start() error {
	if s.listen == "" {
		// Mounted under an HTTP server — nothing to start independently
		return nil
	}

	// s.httpHandler was already wrapped with otelhttp in New() for standalone mode.
	httpSrv := &http.Server{
		Addr:    s.listen,
		Handler: s.httpHandler,
	}
	go func() {
		s.logger.Info("Starting MCP server",
			zap.String("name", s.name),
			zap.String("addr", s.listen),
			zap.String("path", s.path),
		)
		var err error
		if s.tlsConfig != nil {
			httpSrv.TLSConfig = s.tlsConfig
			err = httpSrv.ListenAndServeTLS("", "")
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			s.logger.Error("MCP server stopped", zap.String("name", s.name), zap.Error(err))
		}
	}()
	return nil
}

// HTTPHandler returns the http.Handler for this server.
// Used when the MCP server is mounted under an HTTP server block.
func (s *Server) HTTPHandler() http.Handler {
	return s.httpHandler
}

// SDKServer returns the underlying MCP SDK server.
// Intended for use in tests.
func (s *Server) SDKServer() *sdkmcp.Server {
	return s.sdkServer
}

// GetName returns the server's config block name.
func (s *Server) GetName() string {
	return s.name
}

// GetDefRange returns the HCL source range of the server block definition.
func (s *Server) GetDefRange() hcl.Range {
	return hcl.Range{}
}
