package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	mqttclient "github.com/tsarna/vinculum-mqtt/client"
	mqttpublisher "github.com/tsarna/vinculum-mqtt/publisher"
	mqttsubscriber "github.com/tsarna/vinculum-mqtt/subscriber"
	wire "github.com/tsarna/vinculum-wire"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterClientType("mqtt", process,
		cfg.WithSchema(mqttClientSchema), cfg.WithReadiness())
}

var mqttClientSchema = cfg.TypeSchema{
	Sample:  &MQTTClientDefinition{},
	Summary: "An MQTT client bridging an MQTT broker to the bus.",
	DocPage: "client-mqtt.md",
	Doc: `Connects to an MQTT broker, available in expressions as ` + "`client.<name>`" + `.

` + "`sender`" + ` blocks publish bus messages to MQTT topics; ` + "`receiver`" + ` blocks
subscribe to MQTT topics and deliver what arrives to the bus or an action.`,
	Attrs: map[string]cfg.AttrMeta{
		"brokers": {
			Summary: "Broker addresses to connect to.",
			Doc:     "For example `[\"tcp://mqtt.example.com:1883\"]`. Several addresses are tried in order.",
			Hint:    cfg.HintURL,
		},
		"client_id": {
			Summary: "MQTT client identifier presented to the broker.",
			Doc:     "Must be unique per connection.",
			Default: "vinculum-<name>-<hostname>",
		},
		"keep_alive": {
			Summary: "Interval at which to send keep-alive pings.",
			Hint:    cfg.HintDuration,
			Default: "30s",
		},
		"clean_start": {
			Summary: "Discard any session the broker holds for this client id.",
			Doc:     "When false, the broker resumes the existing session and replays queued messages.",
			Hint:    cfg.HintBool,
			Default: "false",
		},
		"session_expiry_interval": {
			Summary: "How long the broker keeps the session after disconnect.",
			Doc:     "Zero means the session ends when the connection closes.",
			Hint:    cfg.HintDuration,
			Default: "0",
		},
		"on_connect":    cfg.OnConnectAttr,
		"on_disconnect": cfg.OnDisconnectAttr,
		"wire_format":   cfg.WireFormatAttr,
		"metrics":       cfg.MetricsAttr,
		"tracing":       cfg.TracingAttr,
	},
	Blocks: map[string]cfg.TypeSchema{
		"auth": {
			Summary: "Credentials presented to the broker.",
			Attrs: map[string]cfg.AttrMeta{
				"username": {Summary: "Username to authenticate with."},
				"password": {Summary: "Password to authenticate with.", Doc: "Supply it from the environment rather than a literal."},
			},
		},
		"will": {
			Summary: "Message the broker publishes if this client disconnects ungracefully.",
			Attrs: map[string]cfg.AttrMeta{
				"topic":   {Summary: "MQTT topic to publish the will to.", Hint: cfg.HintTopicPattern},
				"payload": {Summary: "Payload of the will message."},
				"qos":     {Summary: "MQTT quality of service for the will.", Doc: "`0` at most once, `1` at least once, `2` exactly once.", Default: "0"},
				"retain":  {Summary: "Ask the broker to retain the will message.", Hint: cfg.HintBool, Default: "false"},
			},
		},
		"sender": {
			Summary: "Publishes bus messages to MQTT topics.",
			Attrs: map[string]cfg.AttrMeta{
				"qos": {
					Summary: "Default quality of service for published messages.",
					Doc: "`0` at most once, `1` at least once, `2` exactly once. At 1 and 2 a " +
						"publish waits for the broker's acknowledgement, so the send having " +
						"returned means the broker has the message. At `0` there is no " +
						"acknowledgement to wait for and the publish returns once the message is " +
						"written to the socket — which is what QoS 0 is rather than a " +
						"shortcoming, but it matters when this sender is bridging an " +
						"acknowledged transport: as the subscriber of a receiver with " +
						"`ack = \"auto\"`, a message published at `0` and then lost is one the " +
						"receiver has already acknowledged upstream.",
					Default: "1",
				},
				"retain": {Summary: "Ask the broker to retain published messages.", Hint: cfg.HintBool, Default: "false"},
				"default_topic_transform": {
					Summary: "How to derive an MQTT topic from a bus topic with no `topic` block.",
					Default: "verbatim",
				},
			},
			Blocks: map[string]cfg.TypeSchema{
				"topic": {
					Summary: "Maps bus topics matching a pattern to an MQTT topic.",
					Doc:     "The label is the bus topic pattern.",
					Attrs: map[string]cfg.AttrMeta{
						"mqtt_topic": {
							Summary: "MQTT topic to publish to.",
							Doc: "The vinculum topic is used verbatim when omitted. Evaluated per " +
								"message, so it can interpolate the fields the pattern captured.",
							Hint:    cfg.HintTopicPattern,
							Context: "message",
						},
						"qos":    {Summary: "Quality of service for this mapping.", Doc: "Overrides the sender's default."},
						"retain": {Summary: "Retain flag for this mapping.", Doc: "Overrides the sender's default.", Hint: cfg.HintBool},
					},
				},
			},
		},
		"receiver": {
			Summary: "Subscribes to MQTT topics and delivers what arrives.",
			Attrs: cfg.MergeAttrs(cfg.SubscriberSourceAttrs, map[string]cfg.AttrMeta{
				"on_decode_error": cfg.OnDecodeErrorAttr.WithContextFields(
					cfg.ContextField{
						Name: "mqtt_topic", Type: "string",
						Summary: "The MQTT topic the message arrived on.",
						Doc:     "Equal to `ctx.topic` here: a vinculum topic derived from the payload cannot be computed once the payload has failed to decode, so `ctx.topic` falls back to this.",
					},
				),
				"qos":             {Summary: "Default quality of service to subscribe with.", Doc: "`0` at most once, `1` at least once, `2` exactly once.", Default: "0"},
				"handle_retained": {Summary: "Deliver messages the broker retained before this subscription.", Hint: cfg.HintBool, Default: "true"},
				"shared_group": {
					Summary: "Shared subscription group name.",
					Doc:     "Instances in the same group share the topic's messages rather than each receiving all of them.",
				},
			}),
			Constraints: cfg.SubscriberSourceConstraints,
			Blocks: map[string]cfg.TypeSchema{
				"subscription": {
					Required: true,
					Summary:  "One MQTT topic filter to subscribe to.",
					Doc:      "The label is the MQTT topic filter.",
					Attrs: map[string]cfg.AttrMeta{
						"vinculum_topic": {
							Summary: "Bus topic to publish arriving messages to.",
							Doc: "The MQTT topic is used verbatim when omitted. Evaluated per " +
								"message, so placeholders captured by the filter can be interpolated.",
							Hint:    cfg.HintTopicPattern,
							Context: "inbound-message",
							ContextFields: []cfg.ContextField{
								{
									Name: "mqtt_topic", Type: "string",
									Summary: "The MQTT topic the message arrived on.",
									Doc: "The filter's literal match, with any `+` and `#` wildcards " +
										"filled in by what the broker delivered.",
								},
							},
						},
						"qos": {Summary: "Quality of service for this subscription.", Doc: "Overrides the receiver's default."},
					},
				},
			},
		},
	},
}

