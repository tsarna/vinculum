// Package rabbitmq implements the `client "rabbitmq"` VCL block, wiring
// vinculum-rabbitmq senders and receivers into the vinculum config pipeline.
package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	rmqclient "github.com/tsarna/vinculum-rabbitmq/client"
	rmqreceiver "github.com/tsarna/vinculum-rabbitmq/receiver"
	rmqsender "github.com/tsarna/vinculum-rabbitmq/sender"
	wire "github.com/tsarna/vinculum-wire"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterClientType("rabbitmq", process,
		cfg.WithSchema(rabbitmqClientSchema), cfg.WithReadiness())
}

// ─── HCL definition structs ──────────────────────────────────────────────────

var rabbitmqClientSchema = cfg.TypeSchema{
	Sample:  &RMQClientDefinition{},
	Summary: "A RabbitMQ (AMQP 0-9-1) client bridging exchanges and queues to the bus.",
	DocPage: "client-rabbitmq.md",
	Doc: `Connects to a RabbitMQ broker, available in expressions as ` + "`client.<name>`" + `.

` + "`sender`" + ` blocks publish bus messages to an exchange; ` + "`receiver`" + ` blocks
consume a queue and deliver what arrives to the bus or an action.`,
	Attrs: map[string]cfg.AttrMeta{
		"brokers": {
			Summary: "Broker URLs to connect to.",
			Doc:     "For example `[\"amqp://guest:guest@rabbit:5672/\"]`. Several are tried in order.",
			Hint:    cfg.HintURL,
		},
		"heartbeat": {
			Summary: "Interval between AMQP heartbeats.",
			Doc: "Zero disables them, which leaves a silently broken TCP connection " +
				"undetected until something tries to use it.",
			Hint:    cfg.HintDuration,
			Default: "10s",
		},
		"connection_timeout": {
			Summary: "Deadline for establishing a connection.",
			Doc:     "Covers the TCP dial and the AMQP handshake together.",
			Hint:    cfg.HintDuration,
			Default: "30s",
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
			Doc:     "Overrides any credentials embedded in the broker URL.",
			Attrs: map[string]cfg.AttrMeta{
				"username": {Summary: "Username to authenticate with."},
				"password": {Summary: "Password to authenticate with.", Doc: "Supply it from the environment rather than a literal."},
			},
		},
		"sender": {
			Summary: "Publishes bus messages to an exchange.",
			Attrs: map[string]cfg.AttrMeta{
				"exchange": {
					Summary: "Exchange to publish to.",
					Doc: "The default exchange (`\"\"`) routes to the queue named by the " +
						"routing key, which is enough for point-to-point messaging.",
				},
				"confirm_mode": {
					Summary: "Wait for the broker to confirm each publish.",
					Doc: "A publish blocks until acknowledged, and a nack surfaces as an " +
						"error. Confirms guarantee delivery to the *exchange*, not to a " +
						"queue: with no binding for the routing key the message is still " +
						"discarded unless `mandatory` is set.",
					Hint:    cfg.HintBool,
					Default: "true",
				},
				"mandatory": {
					Summary: "Return messages the exchange cannot route to any queue.",
					Doc: "Without this, an unroutable message is silently discarded. " +
						"Requires `confirm_mode` for the return to surface as an error " +
						"rather than only as a metric.",
					Hint:    cfg.HintBool,
					Default: "false",
				},
				"persistent": {
					Summary: "Mark messages persistent so they survive a broker restart.",
					Doc: "Delivery mode 2, which the broker writes to disk — and which " +
						"survives a restart only if the queue is durable too. Turning it " +
						"off trades that for throughput.",
					Hint:    cfg.HintBool,
					Default: "true",
				},
				"default_topic_transform": {
					Summary: "How to derive a routing key from a bus topic with no `topic` block.",
					Doc: "`slash_to_dot` rewrites `a/b/c` as `a.b.c`, matching AMQP's " +
						"convention; `verbatim` publishes the bus topic unchanged; `error` " +
						"fails the publish; `ignore` drops the message.",
					Enum:    []string{"slash_to_dot", "verbatim", "error", "ignore"},
					Default: "slash_to_dot",
				},
			},
			Blocks: map[string]cfg.TypeSchema{
				"topic": {
					Summary: "Maps bus topics matching a pattern to a routing key.",
					Doc:     "The label is the bus topic pattern.",
					Attrs: map[string]cfg.AttrMeta{
						"routing_key": {
							Summary: "Routing key to publish with.",
							Doc: "`default_topic_transform` applies when omitted. Evaluated per " +
								"message, so it can interpolate the fields the pattern captured.",
							Context: "message",
						},
						"exchange":   {Summary: "Exchange for this mapping.", Doc: "Overrides the sender's exchange."},
						"persistent": {Summary: "Persistence for this mapping.", Doc: "Overrides the sender's default.", Hint: cfg.HintBool},
					},
				},
			},
		},
		"receiver": {
			Summary: "Consumes a queue and delivers what arrives.",
			Attrs: cfg.MergeAttrs(cfg.SubscriberSourceAttrs, map[string]cfg.AttrMeta{
				"queue": {
					Summary: "Queue to consume from.",
					Doc: "With no `declare` block the queue is declared passively when " +
						"the client connects, so a missing queue is reported at once " +
						"rather than on the first message. The connection is retried, " +
						"so a queue that has not been provisioned yet leaves the client " +
						"not ready with the broker's own error, and it recovers when the " +
						"queue appears.",
				},
				"prefetch": {
					Summary: "Unacknowledged messages the broker may have in flight.",
					Doc: "Bounds how much work is outstanding at once. Zero is unlimited, " +
						"which lets the broker push an entire queue at once.",
					Default: "10",
				},
				"exclusive": {
					Summary: "Claim the queue exclusively for this connection.",
					Doc: "Only one consumer may be active on the queue, so this rules out " +
						"running more than one instance against it.",
					Hint:    cfg.HintBool,
					Default: "false",
				},
				"ack": cfg.AckAttr.
					WithDoc("`auto` acknowledges once delivery returns without error, which is "+
						"fast but loses a message whose handling fails after that point, so it "+
						"is refused alongside `queue_size`, which makes delivery return at the "+
						"moment the message is queued. `manual` acknowledges nothing until the "+
						"configuration calls `inbound::ack()`, and requires `settle_timeout`; "+
						"`inbound::nack()` rejects the message without requeueing it, so it "+
						"reaches the queue's dead-letter exchange if it has one and is dropped "+
						"if it does not, and the reason reaches the log only. `none` is AMQP's "+
						"own no-ack mode, where the *broker* treats the message as delivered "+
						"the moment it is sent and vinculum never acknowledges at all: faster "+
						"still, and the message is gone if handling fails or the process dies "+
						"holding it.").
					WithEnum(cfg.AckAuto, cfg.AckManual, cfg.AckNone),
				"settle_timeout": cfg.SettleTimeoutAttr,
				"default_routing_key_transform": {
					Summary: "How to derive a bus topic from a routing key with no `subscription` block.",
					Doc: "`dot_to_slash` rewrites `a.b.c` as `a/b/c`, matching the bus's " +
						"convention; `verbatim` uses the routing key unchanged; `error` " +
						"fails the delivery; `ignore` drops the message.",
					Enum:    []string{"dot_to_slash", "verbatim", "error", "ignore"},
					Default: "dot_to_slash",
				},
				"on_decode_error": cfg.OnDecodeErrorAttr.WithContextFields(
					cfg.ContextField{Name: "routing_key", Type: "string", Summary: "Routing key the message was delivered with."},
					cfg.ContextField{Name: "exchange", Type: "string", Summary: "Exchange the message was published to."},
					cfg.ContextField{Name: "queue", Type: "string", Summary: "Queue this receiver consumes from."},
				),
			}),
			Constraints: cfg.SubscriberSourceConstraints,
			Blocks: map[string]cfg.TypeSchema{
				"declare": {
					Summary: "Declare the queue if it does not already exist.",
					Doc: "Advanced queue arguments — dead-letter exchange, message TTL, " +
						"maximum length — are deliberately not exposed here; set them with " +
						"a RabbitMQ policy instead.",
					Attrs: map[string]cfg.AttrMeta{
						"durable":     {Summary: "Keep the queue across broker restarts.", Hint: cfg.HintBool, Default: "true"},
						"auto_delete": {Summary: "Delete the queue once its last consumer disconnects.", Hint: cfg.HintBool, Default: "false"},
					},
				},
				"binding": {
					Summary: "Bind the queue to an exchange with a routing key.",
					Doc:     "The label is the routing key pattern.",
					Attrs: map[string]cfg.AttrMeta{
						"exchange": {Summary: "Exchange to bind to."},
					},
				},
				"subscription": {
					Summary: "Maps arriving routing keys to a bus topic.",
					Doc:     "The label is the routing key pattern.",
					Attrs: map[string]cfg.AttrMeta{
						"vinculum_topic": {
							Summary: "Bus topic to publish arriving messages to.",
							Doc: "`default_routing_key_transform` applies when omitted. Evaluated " +
								"per delivery, so it can interpolate the fields the routing-key " +
								"pattern captured. `ctx.fields` is the AMQP headers table merged " +
								"with those captures.",
							Hint:    cfg.HintTopicPattern,
							Context: "inbound-message",
							ContextFields: []cfg.ContextField{
								{Name: "routing_key", Type: "string", Summary: "Routing key the message was delivered with."},
								{Name: "exchange", Type: "string", Summary: "Exchange the message was published to."},
							},
						},
					},
				},
			},
		},
	},
}

