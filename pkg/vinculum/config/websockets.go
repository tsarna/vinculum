package config

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/vinculum-bus/transform"
	"github.com/tsarna/vinculum/pkg/vinculum/websockets/server"
)

type WebsocketServer struct {
	BaseServer
	Listener *server.Listener
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
	Transforms           hcl.Expression `hcl:"message_transforms,optional"`
	DefRange             hcl.Range      `hcl:",def_range"`
}

func ProcessWebsocketsServerBlock(config *Config, block *hcl.Block, remainingBody hcl.Body) (Listener, hcl.Diagnostics) {
	serverDef := WebsocketsServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.evalCtx, &serverDef)
	if diags.HasErrors() {
		return nil, diags
	}

	websocketsServers, ok := config.Servers["websocket"]
	if !ok {
		websocketsServers = make(map[string]Listener)
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

	bus, diags := GetEventBusFromExpression(config, serverDef.Bus)
	if diags.HasErrors() {
		return nil, diags
	}

	listenerBuilder := server.NewServer().WithEventBus(bus).WithLogger(config.Logger)

	/*TODO
	if IsExpressionProvided(serverDef.PingInterval) {
		pingInterval, diags := config.ParseDuration(serverDef.PingInterval)
		if diags.HasErrors() {
			return nil, diags
		}
		listenerBuilder = listenerBuilder.WithPingInterval(pingInterval)
	}
	*/

	/*TODO
	if IsExpressionProvided(serverDef.WriteTimeout) {
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
	if IsExpressionProvided(serverDef.Transforms) {
		transforms, diags = config.GetMessageTransforms(serverDef.Transforms)
		if diags.HasErrors() {
			return nil, diags
		}
	}

	transforms = append(transforms, cty2goTransform)
	listenerBuilder = listenerBuilder.WithMessageTransforms(transforms...)

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

	server := &WebsocketServer{
		BaseServer: BaseServer{
			Name:     block.Labels[1],
			DefRange: serverDef.DefRange,
		},
		Listener: listener,
	}

	return server, nil
}