// ─── HCL definition structs ──────────────────────────────────────────────────

type MQTTClientDefinition struct {
	Brokers               []string                 `hcl:"brokers"`
	ClientID              hcl.Expression           `hcl:"client_id,optional"`
	KeepAlive             hcl.Expression           `hcl:"keep_alive,optional"`
	CleanStart            *bool                    `hcl:"clean_start,optional"`
	SessionExpiryInterval hcl.Expression           `hcl:"session_expiry_interval,optional"`
	TLS                   *cfg.TLSConfig           `hcl:"tls,block"`
	Auth                  *MQTTAuthDefinition      `hcl:"auth,block"`
	Reconnect             *cfg.ReconnectDefinition `hcl:"reconnect,block"`
	Will                  *MQTTWillDefinition      `hcl:"will,block"`
	OnConnect             hcl.Expression           `hcl:"on_connect,optional"`
	OnDisconnect          hcl.Expression           `hcl:"on_disconnect,optional"`
	Publishers            []MQTTPublisherDef       `hcl:"sender,block"`
	Subscribers           []MQTTSubscriberDef      `hcl:"receiver,block"`
	WireFormat            hcl.Expression           `hcl:"wire_format,optional"`
	Metrics               hcl.Expression           `hcl:"metrics,optional"`
	Tracing               hcl.Expression           `hcl:"tracing,optional"`
	DefRange              hcl.Range                `hcl:",def_range"`
}

type MQTTAuthDefinition struct {
	Username string         `hcl:"username,optional"`
	Password hcl.Expression `hcl:"password,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type MQTTWillDefinition struct {
	Topic    hcl.Expression `hcl:"topic"`
	Payload  hcl.Expression `hcl:"payload"`
	QoS      *int           `hcl:"qos,optional"`
	Retain   *bool          `hcl:"retain,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type MQTTPublisherDef struct {
	Name                  string                `hcl:"name,label"`
	QoS                   *int                  `hcl:"qos,optional"`
	Retain                *bool                 `hcl:"retain,optional"`
	TopicMappings         []MQTTTopicMappingDef `hcl:"topic,block"`
	DefaultTopicTransform string                `hcl:"default_topic_transform,optional"`
	DefRange              hcl.Range             `hcl:",def_range"`
}