type RMQClientDefinition struct {
	Brokers           []string                 `hcl:"brokers"`
	Auth              *RMQAuthDefinition       `hcl:"auth,block"`
	TLS               *cfg.TLSConfig           `hcl:"tls,block"`
	Heartbeat         hcl.Expression           `hcl:"heartbeat,optional"`
	ConnectionTimeout hcl.Expression           `hcl:"connection_timeout,optional"`
	Reconnect         *cfg.ReconnectDefinition `hcl:"reconnect,block"`
	OnConnect         hcl.Expression           `hcl:"on_connect,optional"`
	OnDisconnect      hcl.Expression           `hcl:"on_disconnect,optional"`
	Senders           []RMQSenderDefinition    `hcl:"sender,block"`
	Receivers         []RMQReceiverDefinition  `hcl:"receiver,block"`
	WireFormat        hcl.Expression           `hcl:"wire_format,optional"`
	Metrics           hcl.Expression           `hcl:"metrics,optional"`
	Tracing           hcl.Expression           `hcl:"tracing,optional"`
	DefRange          hcl.Range                `hcl:",def_range"`
}

type RMQAuthDefinition struct {
	Username string         `hcl:"username,optional"`
	Password hcl.Expression `hcl:"password,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type RMQSenderDefinition struct {
	Name                  string               `hcl:"name,label"`
	Exchange              string               `hcl:"exchange"`
	ConfirmMode           *bool                `hcl:"confirm_mode,optional"`
	Mandatory             *bool                `hcl:"mandatory,optional"`
	Persistent            *bool                `hcl:"persistent,optional"`
	Topics                []RMQTopicDefinition `hcl:"topic,block"`
	DefaultTopicTransform string               `hcl:"default_topic_transform,optional"`
	DefRange              hcl.Range            `hcl:",def_range"`
}

type RMQTopicDefinition struct {
	Pattern    string         `hcl:"pattern,label"`
	RoutingKey hcl.Expression `hcl:"routing_key,optional"`
	Exchange   string         `hcl:"exchange,optional"`
	Persistent *bool          `hcl:"persistent,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type RMQReceiverDefinition struct {
	Name                       string                       `hcl:"name,label"`
	Queue                      string                       `hcl:"queue"`
	Subscriber                 hcl.Expression               `hcl:"subscriber,optional"`
	Action                     hcl.Expression               `hcl:"action,optional"`
	Transforms                 hcl.Expression               `hcl:"transforms,optional"`
	OnDecodeError              hcl.Expression               `hcl:"on_decode_error,optional"`
	QueueSize                  *int                         `hcl:"queue_size,optional"`
	Prefetch                   *int                         `hcl:"prefetch,optional"`
	Exclusive                  *bool                        `hcl:"exclusive,optional"`
	Ack                        string                       `hcl:"ack,optional"`
	AckRange                   hcl.Range                    `hcl:"ack,attr_range"`
	SettleTimeout              hcl.Expression               `hcl:"settle_timeout,optional"`
	QueueSizeRange             hcl.Range                    `hcl:"queue_size,attr_range"`
	Baggage                    *hclutil.BaggageFilterConfig `hcl:"baggage,block"`
	Declare                    *RMQQueueDeclareDefinition   `hcl:"declare,block"`
	Bindings                   []RMQBindingDefinition       `hcl:"binding,block"`
	Subscriptions              []RMQSubscriptionDefinition  `hcl:"subscription,block"`
	DefaultRoutingKeyTransform string                       `hcl:"default_routing_key_transform,optional"`
	DefRange                   hcl.Range                    `hcl:",def_range"`
}

