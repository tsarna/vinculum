package vws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	"github.com/tsarna/vinculum-bus/transform"
	vwspkg "github.com/tsarna/vinculum-vws"
	"github.com/tsarna/vinculum-vws/server"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

type VinculumWebsocketServer struct {
	cfg.BaseServer
	Listener *server.Listener

	// shutdownTimeout bounds how long Drain waits for connections to close.
	shutdownTimeout time.Duration
}

func (s *VinculumWebsocketServer) GetHandler() http.Handler {
	return s.Listener
}

// Drain closes the open WebSocket connections and waits for them to go away,
// up to the configured shutdown_timeout.
//
// This server never owns a listener — it is always mounted — but it does own
// connections, and http.Server.Shutdown deliberately leaves hijacked
// connections alone. Without this they would simply be severed at exit, with
// no close frame and no chance for a client to distinguish a clean shutdown
// from a crash.
func (s *VinculumWebsocketServer) Drain(ctx context.Context) error {
	if s.shutdownTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.shutdownTimeout)
		defer cancel()
	}
	return s.Listener.Shutdown(ctx)
}

type VinculumWebsocketsServerDefinition struct {
	Bus                  hcl.Expression               `hcl:"bus"`
	Baggage              *hclutil.BaggageFilterConfig `hcl:"baggage,block"`
	QueueSize            *int                         `hcl:"queue_size,optional"`
	PingInterval         hcl.Expression               `hcl:"ping_interval,optional"`
	WriteTimeout         hcl.Expression               `hcl:"write_timeout,optional"`
	AllowSend            hcl.Expression               `hcl:"allow_send,optional"`
	InitialSubscriptions []string                     `hcl:"initial_subscriptions,optional"`
	OutboundTransforms   hcl.Expression               `hcl:"outbound_transforms,optional"`
	InboundTransforms    hcl.Expression               `hcl:"inbound_transforms,optional"`
	Metrics              hcl.Expression               `hcl:"metrics,optional"`
	ShutdownTimeout      hcl.Expression               `hcl:"shutdown_timeout,optional"`
	DefRange             hcl.Range                    `hcl:",def_range"`
}

func init() {
	cfg.RegisterServerType("vws", ProcessVinculumWebsocketsServerBlock, cfg.WithSchema(vwsServerSchema))
}

var vwsServerSchema = cfg.TypeSchema{
	Sample:  &VinculumWebsocketsServerDefinition{},
	Summary: "A WebSocket server speaking the Vinculum (VWS) protocol.",
	DocPage: "server-vws.md#server-vws",
	Doc: `Clients subscribe to topics over a WebSocket and receive matching bus messages,
and — when ` + "`allow_send`" + ` permits — publish back onto the bus. Mount it on a route
of a ` + "`server \"http\"`" + ` block with ` + "`handler = server.<name>`" + `.`,
	Attrs: map[string]cfg.AttrMeta{
		"bus": {
			Summary: "Bus that connected clients subscribe to and publish into.",
			Hint:    cfg.HintBusRef,
		},
		"queue_size": {
			Summary: "Per-connection outbound queue depth.",
			Doc:     "How far one slow client may fall behind before its messages start being dropped.",
			Default: "256",
		},
		"ping_interval": {
			Summary: "How often to send WebSocket pings, to detect dead connections.",
			Hint:    cfg.HintDuration,
		},
		"write_timeout": {
			Summary: "How long to wait writing to a client before closing the connection.",
			Hint:    cfg.HintDuration,
		},
		"allow_send": {
			Summary: "Whether clients may publish onto the bus.",
			Doc: "`true` allows any topic, a string allows topics matching that MQTT " +
				"pattern, and an expression is evaluated per inbound message with " +
				"`ctx.topic` and `ctx.msg` in scope — returning false drops the message " +
				"silently, a string rejects it with that error.",
			Hint:    cfg.HintPredicateExpression,
			Context: "message",
			Default: "false",
		},
		"initial_subscriptions": {
			Summary: "Topic patterns every new client is subscribed to on connect.",
			Hint:    cfg.HintTopicPattern,
		},
		"outbound_transforms": {
			Summary: "Transform pipeline applied to messages going from the bus to clients.",
			Hint:    cfg.HintTransformPipeline,
		},
		"inbound_transforms": {
			Summary: "Transform pipeline applied to messages from clients before publishing.",
			Hint:    cfg.HintTransformPipeline,
		},
		"metrics": cfg.MetricsAttr,
		"shutdown_timeout": cfg.ShutdownTimeoutAttr.WithDoc(
			"On shutdown, connected clients are closed before the buses and clients they " +
				"depend on are stopped, and this bounds the wait for them to go away. " +
				"Applies whether or not the hosting `server \"http\"` block sets its own — " +
				"an upgraded WebSocket is invisible to that block's drain. `0` waits indefinitely."),
	},
}

