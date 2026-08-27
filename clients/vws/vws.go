package vws

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-vws/client"
	cfg "github.com/tsarna/vinculum/config"
)

// No cfg.WithReadiness() here, deliberately, and Ready() is commented out to
// match — see the note on it below.
//
// This client never connects. Build() has one caller, a switch arm in
// ProcessSubscriptionBlock that is unreachable because the branch of
// GetTargetFromExpression that would produce a BusClient is commented out;
// nothing registers the client as Startable, so Connect is not called either;
// and send(ctx, client.<name>, ...) is no way in, since this type implements no
// OnEvent and send rejects it as "expected Subscriber capsule, got client".
//
// A component that reports on a connection it does not have is worse than one
// that reports nothing. Registering this held every configuration that merely
// *declares* a `client "vws"` permanently unready with the reason "not
// started" — which reads as an unreachable peer rather than as a client that
// was never wired up, and would take such a deployment out of service on
// upgrade. The same reasoning already keeps `server "vws"`, `"websocket"`, and
// `"mcp"` out of the probes: always mounted, owning no listener, nothing of
// their own to report.
//
// See specs/VWS-CLIENT-GAP.md. Restore this in the commit that makes the client
// connect, not before.
func init() {
	cfg.RegisterClientType("vws", process, cfg.WithSchema(vwsClientSchema))
}

type VinculumWebsocketClient struct {
	cfg.BaseBusClient
	ClientBuilder *client.ClientBuilder

	mu sync.RWMutex
	// report tells the health subsystem the connection changed, so a drop is
	// visible at once rather than at the next probe. Set at registration, which
	// happens after this client is built and before anything starts.
	//
	// Nothing registers this client today (see init()), so this stays nil and
	// reportHealth is a no-op. The machinery is kept live rather than commented
	// out so that restoring readiness is a change to Ready() alone.
	report cfg.ReadyReporter
}

func (c *VinculumWebsocketClient) Build() (bus.Client, error) {
	if c.Subscriber != nil {
		c.ClientBuilder = c.ClientBuilder.WithSubscriber(c.Subscriber)
	} else {
		c.ClientBuilder = c.ClientBuilder.WithSubscriber(&bus.BaseSubscriber{})
	}

	busClient, err := c.ClientBuilder.Build()
	if err != nil {
		return nil, err
	}
	c.Client = busClient

	return busClient, nil
}

// Ensure interface compliance.
var (
	// _ cfg.Readyable = (*VinculumWebsocketClient)(nil)   // with Ready(), below
	_ cfg.ReadyNotifier = (*VinculumWebsocketClient)(nil)
	_ bus.ClientMonitor = (*healthMonitor)(nil)
)

// SetReadyReporter implements cfg.ReadyNotifier.
func (c *VinculumWebsocketClient) SetReadyReporter(report cfg.ReadyReporter) {
	c.mu.Lock()
	c.report = report
	c.mu.Unlock()
}

func (c *VinculumWebsocketClient) reportHealth(err error) {
	c.mu.RLock()
	report := c.report
	c.mu.RUnlock()

	if report != nil {
		report(err)
	}
}

// healthMonitor is the bus.ClientMonitor installed on every vws client: it
// reports connection changes to the health subsystem, then delegates to the
// reconnector when the block declared one.
//
// A composite is needed because WithMonitor holds a single monitor, and a
// `reconnect` block already claims it. Installing this unconditionally is what
// gives a client with no `reconnect` block a health signal at all — previously
// no monitor was installed and its state was invisible until something probed.
//
// Dormant today: this client contributes to no probe (see the note on init()),
// so no reporter is ever handed to it and reportHealth finds nil. It is kept
// wired so that restoring readiness is a change to Ready() alone.
//
// When that happens, note that a failed *initial* dial fires nothing here —
// Connect returns an error rather than notifying — so the first probe after a
// failed dial is what reports it.
type healthMonitor struct {
	client   *VinculumWebsocketClient
	delegate bus.ClientMonitor // nil when there is no reconnect block
}

func (m *healthMonitor) OnConnect(ctx context.Context, client bus.Client) {
	m.client.reportHealth(nil)
	if m.delegate != nil {
		m.delegate.OnConnect(ctx, client)
	}
}

func (m *healthMonitor) OnDisconnect(ctx context.Context, client bus.Client, err error) {
	reason := err
	if reason == nil {
		// A graceful disconnect is still a disconnect as far as serving goes.
		reason = errors.New("not connected")
	}
	m.client.reportHealth(reason)

	// Delegated after reporting, since this is what starts the reconnect loop.
	if m.delegate != nil {
		m.delegate.OnDisconnect(ctx, client, err)
	}
}

func (m *healthMonitor) OnSubscribe(ctx context.Context, client bus.Client, topic string) {
	if m.delegate != nil {
		m.delegate.OnSubscribe(ctx, client, topic)
	}
}

func (m *healthMonitor) OnUnsubscribe(ctx context.Context, client bus.Client, topic string) {
	if m.delegate != nil {
		m.delegate.OnUnsubscribe(ctx, client, topic)
	}
}

func (m *healthMonitor) OnUnsubscribeAll(ctx context.Context, client bus.Client) {
	if m.delegate != nil {
		m.delegate.OnUnsubscribeAll(ctx, client)
	}
}

