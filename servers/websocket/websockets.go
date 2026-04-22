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
	cfg.RegisterServerType("websocket", ProcessWebsocketsServerBlock)
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

	/*TODO
	if cfg.IsExpressionProvided(serverDef.PingInterval) {
		pingInterval, diags := config.ParseDuration(serverDef.PingInterval)
		if diags.HasErrors() {
			return nil, diags
		}
		listenerBuilder = listenerBuilder.WithPingInterval(pingInterval)
	}
	*/

	/*TODO
	if cfg.IsExpressionProvided(serverDef.WriteTimeout) {
		writeTimeout, diags := config.ParseDuration(serverDef.WriteTimeout)
		if diags.HasErrors() {
			return nil, diags
		}
		listenerBuilder = listenerBuilder.WithWriteTimeout(writeTimeout)
	}
	*/

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