type MQTTTopicMappingDef struct {
	Pattern   string         `hcl:"pattern,label"`
	MQTTTopic hcl.Expression `hcl:"mqtt_topic,optional"`
	QoS       *int           `hcl:"qos,optional"`
	Retain    *bool          `hcl:"retain,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type MQTTSubscriberDef struct {
	Name           string                       `hcl:"name,label"`
	Subscriber     hcl.Expression               `hcl:"subscriber,optional"`
	Action         hcl.Expression               `hcl:"action,optional"`
	Transforms     hcl.Expression               `hcl:"transforms,optional"`
	OnDecodeError  hcl.Expression               `hcl:"on_decode_error,optional"`
	QueueSize      *int                         `hcl:"queue_size,optional"`
	QoS            *int                         `hcl:"qos,optional"`
	HandleRetained *bool                        `hcl:"handle_retained,optional"`
	SharedGroup    string                       `hcl:"shared_group,optional"`
	Baggage        *hclutil.BaggageFilterConfig `hcl:"baggage,block"`
	Subscriptions  []MQTTTopicSubscriptionDef   `hcl:"subscription,block"`
	DefRange       hcl.Range                    `hcl:",def_range"`
}

type MQTTTopicSubscriptionDef struct {
	MQTTTopic     string         `hcl:"mqtt_topic,label"`
	VinculumTopic hcl.Expression `hcl:"vinculum_topic,optional"`
	QoS           *int           `hcl:"qos,optional"`
	DefRange      hcl.Range      `hcl:",def_range"`
}

// ─── Runtime structs ──────────────────────────────────────────────────────────

type builtMQTTPublisherSpec struct {
	name          string
	topicMappings []mqttpublisher.TopicMapping
	defaultXform  mqttpublisher.DefaultTopicTransform
	defaultQoS    byte
	defaultRetain bool
}

type builtMQTTSubscriberSpec struct {
	name           string
	subscriptions  []mqttsubscriber.TopicSubscription
	subscriber     bus.Subscriber
	handleRetained bool
	sharedGroup    string
	onDecodeError  wire.DecodeErrorHook
}

// MQTTPublisherProxy is a config-time bus.Subscriber that forwards OnEvent to
// a named MQTTPublisher. The actual publisher is wired in MQTTClientWrapper.Start().
type MQTTPublisherProxy struct {
	bus.BaseSubscriber
	mu            sync.RWMutex
	publisher     *mqttpublisher.MQTTPublisher
	clientName    string
	publisherName string
}

func (p *MQTTPublisherProxy) wirePublisher(pub *mqttpublisher.MQTTPublisher) {
	p.mu.Lock()
	p.publisher = pub
	p.mu.Unlock()
}

func (p *MQTTPublisherProxy) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	p.mu.RLock()
	pub := p.publisher
	p.mu.RUnlock()
	if pub == nil {
		return fmt.Errorf("mqtt client %q sender %q: not yet started", p.clientName, p.publisherName)
	}
	return pub.OnEvent(ctx, topic, msg, fields)
}

// Unwrap reports the publisher this stands in for, so a settle point asking
// what a delivery's return will mean sees the publisher's answer rather than
// this proxy's silence. Nil before Start, where OnEvent above errors anyway.
func (p *MQTTPublisherProxy) Unwrap() bus.Subscriber {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.publisher == nil {
		return nil
	}
	return p.publisher
}

// MQTTClientWrapper manages an MQTTClient lifecycle and implements bus.Subscriber
// by dispatching OnEvent to all publishers.
type MQTTClientWrapper struct {
	cfg.BaseClient
	bus.BaseSubscriber

	clientCfg        mqttclient.ClientConfig
	pubSpecs         []builtMQTTPublisherSpec
	subSpecs         []builtMQTTSubscriberSpec
	publisherProxies map[string]*MQTTPublisherProxy
	wireFormat       wire.WireFormat
	meterProvider    metric.MeterProvider
	tracerProvider   trace.TracerProvider
	logger           *zap.Logger

	mu         sync.RWMutex
	mqttClient *mqttclient.MQTTClient
	publishers []*mqttpublisher.MQTTPublisher
	connCancel context.CancelFunc
	// report tells the health subsystem the connection changed, so a drop is
	// visible at once rather than at the next probe. Set at registration, which
	// happens after this client is built and before anything starts.
	report cfg.ReadyReporter
}

// Ensure interface compliance.
var (
	_ cfg.Readyable     = (*MQTTClientWrapper)(nil)
	_ cfg.ReadyNotifier = (*MQTTClientWrapper)(nil)
)

// SetReadyReporter implements cfg.ReadyNotifier.
func (c *MQTTClientWrapper) SetReadyReporter(report cfg.ReadyReporter) {
	c.mu.Lock()
	c.report = report
	c.mu.Unlock()
}

func (c *MQTTClientWrapper) reportConnected() { c.reportHealth(nil) }

// reportDisconnected fires before any reconnection attempt, which is the
// guarantee on_disconnect already carries.
//
// The graceful-stop case is not covered — vinculum-mqtt clears its own state on
// a deliberate DISCONNECT and does not promise OnConnectionDown for it — and
// does not need to be: BeginDrain has already made readiness false before any
// client is stopped.
func (c *MQTTClientWrapper) reportDisconnected() {
	c.reportHealth(errors.New("not connected"))
}

func (c *MQTTClientWrapper) reportHealth(err error) {
	c.mu.RLock()
	report := c.report
	c.mu.RUnlock()

	if report != nil {
		report(err)
	}
}

func (c *MQTTClientWrapper) CtyValue() cty.Value {
	if len(c.publisherProxies) == 0 {
		return cfg.NewClientCapsule(c)
	}
	pubMap := make(map[string]cty.Value, len(c.publisherProxies))
	for name, proxy := range c.publisherProxies {
		pubMap[name] = cfg.NewSubscriberCapsule(proxy)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"senders": cfg.NewSubscriberCapsule(c),
		"sender":  cty.ObjectVal(pubMap),
	})
}

func (c *MQTTClientWrapper) Start() error {
	mqttCl, err := mqttclient.NewClient(c.clientCfg)
	if err != nil {
		return fmt.Errorf("mqtt client %q: %w", c.Name, err)
	}

	publishers := make([]*mqttpublisher.MQTTPublisher, 0, len(c.pubSpecs))
	for _, spec := range c.pubSpecs {
		b := mqttpublisher.NewPublisher().
			WithClientName(c.Name).
			WithDefaultQoS(spec.defaultQoS).
			WithDefaultRetain(spec.defaultRetain).
			WithDefaultTransform(spec.defaultXform).
			WithWireFormat(c.wireFormat).
			WithMeterProvider(c.meterProvider).
			WithTracerProvider(c.tracerProvider).
			WithLogger(c.logger)
		for _, tm := range spec.topicMappings {
			b = b.WithTopicMapping(tm)
		}
		p, buildErr := b.Build()
		if buildErr != nil {
			return fmt.Errorf("mqtt client %q sender %q: %w", c.Name, spec.name, buildErr)
		}
		if proxy, ok := c.publisherProxies[spec.name]; ok {
			proxy.wirePublisher(p)
		}
		mqttCl.AddPublisher(p)
		publishers = append(publishers, p)
	}

	for _, spec := range c.subSpecs {
		b := mqttsubscriber.NewSubscriber().
			WithClientName(c.Name).
			WithSubscriber(spec.subscriber).
			WithHandleRetained(spec.handleRetained).
			WithSharedGroup(spec.sharedGroup).
			WithWireFormat(c.wireFormat).
			WithDecodeErrorHook(spec.onDecodeError).
			WithMeterProvider(c.meterProvider).
			WithTracerProvider(c.tracerProvider).
			WithLogger(c.logger)
		for _, ts := range spec.subscriptions {
			b = b.WithSubscription(ts)
		}
		sub, buildErr := b.Build()
		if buildErr != nil {
			return fmt.Errorf("mqtt client %q subscriber %q: %w", c.Name, spec.name, buildErr)
		}
		mqttCl.AddSubscriber(sub)
	}

	connCtx, connCancel := context.WithCancel(context.Background())

	// Published before the connection is launched, not after it succeeds, so
	// Ready has something to answer with while the first connect is still in
	// flight: "not connected", which is the truth, rather than "not started",
	// which would be a lie about a client that is trying.
	c.mu.Lock()
	c.mqttClient = mqttCl
	c.publishers = publishers
	c.connCancel = connCancel
	c.mu.Unlock()

	// mqttclient.Start blocks until the first connection is up, by design and
	// by its documented contract. Waiting on it here made one unreachable
	// broker stall the whole serial boot loop — every client and every HTTP
	// listener after it, none of which had anything to do with MQTT. A caller
	// that does not want to block satisfies that contract with a goroutine;
	// this is not working around the library, it is declining to wait.
	//
	// Nothing awaits the result. autopaho retries on its own schedule and fires
	// OnConnect when it succeeds, which is what pushes readiness and runs the
	// user's on_connect — so the connection reports itself rather than being
	// waited for. Until then publishers have no publish function and OnEvent
	// says "not yet connected".
	go func() {
		if err := mqttCl.Start(connCtx); err != nil {
			// A cancelled context is Stop doing its job, not a failure.
			if connCtx.Err() != nil {
				return
			}
			// What is left is a connection manager that could not be built or
			// that terminated before ever connecting — a bad configuration, or
			// a reconnect limit reached. Neither is retried, so this line is
			// the only notice; the client stays not-ready with a reason.
			c.logger.Error("mqtt client failed to start",
				zap.String("client", c.Name), zap.Error(err))
		}
	}()

	return nil
}

// Ready implements cfg.Readyable: the broker connection is up.
//
// autopaho reconnects on its own, so a broker outage is a recoverable state
// rather than a dead client — exactly what readiness exists to report: out of
// rotation while the broker is away, back in when the reconnect loop succeeds.
//
// A broker that is not listening yet at boot lands on "not connected" and stays
// there until it is, which is the same state as an outage because it is the
// same state. "not started" needs Start not to have run at all, which a probe
// can only see if something reaches a contributor before the process gate
// opens.
func (c *MQTTClientWrapper) Ready(context.Context) error {
	c.mu.RLock()
	client := c.mqttClient
	c.mu.RUnlock()

	if client == nil {
		return errors.New("not started")
	}
	if !client.IsConnected() {
		return errors.New("not connected")
	}
	return nil
}

func (c *MQTTClientWrapper) Stop() error {
	c.mu.RLock()
	mqttCl := c.mqttClient
	connCancel := c.connCancel
	c.mu.RUnlock()

	if mqttCl == nil {
		return nil
	}

	err := mqttCl.Stop(context.Background())
	if connCancel != nil {
		connCancel()
	}
	return err
}

func (c *MQTTClientWrapper) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	if len(c.pubSpecs) == 0 {
		return fmt.Errorf("mqtt client %q: no senders configured", c.Name)
	}

	c.mu.RLock()
	publishers := c.publishers
	c.mu.RUnlock()

	if len(publishers) == 0 {
		return fmt.Errorf("mqtt client %q: not yet started", c.Name)
	}

	var errs []error
	for _, p := range publishers {
		if err := p.OnEvent(ctx, topic, msg, fields); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) > 1 {
		return fmt.Errorf("mqtt client %q: multiple publish errors: %v", c.Name, errs)
	}
	return nil
}

// DeliveryDisposition reduces the publishers this fans out to. One deferring
// publisher is enough to make this call's nil return mean less than "handled",
// so it dominates — the conservative direction. Unwrap cannot express this:
// there are N subscribers behind this one, not one.
func (c *MQTTClientWrapper) DeliveryDisposition() bus.Disposition {
	c.mu.RLock()
	publishers := c.publishers
	c.mu.RUnlock()

	for _, p := range publishers {
		if bus.DispositionOf(p) == bus.Deferred {
			return bus.Deferred
		}
	}
	return bus.Handled
}

// ─── Config processing ────────────────────────────────────────────────────────

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := MQTTClientDefinition{}
	diags := cfg.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	clientName := block.Labels[1]

	if len(def.Brokers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "mqtt: brokers is required",
			Subject:  &def.DefRange,
		}}
	}

	if len(def.Publishers) == 0 && len(def.Subscribers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "mqtt: at least one sender or receiver block is required",
			Subject:  &def.DefRange,
		}}
	}

	seenPubs := make(map[string]struct{}, len(def.Publishers))
	for _, p := range def.Publishers {
		if _, dup := seenPubs[p.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("mqtt: duplicate sender name %q", p.Name),
				Subject:  &p.DefRange,
			}}
		}
		seenPubs[p.Name] = struct{}{}
	}

	seenSubs := make(map[string]struct{}, len(def.Subscribers))
	for _, s := range def.Subscribers {
		if _, dup := seenSubs[s.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("mqtt: duplicate subscriber name %q", s.Name),
				Subject:  &s.DefRange,
			}}
		}
		seenSubs[s.Name] = struct{}{}
	}

	serverURLs := make([]*url.URL, 0, len(def.Brokers))
	for _, brokerStr := range def.Brokers {
		u, parseErr := url.Parse(brokerStr)
		if parseErr != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "mqtt: invalid broker URL",
				Detail:   fmt.Sprintf("%q: %v", brokerStr, parseErr),
				Subject:  &def.DefRange,
			}}
		}
		switch u.Scheme {
		case "mqtt", "mqtts", "ws", "wss":
			// valid
		default:
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "mqtt: invalid broker URL scheme",
				Detail:   fmt.Sprintf("%q uses scheme %q; use mqtt, mqtts, ws, or wss", brokerStr, u.Scheme),
				Subject:  &def.DefRange,
			}}
		}
		serverURLs = append(serverURLs, u)
	}

	hostname, _ := os.Hostname()
	clientID := "vinculum-" + clientName + "-" + hostname
	if cfg.IsExpressionProvided(def.ClientID) {
		val, valDiags := def.ClientID.Value(config.EvalCtx())
		if valDiags.HasErrors() {
			return nil, valDiags
		}
		if val.IsNull() || val.Type() != cty.String {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "mqtt: client_id must be a string",
				Subject:  &def.DefRange,
			}}
		}
		clientID = val.AsString()
	}

	var tlsCfg *tls.Config
	if def.TLS != nil && def.TLS.Enabled {
		c, tlsErr := def.TLS.BuildTLSClientConfig(config.BaseDir)
		if tlsErr != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "mqtt: invalid TLS config",
				Detail:   tlsErr.Error(),
				Subject:  &def.TLS.DefRange,
			}}
		}
		tlsCfg = c
	}

	keepAlive := 30 * time.Second
	if cfg.IsExpressionProvided(def.KeepAlive) {
		d, dDiags := config.ParseDuration(def.KeepAlive)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		keepAlive = d
	}

	var sessionExpiry uint32
	if cfg.IsExpressionProvided(def.SessionExpiryInterval) {
		d, dDiags := config.ParseDuration(def.SessionExpiryInterval)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		sessionExpiry = uint32(d / time.Second)
	}

	cleanStart := false
	if def.CleanStart != nil {
		cleanStart = *def.CleanStart
	}

	var username string
	var password []byte
	if def.Auth != nil {
		username = def.Auth.Username
		if cfg.IsExpressionProvided(def.Auth.Password) {
			val, valDiags := def.Auth.Password.Value(config.EvalCtx())
			if valDiags.HasErrors() {
				return nil, valDiags
			}
			if !val.IsNull() && val.Type() == cty.String {
				password = []byte(val.AsString())
			}
		}
	}

	var willCfg *mqttclient.WillConfig
	if def.Will != nil {
		willCfg = &mqttclient.WillConfig{}

		topicVal, topicDiags := def.Will.Topic.Value(config.EvalCtx())
		if topicDiags.HasErrors() {
			return nil, topicDiags
		}
		if topicVal.IsNull() || topicVal.Type() != cty.String {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "mqtt will: topic must be a string",
				Subject:  &def.Will.DefRange,
			}}
		}
		willCfg.Topic = topicVal.AsString()

		payloadVal, payloadDiags := def.Will.Payload.Value(config.EvalCtx())
		if payloadDiags.HasErrors() {
			return nil, payloadDiags
		}
		if !payloadVal.IsNull() && payloadVal.Type() == cty.String {
			willCfg.Payload = []byte(payloadVal.AsString())
		}

		if def.Will.QoS != nil {
			willCfg.QoS = byte(*def.Will.QoS)
		}
		if def.Will.Retain != nil {
			willCfg.Retain = *def.Will.Retain
		}
	}

	reconnectFn, reconnDiags := config.ReconnectBackoffFunc(def.Reconnect)
	if reconnDiags.HasErrors() {
		return nil, reconnDiags
	}

	// Allocated before the lifecycle hooks so they can be bound to it. The rest
	// of its fields are filled in below, once they have been built; nothing
	// reads them until Start.
	wrapper := &MQTTClientWrapper{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		logger: config.Logger,
	}

	onConnect := makeLifecycleHook(config, def.OnConnect, wrapper.reportConnected)
	onDisconnect := makeLifecycleHook(config, def.OnDisconnect, wrapper.reportDisconnected)

	mp, metricsDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}

	tracerProvider, tracingDiags := config.ResolveTracerProvider(def.Tracing)
	if tracingDiags.HasErrors() {
		return nil, tracingDiags
	}

	var wf wire.WireFormat = wire.Auto
	if cfg.IsExpressionProvided(def.WireFormat) {
		wfVal, wfDiags := def.WireFormat.Value(config.EvalCtx())
		if wfDiags.HasErrors() {
			return nil, wfDiags
		}
		resolved, err := cfg.GetWireFormatFromValue(wfVal)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "mqtt: invalid wire_format",
				Detail:   err.Error(),
				Subject:  def.WireFormat.Range().Ptr(),
			}}
		}
		wf = resolved
	}
	ctyWF := &cfg.CtyWireFormat{Inner: wf}

	clientCfg := mqttclient.ClientConfig{
		ServerURLs:            serverURLs,
		ClientName:            clientName,
		ClientID:              clientID,
		KeepAlive:             keepAlive,
		CleanStart:            cleanStart,
		SessionExpiryInterval: sessionExpiry,
		TLSConfig:             tlsCfg,
		Username:              username,
		Password:              password,
		WillMessage:           willCfg,
		ReconnectBackoffFunc:  reconnectFn,
		MaxReconnectAttempts:  cfg.ReconnectMaxAttempts(def.Reconnect),
		OnConnect:             onConnect,
		OnDisconnect:          onDisconnect,
		MeterProvider:         mp,
		Logger:                config.Logger,
	}

	pubSpecs, pubDiags := buildPublisherSpecs(config, def.Publishers)
	if pubDiags.HasErrors() {
		return nil, pubDiags
	}

	subSpecs, subDiags := buildSubscriberSpecs(config, clientName, def.Subscribers, tracerProvider)
	if subDiags.HasErrors() {
		return nil, subDiags
	}

	publisherProxies := make(map[string]*MQTTPublisherProxy, len(pubSpecs))
	for _, spec := range pubSpecs {
		publisherProxies[spec.name] = &MQTTPublisherProxy{
			clientName:    clientName,
			publisherName: spec.name,
		}
	}

	wrapper.clientCfg = clientCfg
	wrapper.pubSpecs = pubSpecs
	wrapper.subSpecs = subSpecs
	wrapper.publisherProxies = publisherProxies
	wrapper.wireFormat = ctyWF
	wrapper.meterProvider = mp
	wrapper.tracerProvider = tracerProvider

	config.Startables = append(config.Startables, wrapper)
	config.Stoppables = append(config.Stoppables, wrapper)

	return wrapper, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func buildPublisherSpecs(config *cfg.Config, defs []MQTTPublisherDef) ([]builtMQTTPublisherSpec, hcl.Diagnostics) {
	specs := make([]builtMQTTPublisherSpec, 0, len(defs))
	for _, def := range defs {
		spec, diags := buildPublisherSpec(config, def)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildPublisherSpec(config *cfg.Config, def MQTTPublisherDef) (builtMQTTPublisherSpec, hcl.Diagnostics) {
	spec := builtMQTTPublisherSpec{name: def.Name}

	spec.defaultQoS = 1
	if def.QoS != nil {
		spec.defaultQoS = byte(*def.QoS)
	}
	if def.Retain != nil {
		spec.defaultRetain = *def.Retain
	}

	switch def.DefaultTopicTransform {
	case "", "verbatim":
		spec.defaultXform = mqttpublisher.DefaultTopicVerbatim
	case "error":
		spec.defaultXform = mqttpublisher.DefaultTopicError
	case "ignore":
		spec.defaultXform = mqttpublisher.DefaultTopicIgnore
	default:
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("mqtt sender %q: invalid default_topic_transform", def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use verbatim, error, or ignore", def.DefaultTopicTransform),
			Subject:  &def.DefRange,
		}}
	}

	mappings := make([]mqttpublisher.TopicMapping, 0, len(def.TopicMappings))
	for _, tmDef := range def.TopicMappings {
		tm := mqttpublisher.TopicMapping{
			Pattern: tmDef.Pattern,
			QoS:     spec.defaultQoS,
			Retain:  spec.defaultRetain,
		}
		if tmDef.QoS != nil {
			tm.QoS = byte(*tmDef.QoS)
		}
		if tmDef.Retain != nil {
			tm.Retain = *tmDef.Retain
		}
		if cfg.IsExpressionProvided(tmDef.MQTTTopic) {
			tm.MQTTTopicFunc = makeMQTTTopicFunc(config, tmDef.MQTTTopic)
		}
		mappings = append(mappings, tm)
	}
	spec.topicMappings = mappings

	return spec, nil
}

func buildSubscriberSpecs(config *cfg.Config, clientName string, defs []MQTTSubscriberDef, tp trace.TracerProvider) ([]builtMQTTSubscriberSpec, hcl.Diagnostics) {
	specs := make([]builtMQTTSubscriberSpec, 0, len(defs))
	for _, def := range defs {
		spec, diags := buildSubscriberSpec(config, clientName, def, tp)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildSubscriberSpec(config *cfg.Config, clientName string, def MQTTSubscriberDef, tp trace.TracerProvider) (builtMQTTSubscriberSpec, hcl.Diagnostics) {
	spec := builtMQTTSubscriberSpec{
		name:           def.Name,
		handleRetained: true,
		sharedGroup:    def.SharedGroup,
		onDecodeError: cfg.MakeDecodeErrorHook(config, def.OnDecodeError,
			fmt.Sprintf("mqtt receiver %q", def.Name)),
	}
	if def.HandleRetained != nil {
		spec.handleRetained = *def.HandleRetained
	}

	if baggageDiags := def.Baggage.Validate(); baggageDiags.HasErrors() {
		return spec, baggageDiags
	}

	subscriber, diags := cfg.SubscriberSource{
		Subscriber: def.Subscriber,
		Action:     def.Action,
		Transforms: def.Transforms,
		QueueSize:  def.QueueSize,
	}.Resolve(config, def.DefRange, "mqtt/"+clientName+"/"+def.Name, tp)
	if diags.HasErrors() {
		return spec, diags
	}

	// Strip untrusted inbound baggage at this external boundary before it reaches
	// the action. Secure by default: a nil baggage block strips everything; opt
	// in with baggage { passthrough | allow | deny }.
	spec.subscriber = cfg.NewBaggageFilterSubscriber(def.Baggage, subscriber, config.Logger)

	if len(def.Subscriptions) == 0 {
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("mqtt receiver %q: at least one subscription block is required", def.Name),
			Subject:  &def.DefRange,
		}}
	}

	defaultQoS := byte(0)
	if def.QoS != nil {
		defaultQoS = byte(*def.QoS)
	}

	subs := make([]mqttsubscriber.TopicSubscription, 0, len(def.Subscriptions))
	for _, tsDef := range def.Subscriptions {
		qos := defaultQoS
		if tsDef.QoS != nil {
			qos = byte(*tsDef.QoS)
		}
		ts := mqttsubscriber.TopicSubscription{
			MQTTPattern: tsDef.MQTTTopic,
			QoS:         qos,
		}
		if cfg.IsExpressionProvided(tsDef.VinculumTopic) {
			ts.VinculumTopicFunc = makeMQTTVinculumTopicFunc(config, tsDef.VinculumTopic)
		}
		subs = append(subs, ts)
	}
	spec.subscriptions = subs

	return spec, nil
}

func makeMQTTTopicFunc(config *cfg.Config, expr hcl.Expression) mqttpublisher.MQTTTopicFunc {
	return func(topic string, msg any, fields map[string]string) (string, error) {
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("mqtt sender: convert msg: %w", err)
		}

		ctxBuilder := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("topic", topic).
			WithAttribute("msg", ctyMsg)

		ctyFields := make(map[string]cty.Value, len(fields))
		for k, v := range fields {
			ctyFields[k] = cty.StringVal(v)
		}
		ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))

		evalCtx, err := ctxBuilder.BuildEvalContext(config.EvalCtx())
		if err != nil {
			return "", err
		}

		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return "", diags
		}

		if val.IsNull() || val.Type() != cty.String {
			return "", fmt.Errorf("mqtt sender: mqtt_topic must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

// makeMQTTVinculumTopicFunc builds the per-message HCL evaluator for a
// receiver's `subscription { vinculum_topic = ... }` expression, against the
// shared `inbound-message` shape plus the MQTT topic the message arrived on.
func makeMQTTVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) mqttsubscriber.VinculumTopicFunc {
	return func(mqttTopic string, fields map[string]string, msg any) (string, error) {
		ctxBuilder, err := cfg.NewInboundContext(msg, fields)
		if err != nil {
			return "", fmt.Errorf("mqtt subscriber: %w", err)
		}

		evalCtx, err := ctxBuilder.
			WithStringAttribute("mqtt_topic", mqttTopic).
			BuildEvalContext(config.EvalCtx())
		if err != nil {
			return "", err
		}

		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return "", diags
		}

		if val.IsNull() {
			return "", nil
		}
		if val.Type() != cty.String {
			return "", fmt.Errorf("mqtt subscriber: vinculum_topic must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

// makeLifecycleHook builds the func the library calls on each connect or
// disconnect: it reports the state change to the health subsystem, then
// evaluates the user's `on_connect` / `on_disconnect` expression if there is
// one.
//
// It is returned unconditionally, where it used to be nil with no VCL hook
// declared. Health reporting does not depend on the configuration having asked
// for a hook — this is the point at which the client knows its connection
// changed, and it is the only one.
//
// notify runs first: it is a map write and a watcher notification, while the
// expression can do arbitrary I/O, and the library documents this callback as
// synchronous.
func makeLifecycleHook(config *cfg.Config, expr hcl.Expression, notify func()) func(ctx context.Context) {
	hasExpr := cfg.IsExpressionProvided(expr)
	return func(ctx context.Context) {
		notify()
		if !hasExpr {
			return
		}
		evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(config.EvalCtx())
		if err != nil {
			config.UserLogger.Error("mqtt lifecycle hook: build eval context", zap.Error(err))
			return
		}
		_, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			config.UserLogger.Error("mqtt lifecycle hook: eval failed", config.ActionError(diags))
		}
	}
}