type RMQQueueDeclareDefinition struct {
	Durable    *bool     `hcl:"durable,optional"`
	AutoDelete *bool     `hcl:"auto_delete,optional"`
	DefRange   hcl.Range `hcl:",def_range"`
}

type RMQBindingDefinition struct {
	RoutingKey string    `hcl:"routing_key,label"`
	Exchange   string    `hcl:"exchange"`
	DefRange   hcl.Range `hcl:",def_range"`
}

type RMQSubscriptionDefinition struct {
	RoutingKeyPattern string         `hcl:"routing_key_pattern,label"`
	VinculumTopic     hcl.Expression `hcl:"vinculum_topic,optional"`
	DefRange          hcl.Range      `hcl:",def_range"`
}

// ─── Runtime specs ───────────────────────────────────────────────────────────

type builtSenderSpec struct {
	name                  string
	exchange              string
	confirmMode           bool
	mandatory             bool
	persistent            bool
	topicMappings         []rmqsender.TopicMapping
	defaultTopicTransform rmqsender.DefaultTopicTransform
}

type builtReceiverSpec struct {
	name          string
	queue         string
	subscriber    bus.Subscriber
	subscriptions []rmqreceiver.Subscription
	defaultXform  rmqreceiver.DefaultRoutingKeyTransform
	prefetch      int
	exclusive     bool
	ackMode       rmqreceiver.AckMode
	declare       *rmqreceiver.Declare
	bindings      []rmqreceiver.Binding
	onDecodeError wire.DecodeErrorHook
}

// ─── Sender proxy ────────────────────────────────────────────────────────────

// RMQSenderProxy is a config-time bus.Subscriber that forwards OnEvent to
// a named RMQSender. The actual sender is wired in RMQClientWrapper.Start().
type RMQSenderProxy struct {
	bus.BaseSubscriber
	mu         sync.RWMutex
	sender     *rmqsender.RMQSender
	clientName string
	senderName string
}

func (p *RMQSenderProxy) wireSender(s *rmqsender.RMQSender) {
	p.mu.Lock()
	p.sender = s
	p.mu.Unlock()
}

func (p *RMQSenderProxy) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	p.mu.RLock()
	s := p.sender
	p.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("rabbitmq client %q sender %q: not yet started", p.clientName, p.senderName)
	}
	return s.OnEvent(ctx, topic, msg, fields)
}

// ─── Client wrapper ──────────────────────────────────────────────────────────

