package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/o11y"
	mqttclient "github.com/tsarna/vinculum-mqtt/client"
	mqttpublisher "github.com/tsarna/vinculum-mqtt/publisher"
	mqttsubscriber "github.com/tsarna/vinculum-mqtt/subscriber"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterClientType("mqtt", process)
}

// ─── HCL definition structs ──────────────────────────────────────────────────

type MQTTClientDefinition struct {
	Brokers               []string               `hcl:"brokers"`
	ClientID              hcl.Expression         `hcl:"client_id,optional"`
	KeepAlive             hcl.Expression         `hcl:"keep_alive,optional"`
	CleanStart            *bool                  `hcl:"clean_start,optional"`
	SessionExpiryInterval hcl.Expression         `hcl:"session_expiry_interval,optional"`
	TLS                   *cfg.TLSConfig         `hcl:"tls,block"`
	Auth                  *MQTTAuthDefinition    `hcl:"auth,block"`
	Reconnect             *cfg.ReconnectDefinition `hcl:"reconnect,block"`
	Will                  *MQTTWillDefinition    `hcl:"will,block"`
	OnConnect             hcl.Expression         `hcl:"on_connect,optional"`
	OnDisconnect          hcl.Expression         `hcl:"on_disconnect,optional"`
	Publishers            []MQTTPublisherDef     `hcl:"sender,block"`
	Subscribers           []MQTTSubscriberDef    `hcl:"receiver,block"`
	Metrics               hcl.Expression         `hcl:"metrics,optional"`
	DefRange              hcl.Range              `hcl:",def_range"`
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
	Name                  string                `hcl:",label"`
	QoS                   *int                  `hcl:"qos,optional"`
	Retain                *bool                 `hcl:"retain,optional"`
	TopicMappings         []MQTTTopicMappingDef `hcl:"topic,block"`
	DefaultTopicTransform string                `hcl:"default_topic_transform,optional"`
	DefRange              hcl.Range             `hcl:",def_range"`
}

