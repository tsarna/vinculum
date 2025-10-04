package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	"github.com/tsarna/vinculum-bus/transform"
	"github.com/tsarna/vinculum-vws"
	"github.com/tsarna/vinculum-vws/server"
	"github.com/zclconf/go-cty/cty"
)

type VinculumWebsocketServer struct {
	BaseServer
	Listener *server.Listener
}

func (s *VinculumWebsocketServer) GetHandler() http.Handler {
	return s.Listener
}

type VinculumWebsocketsServerDefinition struct {
	Bus                  hcl.Expression `hcl:"bus"`
	QueueSize            *int           `hcl:"queue_size,optional"`
	PingInterval         hcl.Expression `hcl:"ping_interval,optional"`
	WriteTimeout         hcl.Expression `hcl:"write_timeout,optional"`
	AllowSend            hcl.Expression `hcl:"allow_send,optional"`
	InitialSubscriptions []string       `hcl:"initial_subscriptions,optional"`
	OutboundTransforms   hcl.Expression `hcl:"outbound_transforms,optional"`
	InboundTransforms    hcl.Expression `hcl:"inbound_transforms,optional"`
	DefRange             hcl.Range      `hcl:",def_range"`
}

func ProcessVinculumWebsocketsServerBlock(config *Config, block *hcl.Block, remainingBody hcl.Body) (Listener, hcl.Diagnostics) {
	serverDef := VinculumWebsocketsServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.evalCtx, &serverDef)
	if diags.HasErrors() {
		return nil, diags
	}

	vinculumWebsocketsServers, ok := config.Servers["vws"]
	if !ok {
		vinculumWebsocketsServers = make(map[string]Listener)
		config.Servers["vws"] = vinculumWebsocketsServers
	}

	existing, ok := vinculumWebsocketsServers[block.Labels[1]]
	if ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Vinculum WebSockets server already defined",
				Detail:   fmt.Sprintf("Vinculum WebSockets server %s already defined at %s", block.Labels[1], existing.GetDefRange()),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	bus, diags := GetEventBusFromExpression(config, serverDef.Bus)
	if diags.HasErrors() {
		return nil, diags
	}

	listenerBuilder := server.NewListener().WithEventBus(bus).WithLogger(config.Logger)

	if IsExpressionProvided(serverDef.PingInterval) {
		pingInterval, diags := config.ParseDuration(serverDef.PingInterval)
		if diags.HasErrors() {
			return nil, diags
		}
		listenerBuilder = listenerBuilder.WithPingInterval(pingInterval)
	}

	if IsExpressionProvided(serverDef.WriteTimeout) {
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
	if IsExpressionProvided(serverDef.OutboundTransforms) {
		transforms, diags = config.GetMessageTransforms(serverDef.OutboundTransforms)
		if diags.HasErrors() {
			return nil, diags
		}
	}

	transforms = append(transforms, cty2goTransform)
	listenerBuilder = listenerBuilder.WithOutboundTransforms(transforms...)

	inboundTransforms := make([]transform.MessageTransformFunc, 0)
	if IsExpressionProvided(serverDef.InboundTransforms) {
		inboundTransforms, diags = config.GetMessageTransforms(serverDef.InboundTransforms)
		if diags.HasErrors() {
			return nil, diags
		}
	}

	if len(inboundTransforms) > 0 {
		listenerBuilder = listenerBuilder.WithInboundTransforms(inboundTransforms...)
	}

	if IsExpressionProvided(serverDef.AllowSend) {
		val, ok := IsConstantExpression(serverDef.AllowSend)
		if ok {
			if val.Type() == cty.Bool && val.True() {
				listenerBuilder = listenerBuilder.WithEventAuth(server.AllowAllEvents)
			} else if val.Type() == cty.String {
				listenerBuilder = listenerBuilder.WithEventAuth(server.AllowTopicPattern(val.AsString()))
			} else {
				listenerBuilder = listenerBuilder.WithEventAuth(server.DenyAllEvents)
			}
		} else {
			// Dynamically evaluated expression
			listenerBuilder = listenerBuilder.WithEventAuth(config.MakeAllowSend(serverDef.AllowSend))
		}
	}

	listener, err := listenerBuilder.Build()
	if err != nil {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to create Vinculum WebSockets server",
				Detail:   err.Error(),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	server := &VinculumWebsocketServer{
		BaseServer: BaseServer{
			Name:     block.Labels[1],
			DefRange: serverDef.DefRange,
		},
		Listener: listener,
	}

	return server, nil
}

func (config *Config) MakeAllowSend(expr hcl.Expression) server.EventAuthFunc {
	return func(ctx context.Context, msg *vws.WireMessage) (*vws.WireMessage, error) {
		ctyMessage, err := go2cty2go.AnyToCty(msg.Data)
		if err != nil {
			return nil, err
		}

		evalCtxBuilder := NewContext(ctx).
			WithStringAttribute("topic", msg.Topic).
			WithAttribute("msg", ctyMessage)
		evalCtx, diags := evalCtxBuilder.BuildEvalContext(config.evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}

		result, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}

		if result.Type() == cty.Bool {
			if result.True() {
				return msg, nil
			} else {
				return nil, nil
			}
		} else if result.Type() == cty.String {
			return nil, errors.New(result.AsString())
		}

		return nil, errors.New("allow_send expression must return a boolean or string")
	}
}