// RMQClientWrapper manages the lifecycle of a single AMQP connection and
// implements bus.Subscriber by dispatching OnEvent to all senders (fan-out).
type RMQClientWrapper struct {
	cfg.BaseClient
	bus.BaseSubscriber

	clientCfg     rmqclient.Config
	senderSpecs   []builtSenderSpec
	receiverSpecs []builtReceiverSpec
	senderProxies map[string]*RMQSenderProxy

	wireFormat     wire.WireFormat
	meterProvider  metric.MeterProvider
	tracerProvider trace.TracerProvider
	logger         *zap.Logger

	mu      sync.RWMutex
	client  *rmqclient.Client
	senders []*rmqsender.RMQSender
	// report tells the health subsystem the connection changed, so a drop is
	// visible at once rather than at the next probe. Set at registration, which
	// happens after this client is built and before anything starts.
	report cfg.ReadyReporter
}

// SetReadyReporter implements cfg.ReadyNotifier.
func (c *RMQClientWrapper) SetReadyReporter(report cfg.ReadyReporter) {
	c.mu.Lock()
	c.report = report
	c.mu.Unlock()
}

func (c *RMQClientWrapper) reportConnected() { c.reportHealth(nil) }

// reportDisconnected fires before any reconnection attempt, and on a graceful
// Stop as well — vinculum-rabbitmq guards both on `wasConnected`, so it runs
// exactly once per dropped connection.
func (c *RMQClientWrapper) reportDisconnected() {
	c.reportHealth(errors.New("not connected"))
}

func (c *RMQClientWrapper) reportHealth(err error) {
	c.mu.RLock()
	report := c.report
	c.mu.RUnlock()

	if report != nil {
		report(err)
	}
}

// CtyValue exposes the client as `client.<name>` in HCL. When there are
// senders, it returns an object with `senders` (fan-out subscriber capsule)
// and `sender.<senderName>` (per-sender subscriber capsules). With no
// senders, a plain client capsule is returned.
func (c *RMQClientWrapper) CtyValue() cty.Value {
	if len(c.senderProxies) == 0 {
		return cfg.NewClientCapsule(c)
	}
	senderMap := make(map[string]cty.Value, len(c.senderProxies))
	for name, proxy := range c.senderProxies {
		senderMap[name] = cfg.NewSubscriberCapsule(proxy)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"senders": cfg.NewSubscriberCapsule(c),
		"sender":  cty.ObjectVal(senderMap),
	})
}

func (c *RMQClientWrapper) Start() error {
	cli := rmqclient.NewClient(c.clientCfg)

	senders := make([]*rmqsender.RMQSender, 0, len(c.senderSpecs))
	for _, spec := range c.senderSpecs {
		b := rmqsender.NewSender().
			WithClientName(c.Name).
			WithExchange(spec.exchange).
			WithMandatory(spec.mandatory).
			WithPersistent(spec.persistent).
			WithConfirmMode(spec.confirmMode).
			WithDefaultTransform(spec.defaultTopicTransform).
			WithWireFormat(c.wireFormat).
			WithMeterProvider(c.meterProvider).
			WithTracerProvider(c.tracerProvider).
			WithLogger(c.logger)
		for _, tm := range spec.topicMappings {
			b = b.WithTopicMapping(tm)
		}
		s, err := b.Build()
		if err != nil {
			return fmt.Errorf("rabbitmq client %q sender %q: %w", c.Name, spec.name, err)
		}
		if proxy, ok := c.senderProxies[spec.name]; ok {
			proxy.wireSender(s)
		}
		cli.AddSender(s)
		senders = append(senders, s)
	}

	for _, spec := range c.receiverSpecs {
		b := rmqreceiver.NewReceiver().
			WithClientName(c.Name).
			WithQueue(spec.queue).
			WithSubscriber(spec.subscriber).
			WithDefaultTransform(spec.defaultXform).
			WithPrefetch(spec.prefetch).
			WithExclusive(spec.exclusive).
			WithAckMode(spec.ackMode).
			WithWireFormat(c.wireFormat).
			WithDecodeErrorHook(spec.onDecodeError).
			WithMeterProvider(c.meterProvider).
			WithTracerProvider(c.tracerProvider).
			WithLogger(c.logger)
		for _, sub := range spec.subscriptions {
			b = b.WithSubscription(sub)
		}
		if spec.declare != nil {
			b = b.WithDeclare(*spec.declare)
		}
		for _, bind := range spec.bindings {
			b = b.WithBinding(bind)
		}
		r, err := b.Build()
		if err != nil {
			return fmt.Errorf("rabbitmq client %q receiver %q: %w", c.Name, spec.name, err)
		}
		cli.AddReceiver(r)
	}

	// Published before the connection is launched, so Ready has something to
	// answer with while the first connect is still in flight: "not connected",
	// which is the truth, rather than "not started", which would be a lie about
	// a client that is trying.
	c.mu.Lock()
	c.client = cli
	c.senders = senders
	c.mu.Unlock()

	// Start no longer dials — it launches the connect-and-watch loop and
	// returns. What it can still refuse is a configuration it cannot use at
	// all: no brokers, or a second Start. Neither improves on a retry, and
	// neither is reachable from a config that passed validation, so a failure
	// here is a wiring bug rather than an outage.
	if err := cli.Start(context.Background()); err != nil {
		return cfg.Terminal(fmt.Errorf("rabbitmq client %q: %w", c.Name, err))
	}
	return nil
}