type MQTTTopicMappingDef struct {
	Pattern   string         `hcl:",label"`
	MQTTTopic hcl.Expression `hcl:"mqtt_topic,optional"`
	QoS       *int           `hcl:"qos,optional"`
	Retain    *bool          `hcl:"retain,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type MQTTSubscriberDef struct {
	Name           string                     `hcl:",label"`
	Subscriber     hcl.Expression             `hcl:"subscriber,optional"`
	Action         hcl.Expression             `hcl:"action,optional"`
	QoS            *int                       `hcl:"qos,optional"`
	HandleRetained *bool                      `hcl:"handle_retained,optional"`
	SharedGroup    string                     `hcl:"shared_group,optional"`
	Subscriptions  []MQTTTopicSubscriptionDef `hcl:"subscription,block"`
	DefRange       hcl.Range                  `hcl:",def_range"`
}

type MQTTTopicSubscriptionDef struct {
	MQTTTopic     string         `hcl:",label"`
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

// MQTTClientWrapper manages an MQTTClient lifecycle and implements bus.Subscriber
// by dispatching OnEvent to all publishers.
type MQTTClientWrapper struct {
	cfg.BaseClient
	bus.BaseSubscriber

	clientCfg        mqttclient.ClientConfig
	pubSpecs         []builtMQTTPublisherSpec
	subSpecs         []builtMQTTSubscriberSpec
	publisherProxies map[string]*MQTTPublisherProxy
	metricsProvider  o11y.MetricsProvider
	logger           *zap.Logger

	mu         sync.RWMutex
	mqttClient *mqttclient.MQTTClient
	publishers []*mqttpublisher.MQTTPublisher
	connCancel context.CancelFunc
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
			WithDefaultQoS(spec.defaultQoS).
			WithDefaultRetain(spec.defaultRetain).
			WithDefaultTransform(spec.defaultXform).
			WithMetricsProvider(c.metricsProvider).
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
			WithSubscriber(spec.subscriber).
			WithHandleRetained(spec.handleRetained).
			WithSharedGroup(spec.sharedGroup).
			WithMetricsProvider(c.metricsProvider).
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
	if startErr := mqttCl.Start(connCtx); startErr != nil {
		connCancel()
		return fmt.Errorf("mqtt client %q: %w", c.Name, startErr)
	}

	c.mu.Lock()
	c.mqttClient = mqttCl
	c.publishers = publishers
	c.connCancel = connCancel
	c.mu.Unlock()

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

// ─── Config processing ────────────────────────────────────────────────────────

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := MQTTClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}

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

	reconnectFn, reconnDiags := buildReconnectBackoffFunc(config, def.Reconnect)
	if reconnDiags.HasErrors() {
		return nil, reconnDiags
	}

	onConnect := makeLifecycleHook(config, def.OnConnect)
	onDisconnect := makeLifecycleHook(config, def.OnDisconnect)

	metricsProvider, metricsDiags := cfg.ResolveMetricsProvider(config, def.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}

	clientCfg := mqttclient.ClientConfig{
		ServerURLs:            serverURLs,
		ClientID:              clientID,
		KeepAlive:             keepAlive,
		CleanStart:            cleanStart,
		SessionExpiryInterval: sessionExpiry,
		TLSConfig:             tlsCfg,
		Username:              username,
		Password:              password,
		WillMessage:           willCfg,
		ReconnectBackoffFunc:  reconnectFn,
		OnConnect:             onConnect,
		OnDisconnect:          onDisconnect,
		MetricsProvider:       metricsProvider,
		Logger:                config.Logger,
	}

	pubSpecs, pubDiags := buildPublisherSpecs(config, def.Publishers)
	if pubDiags.HasErrors() {
		return nil, pubDiags
	}

	subSpecs, subDiags := buildSubscriberSpecs(config, clientName, def.Subscribers)
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

	wrapper := &MQTTClientWrapper{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		clientCfg:        clientCfg,
		pubSpecs:         pubSpecs,
		subSpecs:         subSpecs,
		publisherProxies: publisherProxies,
		metricsProvider:  metricsProvider,
		logger:           config.Logger,
	}

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

func buildSubscriberSpecs(config *cfg.Config, clientName string, defs []MQTTSubscriberDef) ([]builtMQTTSubscriberSpec, hcl.Diagnostics) {
	specs := make([]builtMQTTSubscriberSpec, 0, len(defs))
	for _, def := range defs {
		spec, diags := buildSubscriberSpec(config, clientName, def)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildSubscriberSpec(config *cfg.Config, clientName string, def MQTTSubscriberDef) (builtMQTTSubscriberSpec, hcl.Diagnostics) {
	spec := builtMQTTSubscriberSpec{
		name:           def.Name,
		handleRetained: true,
		sharedGroup:    def.SharedGroup,
	}
	if def.HandleRetained != nil {
		spec.handleRetained = *def.HandleRetained
	}

	hasSubscriber := cfg.IsExpressionProvided(def.Subscriber)
	hasAction := cfg.IsExpressionProvided(def.Action)
	if hasSubscriber == hasAction {
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("mqtt receiver %q: exactly one of subscriber or action must be specified", def.Name),
			Subject:  &def.DefRange,
		}}
	}

	if hasSubscriber {
		sub, diags := cfg.GetSubscriberFromExpression(config, def.Subscriber)
		if diags.HasErrors() {
			return spec, diags
		}
		spec.subscriber = sub
	} else {
		spec.subscriber = cfg.NewActionSubscriber(config, def.Action)
	}

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

		ctxBuilder := cfg.NewContext(context.Background()).
			WithStringAttribute("topic", topic).
			WithAttribute("msg", ctyMsg)

		if len(fields) > 0 {
			ctyFields := make(map[string]cty.Value, len(fields))
			for k, v := range fields {
				ctyFields[k] = cty.StringVal(v)
			}
			ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
		}

		evalCtx, diags := ctxBuilder.BuildEvalContext(config.EvalCtx())
		if diags.HasErrors() {
			return "", diags
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

func makeMQTTVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) mqttsubscriber.VinculumTopicFunc {
	return func(mqttTopic string, fields map[string]string, msg any) (string, error) {
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("mqtt subscriber: convert msg: %w", err)
		}

		ctxBuilder := cfg.NewContext(context.Background()).
			WithStringAttribute("topic", mqttTopic).
			WithAttribute("msg", ctyMsg)

		if len(fields) > 0 {
			ctyFields := make(map[string]cty.Value, len(fields))
			for k, v := range fields {
				ctyFields[k] = cty.StringVal(v)
			}
			ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
		}

		evalCtx, diags := ctxBuilder.BuildEvalContext(config.EvalCtx())
		if diags.HasErrors() {
			return "", diags
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

func makeLifecycleHook(config *cfg.Config, expr hcl.Expression) func(ctx context.Context) {
	if !cfg.IsExpressionProvided(expr) {
		return nil
	}
	return func(ctx context.Context) {
		evalCtx, diags := cfg.NewContext(ctx).BuildEvalContext(config.EvalCtx())
		if diags.HasErrors() {
			config.Logger.Error("mqtt lifecycle hook: build eval context", zap.Error(diags))
			return
		}
		_, diags = expr.Value(evalCtx)
		if diags.HasErrors() {
			config.Logger.Error("mqtt lifecycle hook: eval failed", zap.Error(diags))
		}
	}
}

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
