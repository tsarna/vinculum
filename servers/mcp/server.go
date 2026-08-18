package mcp

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	cfg "github.com/tsarna/vinculum/config"
	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// ServerConfig holds all parsed configuration needed to create an MCP Server.
type ServerConfig struct {
	Name          string
	DefRange      hcl.Range
	ServerName    string
	ServerVersion string
	OtlpClient    cfg.OtlpClient
	// TracerProvider, when set, overrides the tracer provider used for
	// MCP-method spans. When nil, it is derived from OtlpClient (else the
	// global provider). Primarily a test injection point.
	TracerProvider oteltrace.TracerProvider
	MeterProvider  otelmetric.MeterProvider
	ParentEvalCtx  *hcl.EvalContext
	Logger         *zap.Logger
	Resources      []ResourceDef
	Tools          []ToolDef
	Prompts        []PromptDef
}

// Server is a vinculum MCP server. It wraps the MCP SDK server and handles
// action-based (synchronous) resource, tool, and prompt requests by evaluating
// HCL expressions with per-request eval contexts.
type Server struct {
	name          string
	sdkServer     *sdkmcp.Server
	httpHandler   http.Handler
	logger        *zap.Logger
	parentEvalCtx *hcl.EvalContext
	tracer        oteltrace.Tracer
	metrics       *mcpMetrics
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

	sdkSrv := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	// Resolve the tracer provider for MCP-method spans: explicit override,
	// else the configured OTLP client, else the global provider (which is the
	// no-op provider when tracing is not configured).
	tp := scfg.TracerProvider
	if tp == nil && scfg.OtlpClient != nil {
		tp = scfg.OtlpClient.GetTracerProvider()
	}
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	s := &Server{
		name:          scfg.Name,
		sdkServer:     sdkSrv,
		logger:        scfg.Logger,
		parentEvalCtx: scfg.ParentEvalCtx,
		tracer:        tp.Tracer(instrumentationScope),
		metrics:       newMCPMetrics(scfg.MeterProvider),
	}

	// Instrument every inbound MCP request/notification with a span and the
	// operation-duration metric, following the OTel MCP semantic conventions.
	sdkSrv.AddReceivingMiddleware(s.instrumentMiddleware())

	registerResources(s, scfg.Resources)

	if err := registerTools(s, scfg.Tools); err != nil {
		return nil, fmt.Errorf("registering tools: %w", err)
	}

	registerPrompts(s, scfg.Prompts)

	s.httpHandler = sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server {
		return s.sdkServer
	}, nil)

	return s, nil
}

// HTTPHandler returns the http.Handler for this server, for the `server "http"`
// route it is mounted on.
//
// The handler it returns is deliberately **unauthenticated**. The block that
// mounts a handler is the one that authenticates it.
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
