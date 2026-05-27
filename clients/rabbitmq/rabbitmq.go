// Package rabbitmq implements the `client "rabbitmq"` VCL block, wiring
// vinculum-rabbitmq senders and receivers into the vinculum config pipeline.
package rabbitmq

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
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
	cfg.RegisterClientType("rabbitmq", process)
}

// ─── HCL definition structs ──────────────────────────────────────────────────

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
	Name                  string               `hcl:",label"`
	Exchange              string               `hcl:"exchange"`
	ConfirmMode           *bool                `hcl:"confirm_mode,optional"`
	Mandatory             *bool                `hcl:"mandatory,optional"`
	Persistent            *bool                `hcl:"persistent,optional"`
	Topics                []RMQTopicDefinition `hcl:"topic,block"`
	DefaultTopicTransform string               `hcl:"default_topic_transform,optional"`
	DefRange              hcl.Range            `hcl:",def_range"`
}

type RMQTopicDefinition struct {
	Pattern    string         `hcl:",label"`
	RoutingKey hcl.Expression `hcl:"routing_key,optional"`
	Exchange   string         `hcl:"exchange,optional"`
	Persistent *bool          `hcl:"persistent,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type RMQReceiverDefinition struct {
	Name                       string                      `hcl:",label"`
	Queue                      string                      `hcl:"queue"`
	Subscriber                 hcl.Expression              `hcl:"subscriber,optional"`
	Action                     hcl.Expression              `hcl:"action,optional"`
	Transforms                 hcl.Expression              `hcl:"transforms,optional"`
	QueueSize                  *int                        `hcl:"queue_size,optional"`
	Prefetch                   *int                        `hcl:"prefetch,optional"`
	Exclusive                  *bool                       `hcl:"exclusive,optional"`
	AutoAck                    *bool                       `hcl:"auto_ack,optional"`
	Declare                    *RMQQueueDeclareDefinition  `hcl:"declare,block"`
	Bindings                   []RMQBindingDefinition      `hcl:"binding,block"`
	Subscriptions              []RMQSubscriptionDefinition `hcl:"subscription,block"`
	DefaultRoutingKeyTransform string                      `hcl:"default_routing_key_transform,optional"`
	DefRange                   hcl.Range                   `hcl:",def_range"`
}

type RMQQueueDeclareDefinition struct {
	Durable    *bool     `hcl:"durable,optional"`
	AutoDelete *bool     `hcl:"auto_delete,optional"`
	DefRange   hcl.Range `hcl:",def_range"`
}

type RMQBindingDefinition struct {
	RoutingKey string    `hcl:",label"`
	Exchange   string    `hcl:"exchange"`
	DefRange   hcl.Range `hcl:",def_range"`
}

type RMQSubscriptionDefinition struct {
	RoutingKeyPattern string         `hcl:",label"`
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
	name              string
	queue             string
	subscriber        bus.Subscriber
	subscriptions     []rmqreceiver.Subscription
	defaultXform      rmqreceiver.DefaultRoutingKeyTransform
	prefetch          int
	exclusive         bool
	autoAck           bool
	declare           *rmqreceiver.Declare
	bindings          []rmqreceiver.Binding
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
			WithAutoAck(spec.autoAck).
			WithWireFormat(c.wireFormat).
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

	if err := cli.Start(context.Background()); err != nil {
		return err
	}

	c.mu.Lock()
	c.client = cli
	c.senders = senders
	c.mu.Unlock()
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

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := RMQClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
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

	reconnectFn, recDiags := buildReconnectBackoffFunc(config, def.Reconnect)
	if recDiags.HasErrors() {
		return nil, recDiags
	}

	onConnect := makeLifecycleHook(config, def.OnConnect)
	onDisconnect := makeLifecycleHook(config, def.OnDisconnect)

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

	wrapper := &RMQClientWrapper{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		clientCfg: rmqclient.Config{
			ClientName:        clientName,
			Brokers:           urlsToStrings(parsedURLs),
			Username:          username,
			Password:          password,
			Heartbeat:         heartbeat,
			ConnectionTimeout: connTimeout,
			TLSClientConfig:   tlsCfg,
			Logger:            config.Logger,
			OnConnect:         onConnect,
			OnDisconnect:      onDisconnect,
			ReconnectBackoff:  reconnectFn,
			MeterProvider:     mp,
		},
		senderSpecs:    senderSpecs,
		receiverSpecs:  receiverSpecs,
		senderProxies:  senderProxies,
		wireFormat:     ctyWF,
		meterProvider:  mp,
		tracerProvider: tracerProvider,
		logger:         config.Logger,
	}

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
	}

	if def.Prefetch != nil {
		spec.prefetch = *def.Prefetch
	}
	if def.Exclusive != nil {
		spec.exclusive = *def.Exclusive
	}
	if def.AutoAck != nil {
		spec.autoAck = *def.AutoAck
	}

	xform, xformDiags := parseDefaultRoutingKeyTransform(def.DefaultRoutingKeyTransform, def.Name, def.DefRange)
	if xformDiags.HasErrors() {
		return spec, xformDiags
	}
	spec.defaultXform = xform

	subscriber, sDiags := cfg.SubscriberSource{
		Subscriber: def.Subscriber,
		Action:     def.Action,
		Transforms: def.Transforms,
		QueueSize:  def.QueueSize,
	}.Resolve(config, def.DefRange, "rabbitmq/"+clientName+"/"+def.Name, tp)
	if sDiags.HasErrors() {
		return spec, sDiags
	}
	spec.subscriber = subscriber

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
// `subscription { vinculum_topic = ... }` expression. The expression sees
// ctx.routing_key, ctx.exchange, ctx.topic (alias for routing_key), ctx.msg,
// and ctx.fields.
func makeVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) rmqreceiver.VinculumTopicFunc {
	return func(routingKey, exchange string, fields map[string]string, msg any) (string, error) {
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("rabbitmq receiver: convert msg: %w", err)
		}

		ctyFields := make(map[string]cty.Value, len(fields))
		for k, v := range fields {
			ctyFields[k] = cty.StringVal(v)
		}
		evalCtx, err := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("routing_key", routingKey).
			WithStringAttribute("exchange", exchange).
			WithStringAttribute("topic", routingKey).
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
		if val.IsNull() {
			return "", nil
		}
		if val.Type() != cty.String {
			return "", fmt.Errorf("rabbitmq receiver: vinculum_topic must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

func makeLifecycleHook(config *cfg.Config, expr hcl.Expression) func(ctx context.Context) {
	if !cfg.IsExpressionProvided(expr) {
		return nil
	}
	return func(ctx context.Context) {
		evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(config.EvalCtx())
		if err != nil {
			config.UserLogger.Error("rabbitmq lifecycle hook: build eval context", zap.Error(err))
			return
		}
		_, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			config.UserLogger.Error("rabbitmq lifecycle hook: eval failed", zap.Error(diags))
		}
	}
}

// buildReconnectBackoffFunc parses a ReconnectDefinition into an exponential
// backoff function for the client's reconnect loop. Returns nil when no
// reconnect block is configured, in which case the client uses its own
// default backoff.
func buildReconnectBackoffFunc(config *cfg.Config, def *cfg.ReconnectDefinition) (func(int) time.Duration, hcl.Diagnostics) {
	if def == nil {
		return nil, nil
	}

	initialDelay := time.Second
	maxDelay := 60 * time.Second
	backoffFactor := 2.0

	if cfg.IsExpressionProvided(def.InitialDelay) {
		d, diags := config.ParseDuration(def.InitialDelay)
		if diags.HasErrors() {
			return nil, diags
		}
		initialDelay = d
	}
	if cfg.IsExpressionProvided(def.MaxDelay) {
		d, diags := config.ParseDuration(def.MaxDelay)
		if diags.HasErrors() {
			return nil, diags
		}
		maxDelay = d
	}
	if def.BackoffFactor != nil {
		backoffFactor = *def.BackoffFactor
	}

	return func(attempt int) time.Duration {
		delay := time.Duration(float64(initialDelay) * math.Pow(backoffFactor, float64(attempt)))
		if delay > maxDelay {
			delay = maxDelay
		}
		return delay
	}, nil
}

// Ensure interface compliance.
var (
	_ cfg.Client     = (*RMQClientWrapper)(nil)
	_ cfg.Startable  = (*RMQClientWrapper)(nil)
	_ cfg.Stoppable  = (*RMQClientWrapper)(nil)
	_ cfg.CtyValuer  = (*RMQClientWrapper)(nil)
	_ bus.Subscriber = (*RMQClientWrapper)(nil)
	_ bus.Subscriber = (*RMQSenderProxy)(nil)
)