// Ready implements cfg.Readyable: the WebSocket to the peer is up.
//
// A dropped connection is recoverable — a `reconnect` block installs a monitor
// that re-dials — so this is the readiness case rather than a fatal one: out of
// rotation while the bridge is down, back in when it reconnects.
//
// COMMENTED OUT until the client actually connects. See the note on init()
// above. Restoring it is a two-line change — uncomment this and the
// cfg.Readyable assertion, and put cfg.WithReadiness() back on the
// RegisterClientType call — because the rest of the reporting machinery
// (SetReadyReporter, reportHealth, healthMonitor) is left in place and
// compiling. It is inert only because nothing registers this client as a
// contributor, so no reporter is ever handed to it and reportHealth finds nil.
//
// Defining Ready() is precisely what makes applyReadiness disagree: it errors
// when a type's registration and its Readyable implementation differ in either
// direction, so this cannot be half-disabled.
//
// func (c *VinculumWebsocketClient) Ready(context.Context) error {
// 	// c.Client is set by Build, which runs during startup, before the process
// 	// gate opens and lets any probe reach a contributor.
// 	vwsClient, ok := c.Client.(*client.Client)
// 	if !ok || vwsClient == nil {
// 		return errors.New("not started")
// 	}
// 	if !vwsClient.IsConnected() {
// 		return errors.New("not connected")
// 	}
// 	return nil
// }

var vwsClientSchema = cfg.TypeSchema{
	Sample:  &VinculumWebsocketsClientDefinition{},
	Summary: "A client connection to a Vinculum (VWS) WebSocket server.",
	DocPage: "server-vws.md#client-vws",
	Doc: `Connects outbound to a ` + "`server \"vws\"`" + ` endpoint and joins its bus, so two
Vinculum instances can be bridged over a WebSocket. Available in expressions as
` + "`client.<name>`" + `.`,
	Attrs: map[string]cfg.AttrMeta{
		"url": {
			Summary: "WebSocket URL of the VWS server.",
			Doc:     "For example `\"wss://events.example.com/ws\"`.",
			Hint:    cfg.HintURL,
		},
		"dial_timeout": {
			Summary: "Deadline for establishing the connection.",
			Hint:    cfg.HintDuration,
			Default: "30s",
		},
		"write_queue_size": {
			Summary: "Outbound message queue depth.",
			Default: "100",
		},
		"auth": {
			Summary: "Expression producing credentials for the connection.",
			Doc:     "Evaluated when connecting, so a token can be refreshed on each reconnect.",
			Hint:    cfg.HintActionExpression,
			Context: "connection",
		},
		"headers": {
			Summary: "Extra headers sent with the WebSocket handshake.",
		},
	},
}

type VinculumWebsocketsClientDefinition struct {
	Url            string                   `hcl:"url"`
	DialTimeout    hcl.Expression           `hcl:"dial_timeout,optional"`
	WriteQueueSize *int                     `hcl:"write_queue_size,optional"`
	AuthExpression hcl.Expression           `hcl:"auth,optional"`
	Headers        map[string]string        `hcl:"headers,optional"`
	Reconnect      *cfg.ReconnectDefinition `hcl:"reconnect,block"`
	DefRange       hcl.Range                `hcl:",def_range"`
}

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	clientDef := VinculumWebsocketsClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &clientDef)
	if diags.HasErrors() {
		return nil, diags
	}
	clientDef.DefRange = block.DefRange

	vinculumWebsocketsClients, ok := config.Clients["vws"]
	if !ok {
		vinculumWebsocketsClients = make(map[string]cfg.Client)
		config.Clients["vws"] = vinculumWebsocketsClients
	}

	existing, ok := vinculumWebsocketsClients[block.Labels[1]]
	if ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Vinculum WebSockets client already defined",
				Detail:   fmt.Sprintf("Vinculum WebSockets client %s already defined at %s", block.Labels[1], existing.GetDefRange()),
				Subject:  &clientDef.DefRange,
			},
		}
	}

	clientBuilder := client.NewClient().
		WithLogger(config.Logger).
		WithURL(clientDef.Url)

	if cfg.IsExpressionProvided(clientDef.DialTimeout) {
		dialTimeout, diags := config.ParseDuration(clientDef.DialTimeout)
		if diags.HasErrors() {
			return nil, diags
		}
		clientBuilder = clientBuilder.WithDialTimeout(dialTimeout)
	}

	if clientDef.WriteQueueSize != nil {
		clientBuilder = clientBuilder.WithWriteChannelSize(*clientDef.WriteQueueSize)
	}

	if clientDef.Headers != nil {
		for key, value := range clientDef.Headers {
			clientBuilder = clientBuilder.WithHeader(key, value)
		}
	}

	if cfg.IsExpressionProvided(clientDef.AuthExpression) {
		// @@@ TODO
	}

	c := &VinculumWebsocketClient{
		BaseBusClient: cfg.BaseBusClient{
			BaseClient: cfg.BaseClient{
				Name:     block.Labels[1],
				DefRange: clientDef.DefRange,
			},
		},
	}

	// The health monitor is installed whether or not a `reconnect` block was
	// declared, wrapping the reconnector when there is one. Without it, a
	// client with no reconnect block installed no monitor at all and its
	// connection state was invisible until something probed.
	monitor := &healthMonitor{client: c}
	if clientDef.Reconnect != nil {
		reconnector, diags := config.CreateReconnector(*clientDef.Reconnect)
		if diags.HasErrors() {
			return nil, diags
		}
		monitor.delegate = reconnector
	}
	c.ClientBuilder = clientBuilder.WithMonitor(monitor)

	return c, nil
}
