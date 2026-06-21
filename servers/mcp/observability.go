package mcp

import (
	"context"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationScope is the OTel instrumentation scope used for the MCP
// server's tracer and meter.
const instrumentationScope = "github.com/tsarna/vinculum/servers/mcp"

// Attribute keys from the OpenTelemetry GenAI/MCP semantic conventions
// (model/mcp/*.yaml in open-telemetry/semantic-conventions-genai). These are
// still marked "development" upstream and are not yet present in the Go
// `semconv` package, so they are defined here as string constants.
const (
	attrMCPMethodName      = "mcp.method.name"
	attrMCPSessionID       = "mcp.session.id"
	attrMCPResourceURI     = "mcp.resource.uri"
	attrGenAIToolName      = "gen_ai.tool.name"
	attrGenAIPromptName    = "gen_ai.prompt.name"
	attrGenAIOperationName = "gen_ai.operation.name"
	attrErrorType          = "error.type"
	attrNetworkTransport   = "network.transport"
	attrNetworkProtocol    = "network.protocol.name"
)

// error.type values. errorTypeTool is the convention-mandated value when a tool
// call returns a CallToolResult with IsError=true; errorTypeOther is the OTel
// low-cardinality catch-all for protocol errors whose JSON-RPC code is not
// reliably available at this layer.
const (
	errorTypeTool  = "tool_error"
	errorTypeOther = "_OTHER"
)

// mcpMetrics holds the OTel instruments emitted by the MCP server.
type mcpMetrics struct {
	// opDuration records mcp.server.operation.duration: the time to handle an
	// inbound MCP request or notification, in seconds.
	opDuration metric.Float64Histogram
}

// newMCPMetrics builds the MCP server instruments from the given MeterProvider.
// A nil provider yields no-op instruments so the server runs without metrics
// configured.
func newMCPMetrics(mp metric.MeterProvider) *mcpMetrics {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	meter := mp.Meter(instrumentationScope)
	opDuration, _ := meter.Float64Histogram(
		"mcp.server.operation.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound MCP requests/notifications handled by the server."),
	)
	return &mcpMetrics{opDuration: opDuration}
}

// instrumentMiddleware returns SDK receiving middleware that records a
// `mcp.server` span and the `mcp.server.operation.duration` metric for every
// inbound MCP request or notification, following the OpenTelemetry MCP
// semantic conventions. It is transport-independent, so it covers both
// standalone and HTTP-mounted servers identically.
func (s *Server) instrumentMiddleware() sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
			// Attributes common to both the span and the metric. Kept
			// low-cardinality: method name, transport, and the tool/prompt
			// identity. The resource URI and session ID are span-only.
			attrs := []attribute.KeyValue{
				attribute.String(attrMCPMethodName, method),
				attribute.String(attrNetworkTransport, "tcp"),
				attribute.String(attrNetworkProtocol, "http"),
			}

			// target becomes part of the span name when it is low-cardinality
			// (tool or prompt name). The resource URI is deliberately excluded
			// to avoid high-cardinality span names.
			target := ""
			switch p := req.GetParams().(type) {
			case *sdkmcp.CallToolParamsRaw:
				// CallToolParamsRaw is the param type seen by receiving
				// middleware (arguments are still raw JSON at this layer).
				attrs = append(attrs,
					attribute.String(attrGenAIToolName, p.Name),
					attribute.String(attrGenAIOperationName, "execute_tool"),
				)
				target = p.Name
			case *sdkmcp.CallToolParams:
				attrs = append(attrs,
					attribute.String(attrGenAIToolName, p.Name),
					attribute.String(attrGenAIOperationName, "execute_tool"),
				)
				target = p.Name
			case *sdkmcp.GetPromptParams:
				attrs = append(attrs, attribute.String(attrGenAIPromptName, p.Name))
				target = p.Name
			}

			// Metric attributes are a snapshot of the shared (low-cardinality)
			// set, taken before the span-only attributes are appended.
			metricAttrs := make([]attribute.KeyValue, len(attrs))
			copy(metricAttrs, attrs)

			// Span-only, higher-cardinality attributes.
			spanAttrs := attrs
			if sess := req.GetSession(); sess != nil {
				if sid := sess.ID(); sid != "" {
					spanAttrs = append(spanAttrs, attribute.String(attrMCPSessionID, sid))
				}
			}
			if rp, ok := req.GetParams().(*sdkmcp.ReadResourceParams); ok {
				spanAttrs = append(spanAttrs, attribute.String(attrMCPResourceURI, rp.URI))
			}

			spanName := method
			if target != "" {
				spanName = method + " " + target
			}

			ctx, span := s.tracer.Start(ctx, spanName,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(spanAttrs...),
			)

			start := time.Now()
			result, err := next(ctx, method, req)
			elapsed := time.Since(start).Seconds()

			if errType := classifyError(result, err); errType != "" {
				et := attribute.String(attrErrorType, errType)
				span.SetAttributes(et)
				metricAttrs = append(metricAttrs, et)
				desc := errType
				if err != nil {
					desc = err.Error()
				}
				span.SetStatus(codes.Error, desc)
			}

			s.metrics.opDuration.Record(ctx, elapsed, metric.WithAttributes(metricAttrs...))
			span.End()

			return result, err
		}
	}
}

// classifyError returns the error.type value for a completed MCP operation, or
// "" when it succeeded. A protocol-level Go error maps to the low-cardinality
// catch-all; a tool result flagged IsError maps to "tool_error" per the spec.
func classifyError(result sdkmcp.Result, err error) string {
	if err != nil {
		return errorTypeOther
	}
	if ctr, ok := result.(*sdkmcp.CallToolResult); ok && ctr.IsError {
		return errorTypeTool
	}
	return ""
}