// Ready implements cfg.Readyable: the broker connection is up.
//
// The client reconnects on its own, so a broker outage is a recoverable state
// rather than a dead client — exactly what readiness exists to report: out of
// rotation while the broker is away, back in when the reconnect loop succeeds.
//
// A broker that is not listening yet at boot lands on "not connected" and stays
// there until it is. That used to be "not started" forever: a failed first dial
// returned from Start before the reconnect watcher was ever spawned, so the
// client was dead for the life of the process and the report, while accurate,
// described something no probe would ever see change.
func (c *RMQClientWrapper) Ready(context.Context) error {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return errors.New("not started")
	}
	if !client.IsConnected() {
		return errors.New("not connected")
	}
	return nil
}

func (c *RMQClientWrapper) Stop() error {
	c.mu.RLock()
	cli := c.client
	c.mu.RUnlock()
	if cli == nil {
		return nil
	}
	return cli.Stop()
}

// OnEvent fans an event out to all senders. Errors from individual senders
// are collected and the first one is returned (or a combined message when
// there are several).
func (c *RMQClientWrapper) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	if len(c.senderSpecs) == 0 {
		return fmt.Errorf("rabbitmq client %q: no senders configured", c.Name)
	}
	c.mu.RLock()
	senders := c.senders
	c.mu.RUnlock()
	if len(senders) == 0 {
		return fmt.Errorf("rabbitmq client %q: not yet started", c.Name)
	}

	var errs []error
	for _, s := range senders {
		if err := s.OnEvent(ctx, topic, msg, fields); err != nil {
			errs = append(errs, err)
		}
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("rabbitmq client %q: multiple publish errors: %v", c.Name, errs)
	}
}

// ─── Process ─────────────────────────────────────────────────────────────────

// renamedReceiverAttrs retires the receiver's `auto_ack`, whose boolean had to
// carry a distinction the other receivers spelled differently — and inverted:
// `auto_ack = false` here was vinculum-acknowledges-after-handling, the same
// behaviour Redis spelled `auto_ack = true`.
//
// The descent stops at the receiver. Its nested `declare` block has an
// `auto_delete` of its own, meaning AMQP's delete-the-queue-when-unused, and a
// rename table that reached it would report a perfectly good attribute.
var renamedReceiverAttrs = cfg.RenameSpec{
	Blocks: []cfg.RenamedInBlock{{
		Type:   "receiver",
		Labels: []string{"name"},
		Spec: cfg.RenameSpec{
			Attrs: map[string]cfg.RenamedAttr{
				"auto_ack": {
					Now:   "ack",
					Since: "0.46.0",
					Note: "`auto_ack = false` was the default and meant vinculum acknowledged " +
						"after handling — now written `ack = \"auto\"`, which is also the " +
						"default. `auto_ack = true` was AMQP's own no-ack mode, where the " +
						"broker considers the message delivered on send, and is now " +
						"`ack = \"none\"`.",
				},
			},
		},
	}},
}

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	// Before decoding, so a configuration still saying auto_ack is told what it
	// became rather than that the argument is not expected here.
	if diags := cfg.CheckRenamedAttrs(remainingBody, renamedReceiverAttrs); diags.HasErrors() {
		return nil, diags
	}

	def := RMQClientDefinition{}
	diags := cfg.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	clientName := block.Labels[1]

	if len(def.Brokers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "rabbitmq: brokers is required",
			Subject:  &def.DefRange,
		}}
	}

	parsedURLs, hasAmqps, hasAmqp, urlDiags := parseBrokers(def.Brokers, def.DefRange)
	if urlDiags.HasErrors() {
		return nil, urlDiags
	}

	if err := validateUniqueNames(def); err != nil {
		return nil, err
	}

	tlsCfg, tlsDiags := resolveTLS(config, def.TLS, hasAmqps, hasAmqp, def.DefRange)
	if tlsDiags.HasErrors() {
		return nil, tlsDiags
	}

	heartbeat := 10 * time.Second
	if cfg.IsExpressionProvided(def.Heartbeat) {
		d, dDiags := config.ParseDuration(def.Heartbeat)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		heartbeat = d
	}

	connTimeout := 30 * time.Second
	if cfg.IsExpressionProvided(def.ConnectionTimeout) {
		d, dDiags := config.ParseDuration(def.ConnectionTimeout)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		connTimeout = d
	}

	var username, password string
	if def.Auth != nil {
		username = def.Auth.Username
		if cfg.IsExpressionProvided(def.Auth.Password) {
			val, valDiags := def.Auth.Password.Value(config.EvalCtx())
			if valDiags.HasErrors() {
				return nil, valDiags
			}
			if !val.IsNull() && val.Type() == cty.String {
				password = val.AsString()
			}
		}
	}

	reconnectFn, recDiags := config.ReconnectBackoffFunc(def.Reconnect)
	if recDiags.HasErrors() {
		return nil, recDiags
	}

	// Allocated before the lifecycle hooks so they can be bound to it. The rest
	// of its fields are filled in below, once they have been built; nothing
	// reads them until Start.
	wrapper := &RMQClientWrapper{
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
				Summary:  "rabbitmq: invalid wire_format",
				Detail:   err.Error(),
				Subject:  def.WireFormat.Range().Ptr(),
			}}
		}
		wf = resolved
	}
	ctyWF := &cfg.CtyWireFormat{Inner: wf}

	senderSpecs, sDiags := buildSenderSpecs(config, def.Senders)
	if sDiags.HasErrors() {
		return nil, sDiags
	}

	receiverSpecs, rDiags := buildReceiverSpecs(config, clientName, def.Receivers, tracerProvider)
	if rDiags.HasErrors() {
		return nil, rDiags
	}

	senderProxies := make(map[string]*RMQSenderProxy, len(senderSpecs))
	for _, spec := range senderSpecs {
		senderProxies[spec.name] = &RMQSenderProxy{
			clientName: clientName,
			senderName: spec.name,
		}
	}

	wrapper.clientCfg = rmqclient.Config{
		ClientName:           clientName,
		Brokers:              urlsToStrings(parsedURLs),
		Username:             username,
		Password:             password,
		Heartbeat:            heartbeat,
		ConnectionTimeout:    connTimeout,
		TLSClientConfig:      tlsCfg,
		Logger:               config.Logger,
		OnConnect:            onConnect,
		OnDisconnect:         onDisconnect,
		ReconnectBackoff:     reconnectFn,
		MaxReconnectAttempts: cfg.ReconnectMaxAttempts(def.Reconnect),
		MeterProvider:        mp,
	}
	wrapper.senderSpecs = senderSpecs
	wrapper.receiverSpecs = receiverSpecs
	wrapper.senderProxies = senderProxies
	wrapper.wireFormat = ctyWF
	wrapper.meterProvider = mp
	wrapper.tracerProvider = tracerProvider

	config.Startables = append(config.Startables, wrapper)
	config.Stoppables = append(config.Stoppables, wrapper)

	return wrapper, nil
}

