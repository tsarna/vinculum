package websocketserver

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/vinculum-bus/transform"
	cfg "github.com/tsarna/vinculum/config"
)

type WebsocketServer struct {
	cfg.BaseServer
	Listener *Listener
}

func (s *WebsocketServer) GetHandler() http.Handler {
	return s.Listener
}

type WebsocketsServerDefinition struct {
	Bus                  hcl.Expression `hcl:"bus"`
	QueueSize            *int           `hcl:"queue_size,optional"`
	PingInterval         hcl.Expression `hcl:"ping_interval,optional"`
	WriteTimeout         hcl.Expression `hcl:"write_timeout,optional"`
	InitialSubscriptions []string       `hcl:"initial_subscriptions,optional"`
	OutboundTransforms   hcl.Expression `hcl:"outbound_transforms,optional"`
	InboundTransforms    hcl.Expression `hcl:"inbound_transforms,optional"`
	DefRange             hcl.Range      `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("websocket", ProcessWebsocketsServerBlock, cfg.WithSchema(websocketServerSchema))
}

var websocketServerSchema = cfg.TypeSchema{
	Sample:  &WebsocketsServerDefinition{},
	Summary: "A WebSocket server that pushes bus messages as raw frames.",
	DocPage: "server-websocket.md",
	Doc: `Bridges a bus to WebSocket clients using raw frames, with no subscribe protocol:
every connected client receives the messages the server is subscribed to, and any
frame a client sends is published to a fixed topic — ` + "`text`" + ` for text frames,
` + "`binary`" + ` for binary ones.

Use ` + "`server \"vws\"`" + ` instead when clients need to control their own subscriptions.
Mount this on a route of a ` + "`server \"http\"`" + ` block with ` + "`handler = server.<name>`" + `.`,
	Attrs: map[string]cfg.AttrMeta{
		"bus": {
			Summary: "Bus to bridge to connected clients.",
			Hint:    cfg.HintBusRef,
		},
		"queue_size": {
			Summary: "Per-connection outbound queue depth.",
			Doc:     "How far one slow client may fall behind before its messages start being dropped.",
			Default: "256",
		},
		"ping_interval": {
			Summary: "How often to send WebSocket pings, to detect dead connections.",
			Doc:     "A ping that goes unanswered within `write_timeout` means the peer is gone, and the connection is dropped along with its queue and subscriptions. Set `0` to disable pings, leaving a dead peer undetected until the OS gives up on the TCP connection.",
			Hint:    cfg.HintDuration,
			Default: "30s",
		},
		"write_timeout": {
			Summary: "How long to wait writing to a client before closing the connection.",
			Doc:     "Bounds each individual write and each ping, so a client that has stopped reading cannot hold the connection's writer indefinitely. Set `0` to wait forever.",
			Hint:    cfg.HintDuration,
			Default: "10s",
		},
		"initial_subscriptions": {
			Summary: "Topic patterns each new connection is subscribed to on connect.",
			Doc:     "Matching messages are forwarded to the client.",
			Hint:    cfg.HintTopicPattern,
		},
		"outbound_transforms": {
			Summary: "Transform pipeline applied to messages going from the bus to clients.",
			Hint:    cfg.HintTransformPipeline,
		},
		"inbound_transforms": {
			Summary: "Transform pipeline applied to frames from clients before publishing.",
			Hint:    cfg.HintTransformPipeline,
		},
	},
}

func ProcessWebsocketsServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	serverDef := WebsocketsServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &serverDef)
	if diags.HasErrors() {
		return nil, diags
	}
	serverDef.DefRange = block.DefRange

	websocketsServers, ok := config.Servers["websocket"]
	if !ok {
		websocketsServers = make(map[string]cfg.Listener)
		config.Servers["websocket"] = websocketsServers
	}

	existing, ok := websocketsServers[block.Labels[1]]
	if ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "WebSockets server already defined",
				Detail:   fmt.Sprintf("WebSockets server %s already defined at %s", block.Labels[1], existing.GetDefRange()),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	bus, diags := cfg.GetEventBusFromExpression(config, serverDef.Bus)
	if diags.HasErrors() {
		return nil, diags
	}

	listenerBuilder := NewServer().WithEventBus(bus).WithLogger(config.Logger)

	if cfg.IsExpressionProvided(serverDef.PingInterval) {
		pingInterval, diags := config.ParseDuration(serverDef.PingInterval)
		if diags.HasErrors() {
			return nil, diags
		}
		listenerBuilder = listenerBuilder.WithPingInterval(pingInterval)
	}

	if cfg.IsExpressionProvided(serverDef.WriteTimeout) {
		writeTimeout, diags := config.ParseDuration(serverDef.WriteTimeout)
		if diags.HasErrors() {
			return nil, diags
		}
		listenerBuilder = listenerBuilder.WithWriteTimeout(writeTimeout)
	}

	if serverDef.QueueSize != nil {
		listenerBuilder = listenerBuilder.WithQueueSize(*serverDef.QueueSize)
	}

	if serverDef.InitialSubscriptions != nil {
		listenerBuilder = listenerBuilder.WithInitialSubscriptions(serverDef.InitialSubscriptions...)
	}

	transforms := make([]transform.MessageTransformFunc, 0)
	if cfg.IsExpressionProvided(serverDef.OutboundTransforms) {
		transforms, diags = config.GetMessageTransforms(serverDef.OutboundTransforms)
		if diags.HasErrors() {
			return nil, diags
		}
	}

	transforms = append(transforms, cfg.Cty2GoTransform)
	listenerBuilder = listenerBuilder.WithOutboundTransforms(transforms...)

	inboundTransforms := make([]transform.MessageTransformFunc, 0)
	if cfg.IsExpressionProvided(serverDef.InboundTransforms) {
		inboundTransforms, diags = config.GetMessageTransforms(serverDef.InboundTransforms)
		if diags.HasErrors() {
			return nil, diags
		}
	}

	if len(inboundTransforms) > 0 {
		listenerBuilder = listenerBuilder.WithInboundTransforms(inboundTransforms...)
	}

	listener, err := listenerBuilder.Build()
	if err != nil {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to create WebSockets server",
				Detail:   err.Error(),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	srv := &WebsocketServer{
		BaseServer: cfg.BaseServer{
			Name:     block.Labels[1],
			DefRange: serverDef.DefRange,
		},
		Listener: listener,
	}

	return srv, nil
}