func ProcessVinculumWebsocketsServerBlock(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Listener, hcl.Diagnostics) {
	serverDef := VinculumWebsocketsServerDefinition{}
	diags := cfg.DecodeBody(remainingBody, config.EvalCtx(), &serverDef)
	if diags.HasErrors() {
		return nil, diags
	}
	serverDef.DefRange = block.DefRange

	vinculumWebsocketsServers, ok := config.Servers["vws"]
	if !ok {
		vinculumWebsocketsServers = make(map[string]cfg.Listener)
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

	if baggageDiags := serverDef.Baggage.Validate(); baggageDiags.HasErrors() {
		return nil, baggageDiags
	}

	bus, diags := cfg.GetEventBusFromExpression(config, serverDef.Bus)
	if diags.HasErrors() {
		return nil, diags
	}

	// Connected clients are untrusted, and each inbound frame carries its own
	// headers: the connection extracts trace context from them per message, so
	// baggage a peer writes reaches ctx.baggage in every handler the message
	// touches and is re-propagated on outbound calls made from there.
	//
	// The filter is installed unconditionally — a nil Baggage is the secure
	// default, stripping everything — so omitting the block is safe rather than
	// merely undeclared. That is what the rest of the language promises, and
	// this server was the one inbound surface not keeping it.
	bus = cfg.NewBaggageFilterEventBus(serverDef.Baggage, bus, config.Logger)

	listenerBuilder := server.NewListener().WithEventBus(bus).WithLogger(config.Logger).WithServerName(block.Labels[1])

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

	if cfg.IsExpressionProvided(serverDef.AllowSend) {
		val, ok := cfg.IsConstantExpression(serverDef.AllowSend)
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
			listenerBuilder = listenerBuilder.WithEventAuth(MakeAllowSend(config, serverDef.AllowSend))
		}
	}

	mp, metricsDiags := cfg.ResolveMeterProvider(config, serverDef.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}
	if mp != nil {
		listenerBuilder = listenerBuilder.WithMeterProvider(mp)
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

	shutdownTimeout, diags := config.ParseDurationOrDefault(serverDef.ShutdownTimeout, cfg.DefaultShutdownTimeout)
	if diags.HasErrors() {
		return nil, diags
	}

	srv := &VinculumWebsocketServer{
		BaseServer: cfg.BaseServer{
			Name:     block.Labels[1],
			DefRange: serverDef.DefRange,
		},
		Listener:        listener,
		shutdownTimeout: shutdownTimeout,
	}

	// Registered even though this server is always mounted: what it drains is
	// its connections, which the hosting http.Server will not close.
	config.Drainables = append(config.Drainables, srv)

	return srv, nil
}

func MakeAllowSend(config *cfg.Config, expr hcl.Expression) server.EventAuthFunc {
	return func(ctx context.Context, msg *vwspkg.WireMessage) (*vwspkg.WireMessage, error) {
		ctyMessage, err := go2cty2go.AnyToCty(msg.Data)
		if err != nil {
			return nil, err
		}

		evalCtx, err := hclutil.NewEvalContext(ctx).
			WithStringAttribute("topic", msg.Topic).
			WithAttribute("msg", ctyMessage).
			BuildEvalContext(config.EvalCtx())
		if err != nil {
			return nil, err
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