// ─── Sub-builders ────────────────────────────────────────────────────────────

func buildSenderSpecs(config *cfg.Config, defs []RMQSenderDefinition) ([]builtSenderSpec, hcl.Diagnostics) {
	specs := make([]builtSenderSpec, 0, len(defs))
	for _, d := range defs {
		spec, diags := buildSenderSpec(config, d)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildSenderSpec(config *cfg.Config, def RMQSenderDefinition) (builtSenderSpec, hcl.Diagnostics) {
	spec := builtSenderSpec{
		name:        def.Name,
		exchange:    def.Exchange,
		confirmMode: true, // default
		persistent:  true, // default
	}
	if def.ConfirmMode != nil {
		spec.confirmMode = *def.ConfirmMode
	}
	if def.Mandatory != nil {
		spec.mandatory = *def.Mandatory
	}
	if def.Persistent != nil {
		spec.persistent = *def.Persistent
	}

	xform, xformDiags := parseDefaultTopicTransform(def.DefaultTopicTransform, def.Name, def.DefRange)
	if xformDiags.HasErrors() {
		return spec, xformDiags
	}
	spec.defaultTopicTransform = xform

	mappings := make([]rmqsender.TopicMapping, 0, len(def.Topics))
	for _, t := range def.Topics {
		tm := rmqsender.TopicMapping{
			Pattern:  t.Pattern,
			Exchange: t.Exchange,
		}
		if t.Persistent != nil {
			p := *t.Persistent
			tm.Persistent = &p
		}
		if cfg.IsExpressionProvided(t.RoutingKey) {
			tm.RoutingKeyFunc = makeRoutingKeyFunc(config, t.RoutingKey)
		}
		mappings = append(mappings, tm)
	}
	spec.topicMappings = mappings

	return spec, nil
}

func buildReceiverSpecs(config *cfg.Config, clientName string, defs []RMQReceiverDefinition, tp trace.TracerProvider) ([]builtReceiverSpec, hcl.Diagnostics) {
	specs := make([]builtReceiverSpec, 0, len(defs))
	for _, d := range defs {
		spec, diags := buildReceiverSpec(config, clientName, d, tp)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildReceiverSpec(config *cfg.Config, clientName string, def RMQReceiverDefinition, tp trace.TracerProvider) (builtReceiverSpec, hcl.Diagnostics) {
	spec := builtReceiverSpec{
		name:     def.Name,
		queue:    def.Queue,
		prefetch: 10, // default
		onDecodeError: cfg.MakeDecodeErrorHook(config, def.OnDecodeError,
			fmt.Sprintf("rabbitmq receiver %q", def.Name)),
	}

	if def.Prefetch != nil {
		spec.prefetch = *def.Prefetch
	}
	if def.Exclusive != nil {
		spec.exclusive = *def.Exclusive
	}
	policy, ackDiags := config.ResolveAck(cfg.AckRequest{
		Receiver:       fmt.Sprintf("rabbitmq client %q receiver %q", clientName, def.Name),
		Value:          def.Ack,
		ValueRange:     def.AckRange,
		SettleTimeout:  def.SettleTimeout,
		QueueSize:      def.QueueSize,
		QueueSizeRange: def.QueueSizeRange,
		Extra:          cfg.AckNone,
		DefRange:       def.DefRange,
	})
	if ackDiags.HasErrors() {
		return spec, ackDiags
	}
	switch policy.Mode {
	case cfg.AckNone:
		// AMQP's no-ack mode: the broker considers the message delivered on
		// send, and there is nothing left for anyone to settle.
		spec.ackMode = rmqreceiver.AckNone
	case cfg.AckManual:
		spec.ackMode = rmqreceiver.AckManual
	default:
		spec.ackMode = rmqreceiver.AckAfterHandling
	}

	xform, xformDiags := parseDefaultRoutingKeyTransform(def.DefaultRoutingKeyTransform, def.Name, def.DefRange)
	if xformDiags.HasErrors() {
		return spec, xformDiags
	}
	spec.defaultXform = xform

	if baggageDiags := def.Baggage.Validate(); baggageDiags.HasErrors() {
		return spec, baggageDiags
	}

	subscriber, sDiags := cfg.SubscriberSource{
		Subscriber: def.Subscriber,
		Action:     def.Action,
		Transforms: def.Transforms,
		QueueSize:  def.QueueSize,
	}.Resolve(config, def.DefRange, "rabbitmq/"+clientName+"/"+def.Name, tp)
	if sDiags.HasErrors() {
		return spec, sDiags
	}

	// Bound how long a delivery may go unsettled, outside the async queue so the
	// clock starts when the message arrives rather than when a worker reaches
	// it. A no-op unless ack = "manual".
	subscriber = cfg.NewSettleTimeoutSubscriber(policy, subscriber,
		fmt.Sprintf("rabbitmq/%s/%s", clientName, def.Name), config.UserLogger)

	// Strip untrusted inbound baggage at this external boundary before it reaches
	// the action. Secure by default: a nil baggage block strips everything; opt
	// in with baggage { passthrough | allow | deny }.
	spec.subscriber = cfg.NewBaggageFilterSubscriber(def.Baggage, subscriber, config.Logger)

	if def.Declare != nil {
		d := rmqreceiver.Declare{
			Durable:    true,  // default
			AutoDelete: false, // default
		}
		if def.Declare.Durable != nil {
			d.Durable = *def.Declare.Durable
		}
		if def.Declare.AutoDelete != nil {
			d.AutoDelete = *def.Declare.AutoDelete
		}
		spec.declare = &d
	}

	bindings := make([]rmqreceiver.Binding, 0, len(def.Bindings))
	for _, b := range def.Bindings {
		bindings = append(bindings, rmqreceiver.Binding{
			RoutingKey: b.RoutingKey,
			Exchange:   b.Exchange,
		})
	}
	spec.bindings = bindings

	subs := make([]rmqreceiver.Subscription, 0, len(def.Subscriptions))
	for _, s := range def.Subscriptions {
		sub := rmqreceiver.Subscription{
			RoutingKeyPattern: s.RoutingKeyPattern,
		}
		if cfg.IsExpressionProvided(s.VinculumTopic) {
			sub.VinculumTopicFunc = makeVinculumTopicFunc(config, s.VinculumTopic)
		}
		subs = append(subs, sub)
	}
	spec.subscriptions = subs

	return spec, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func parseBrokers(brokers []string, defRange hcl.Range) (parsed []*url.URL, hasAmqps, hasAmqp bool, diags hcl.Diagnostics) {
	parsed = make([]*url.URL, 0, len(brokers))
	for _, raw := range brokers {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, false, false, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "rabbitmq: invalid broker URL",
				Detail:   fmt.Sprintf("%q: %v", raw, err),
				Subject:  &defRange,
			}}
		}
		switch u.Scheme {
		case "amqp":
			hasAmqp = true
		case "amqps":
			hasAmqps = true
		default:
			return nil, false, false, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "rabbitmq: invalid broker URL scheme",
				Detail:   fmt.Sprintf("%q uses scheme %q; use amqp or amqps", raw, u.Scheme),
				Subject:  &defRange,
			}}
		}
		parsed = append(parsed, u)
	}
	return parsed, hasAmqps, hasAmqp, nil
}

