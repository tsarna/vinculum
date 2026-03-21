package mcp

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tsarna/vinculum/functions"
	"go.uber.org/zap"
	"github.com/zclconf/go-cty/cty/function"
)

// ServerConfig holds all parsed configuration needed to create an MCP Server.
type ServerConfig struct {
	Name          string
	DefRange      hcl.Range
	Listen        string
	Path          string
	ServerName    string
	ServerVersion string
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
	sdkServer     *sdkmcp.Server
	httpHandler   http.Handler
	logger        *zap.Logger
	parentEvalCtx *hcl.EvalContext
	mcpFuncs      map[string]function.Function
}

// New creates a new MCP server from the given configuration.
func New(cfg ServerConfig) (*Server, error) {
	serverName := cfg.ServerName
	if serverName == "" {
		serverName = cfg.Name
	}
	serverVersion := cfg.ServerVersion
	if serverVersion == "" {
		serverVersion = "0.0.0"
	}

	path := cfg.Path
	if path == "" {
		path = "/"
	}

	sdkSrv := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	s := &Server{
		name:          cfg.Name,
		listen:        cfg.Listen,
		path:          path,
		sdkServer:     sdkSrv,
		logger:        cfg.Logger,
		parentEvalCtx: cfg.ParentEvalCtx,
		mcpFuncs:      functions.GetMcpFunctions(),
	}

	registerResources(s, cfg.Resources)

	if err := registerTools(s, cfg.Tools); err != nil {
		return nil, fmt.Errorf("registering tools: %w", err)
	}

	registerPrompts(s, cfg.Prompts)

	// Build the HTTP handler: StreamableHTTPHandler wrapped at the configured path
	streamHandler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server {
		return s.sdkServer
	}, nil)

	if path == "/" {
		s.httpHandler = streamHandler
	} else {
		mux := http.NewServeMux()
		mux.Handle(path, streamHandler)
		s.httpHandler = mux
	}

	return s, nil
}

// Start begins listening for connections on the configured address.
func (s *Server) Start() error {
	if s.listen == "" {
		// Mounted under an HTTP server — nothing to start independently
		return nil
	}
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
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