func urlsToStrings(urls []*url.URL) []string {
	out := make([]string, len(urls))
	for i, u := range urls {
		out[i] = u.String()
	}
	return out
}

func validateUniqueNames(def RMQClientDefinition) hcl.Diagnostics {
	seenSenders := make(map[string]struct{}, len(def.Senders))
	for _, s := range def.Senders {
		if _, dup := seenSenders[s.Name]; dup {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("rabbitmq: duplicate sender name %q", s.Name),
				Subject:  &s.DefRange,
			}}
		}
		seenSenders[s.Name] = struct{}{}
	}
	seenReceivers := make(map[string]struct{}, len(def.Receivers))
	for _, r := range def.Receivers {
		if _, dup := seenReceivers[r.Name]; dup {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("rabbitmq: duplicate receiver name %q", r.Name),
				Subject:  &r.DefRange,
			}}
		}
		seenReceivers[r.Name] = struct{}{}
	}
	return nil
}

// resolveTLS reconciles the tls block with the broker URL schemes:
//   - If any amqps:// URL and no tls block: synthesize tls{enabled=true}.
//   - If tls{enabled=false} AND any amqps:// URL: log warning, force enabled.
//   - If amqp:// URL with tls{enabled=true}: hard error.
func resolveTLS(config *cfg.Config, tlsDef *cfg.TLSConfig, hasAmqps, hasAmqp bool, defRange hcl.Range) (*tls.Config, hcl.Diagnostics) {
	if tlsDef == nil {
		if hasAmqps {
			synth := &cfg.TLSConfig{Enabled: true}
			c, err := synth.BuildTLSClientConfig(config.BaseDir)
			if err != nil {
				return nil, hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "rabbitmq: build default TLS config for amqps://",
					Detail:   err.Error(),
					Subject:  &defRange,
				}}
			}
			return c, nil
		}
		return nil, nil
	}

	if tlsDef.Enabled && hasAmqp && !hasAmqps {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "rabbitmq: tls { enabled = true } with amqp:// broker URL",
			Detail:   "tls enabled but all broker URLs use the plain amqp:// scheme; use amqps:// or remove the tls block",
			Subject:  &tlsDef.DefRange,
		}}
	}
	if !tlsDef.Enabled && hasAmqps {
		config.UserLogger.Warn("rabbitmq: tls { enabled = false } overridden because at least one broker URL uses amqps://",
			zap.String("client", "rabbitmq"))
		tlsDef.Enabled = true
	}

	if !tlsDef.Enabled {
		return nil, nil
	}
	c, err := tlsDef.BuildTLSClientConfig(config.BaseDir)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "rabbitmq: invalid TLS config",
			Detail:   err.Error(),
			Subject:  &tlsDef.DefRange,
		}}
	}
	return c, nil
}

func parseDefaultTopicTransform(s, senderName string, defRange hcl.Range) (rmqsender.DefaultTopicTransform, hcl.Diagnostics) {
	switch s {
	case "", "slash_to_dot":
		return rmqsender.DefaultTopicSlashToDot, nil
	case "verbatim":
		return rmqsender.DefaultTopicVerbatim, nil
	case "error":
		return rmqsender.DefaultTopicError, nil
	case "ignore":
		return rmqsender.DefaultTopicIgnore, nil
	default:
		return 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("rabbitmq sender %q: invalid default_topic_transform", senderName),
			Detail:   fmt.Sprintf("%q is not valid; use slash_to_dot, verbatim, error, or ignore", s),
			Subject:  &defRange,
		}}
	}
}

func parseDefaultRoutingKeyTransform(s, receiverName string, defRange hcl.Range) (rmqreceiver.DefaultRoutingKeyTransform, hcl.Diagnostics) {
	switch s {
	case "", "dot_to_slash":
		return rmqreceiver.DefaultRKDotToSlash, nil
	case "verbatim":
		return rmqreceiver.DefaultRKVerbatim, nil
	case "error":
		return rmqreceiver.DefaultRKError, nil
	case "ignore":
		return rmqreceiver.DefaultRKIgnore, nil
	default:
		return 0, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("rabbitmq receiver %q: invalid default_routing_key_transform", receiverName),
			Detail:   fmt.Sprintf("%q is not valid; use dot_to_slash, verbatim, error, or ignore", s),
			Subject:  &defRange,
		}}
	}
}

// makeRoutingKeyFunc builds the per-message HCL evaluator for a sender's
// `topic { routing_key = ... }` expression. The expression sees ctx.topic,
// ctx.msg, and ctx.fields (the latter merged with pattern-extracted captures
// by the sender package before calling this function).
func makeRoutingKeyFunc(config *cfg.Config, expr hcl.Expression) rmqsender.RoutingKeyFunc {
	return func(topic string, msg any, fields map[string]string) (string, error) {
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("rabbitmq sender: convert msg: %w", err)
		}

		ctyFields := make(map[string]cty.Value, len(fields))
		for k, v := range fields {
			ctyFields[k] = cty.StringVal(v)
		}
		evalCtx, err := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("topic", topic).
			WithAttribute("msg", ctyMsg).
			WithAttribute("fields", cty.ObjectVal(ctyFields)).
			BuildEvalContext(config.EvalCtx())
		if err != nil {
			return "", err
		}

		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return "", diags
		}
		if val.IsNull() || val.Type() != cty.String {
			return "", fmt.Errorf("rabbitmq sender: routing_key must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

// makeVinculumTopicFunc builds the per-message HCL evaluator for a receiver's
// `subscription { vinculum_topic = ... }` expression, against the shared
// `inbound-message` shape plus the delivery's AMQP identity.
func makeVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) rmqreceiver.VinculumTopicFunc {
	return func(routingKey, exchange string, fields map[string]string, msg any) (string, error) {
		ctxBuilder, err := cfg.NewInboundContext(msg, fields)
		if err != nil {
			return "", fmt.Errorf("rabbitmq receiver: %w", err)
		}

		evalCtx, err := ctxBuilder.
			WithStringAttribute("routing_key", routingKey).
			WithStringAttribute("exchange", exchange).
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
			return "", fmt.Errorf("rabbitmq receiver: vinculum_topic must return a string, got %s", val.Type().FriendlyName())
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
// expression can do arbitrary I/O.
func makeLifecycleHook(config *cfg.Config, expr hcl.Expression, notify func()) func(ctx context.Context) {
	hasExpr := cfg.IsExpressionProvided(expr)
	return func(ctx context.Context) {
		notify()
		if !hasExpr {
			return
		}
		evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(config.EvalCtx())
		if err != nil {
			config.UserLogger.Error("rabbitmq lifecycle hook: build eval context", zap.Error(err))
			return
		}
		_, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			config.UserLogger.Error("rabbitmq lifecycle hook: eval failed", config.ActionError(diags))
		}
	}
}

// Ensure interface compliance.
var (
	_ cfg.Client        = (*RMQClientWrapper)(nil)
	_ cfg.Startable     = (*RMQClientWrapper)(nil)
	_ cfg.Stoppable     = (*RMQClientWrapper)(nil)
	_ cfg.CtyValuer     = (*RMQClientWrapper)(nil)
	_ cfg.Readyable     = (*RMQClientWrapper)(nil)
	_ cfg.ReadyNotifier = (*RMQClientWrapper)(nil)
	_ bus.Subscriber    = (*RMQClientWrapper)(nil)
	_ bus.Subscriber    = (*RMQSenderProxy)(nil)
)
