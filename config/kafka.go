package config

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/o11y"
	kconsumer "github.com/tsarna/vinculum-kafka/consumer"
	kproducer "github.com/tsarna/vinculum-kafka/producer"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// ─── HCL definition structs ──────────────────────────────────────────────────

// TopicMappingDefinition is one topic_mapping block inside a producer block.
type TopicMappingDefinition struct {
	Pattern    string         `hcl:"pattern"`
	KafkaTopic string         `hcl:"kafka_topic"`
	Key        hcl.Expression `hcl:"key,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

// ProducerDefinition is one producer block inside a client "kafka" block.
type ProducerDefinition struct {
	Name                  string                   `hcl:",label"`
	ProduceMode           string                   `hcl:"produce_mode,optional"`
	TopicMappings         []TopicMappingDefinition `hcl:"topic_mapping,block"`
	DefaultTopicTransform string                   `hcl:"default_topic_transform,optional"`
	DefRange              hcl.Range                `hcl:",def_range"`
}

// SASLDefinition is the optional sasl block inside a client "kafka" block.
type SASLDefinition struct {
	Mechanism string         `hcl:"mechanism"`
	Username  string         `hcl:"username,optional"`
	Password  hcl.Expression `hcl:"password,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

// TopicSubscriptionDefinition is one topic_subscription block inside a consumer block.
type TopicSubscriptionDefinition struct {
	KafkaTopic    string         `hcl:"kafka_topic"`
	VinculumTopic hcl.Expression `hcl:"vinculum_topic"`
	DefRange      hcl.Range      `hcl:",def_range"`
}

// ConsumerDefinition is one consumer block inside a client "kafka" block.
type ConsumerDefinition struct {
	Name          string                        `hcl:",label"`
	GroupID       string                        `hcl:"group_id"`
	StartOffset   string                        `hcl:"start_offset,optional"`
	Target        hcl.Expression                `hcl:"target"`
	CommitMode    string                        `hcl:"commit_mode,optional"`
	DLQTopic      string                        `hcl:"dlq_topic,optional"`
	Subscriptions []TopicSubscriptionDefinition `hcl:"topic_subscription,block"`
	DefRange      hcl.Range                     `hcl:",def_range"`
}

// KafkaClientDefinition holds the decoded HCL for a client "kafka" block.
type KafkaClientDefinition struct {
	Brokers        []string             `hcl:"brokers"`
	TLS            *TLSConfig           `hcl:"tls,block"`
	SASL           *SASLDefinition      `hcl:"sasl,block"`
	Producers      []ProducerDefinition `hcl:"producer,block"`
	Consumers      []ConsumerDefinition `hcl:"consumer,block"`
	// Producer client-level settings (apply to the shared kgo.Client)
	Acks           string         `hcl:"acks,optional"`
	Compression    string         `hcl:"compression,optional"`
	Idempotent     *bool          `hcl:"idempotent,optional"`
	Linger         hcl.Expression `hcl:"linger,optional"`
	MaxRecords     *int           `hcl:"max_records,optional"`
	// Connection timeouts
	DialTimeout    hcl.Expression `hcl:"dial_timeout,optional"`
	RequestTimeout hcl.Expression `hcl:"request_timeout,optional"`
	MetadataMaxAge hcl.Expression `hcl:"metadata_max_age,optional"`
	// Observability
	Metrics        hcl.Expression `hcl:"metrics,optional"`
	DefRange       hcl.Range      `hcl:",def_range"`
}

// ─── Runtime struct ───────────────────────────────────────────────────────────

// builtProducerSpec holds what is known at config time about one producer,
// ready to be handed to kproducer.ProducerBuilder in Start().
type builtProducerSpec struct {
	name          string
	topicMappings []kproducer.TopicMapping
	produceMode   kproducer.ProduceMode
	defaultXform  kproducer.DefaultTopicTransform
}

// builtConsumerSpec holds what is known at config time about one consumer,
// ready to be handed to kconsumer.ConsumerBuilder in Start().
type builtConsumerSpec struct {
	name          string
	groupID       string
	startOffset   kgo.Offset
	commitMode    kconsumer.CommitMode
	dlqTopic      string
	subscriptions []kconsumer.TopicSubscription
	target        bus.Subscriber
}

// KafkaProducerProxy is a config-time bus.Subscriber that forwards OnEvent to
// a named KafkaProducer. The actual producer is wired in KafkaClient.Start().
type KafkaProducerProxy struct {
	bus.BaseSubscriber
	mu           sync.RWMutex
	producer     *kproducer.KafkaProducer
	clientName   string
	producerName string
}

func (p *KafkaProducerProxy) wireProducer(prod *kproducer.KafkaProducer) {
	p.mu.Lock()
	p.producer = prod
	p.mu.Unlock()
}

func (p *KafkaProducerProxy) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	p.mu.RLock()
	prod := p.producer
	p.mu.RUnlock()
	if prod == nil {
		return fmt.Errorf("kafka client %q producer %q: not yet started", p.clientName, p.producerName)
	}
	return prod.OnEvent(ctx, topic, msg, fields)
}

// KafkaClient manages a franz-go producer client, zero or more KafkaProducers,
// and zero or more KafkaConsumers. It implements bus.Subscriber by dispatching
// OnEvent to all producers. Implements config.Startable and config.Stoppable.
type KafkaClient struct {
	BaseClient
	bus.BaseSubscriber

	kgoOpts         []kgo.Opt
	prodSpecs       []builtProducerSpec
	consSpecs       []builtConsumerSpec
	producerProxies map[string]*KafkaProducerProxy // created at config time, wired at Start()
	metricsProvider o11y.MetricsProvider
	logger          *zap.Logger

	mu         sync.RWMutex
	kgoClient  *kgo.Client
	producers  []*kproducer.KafkaProducer
	consumers  []*kconsumer.KafkaConsumer
	consCancel context.CancelFunc
}

// CtyValue returns the cty value used to expose this client in VCL expressions.
//
// For clients with producers the value is an object with:
//   - "producers" — a subscriber capsule wrapping this KafkaClient; dispatches
//     OnEvent to all producers (client.events.producers)
//   - "producer"  — an object mapping each producer name to its own subscriber
//     capsule (client.events.producer.name)
//
// For consumer-only clients (no producers) the value falls back to a plain
// ClientCapsule since there is nothing producer-related to expose.
func (c *KafkaClient) CtyValue() cty.Value {
	if len(c.producerProxies) == 0 {
		return NewClientCapsule(c)
	}
	prodMap := make(map[string]cty.Value, len(c.producerProxies))
	for name, proxy := range c.producerProxies {
		prodMap[name] = NewSubscriberCapsule(proxy)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"producers": NewSubscriberCapsule(c),
		"producer":  cty.ObjectVal(prodMap),
	})
}

// Start creates producer and consumer clients and starts all consumers.
// It is called by the vinculum lifecycle manager before events begin to flow.
func (c *KafkaClient) Start() error {
	// ── Producer client (only when producers are configured) ──────────────
	var kgoClient *kgo.Client
	if len(c.prodSpecs) > 0 {
		client, err := kgo.NewClient(c.kgoOpts...)
		if err != nil {
			return fmt.Errorf("kafka client %q: create producer client: %w", c.Name, err)
		}
		kgoClient = client
	}

	producers := make([]*kproducer.KafkaProducer, 0, len(c.prodSpecs))
	for _, spec := range c.prodSpecs {
		b := kproducer.NewProducer().
			WithClient(kgoClient).
			WithProduceMode(spec.produceMode).
			WithDefaultTransform(spec.defaultXform).
			WithMetricsProvider(c.metricsProvider).
			WithLogger(c.logger)
		for _, tm := range spec.topicMappings {
			b = b.WithTopicMapping(tm)
		}
		p, err := b.Build()
		if err != nil {
			kgoClient.Close()
			return fmt.Errorf("kafka client %q producer %q: %w", c.Name, spec.name, err)
		}
		if proxy, ok := c.producerProxies[spec.name]; ok {
			proxy.wireProducer(p)
		}
		producers = append(producers, p)
	}

	// ── Consumer clients (one per spec — each needs its own group/client) ─
	consumerCtx, consCancel := context.WithCancel(context.Background())
	consumers := make([]*kconsumer.KafkaConsumer, 0, len(c.consSpecs))

	for _, spec := range c.consSpecs {
		b := kconsumer.NewConsumer().
			WithBaseOpts(c.kgoOpts).
			WithGroupID(spec.groupID).
			WithStartOffset(spec.startOffset).
			WithCommitMode(spec.commitMode).
			WithDLQTopic(spec.dlqTopic).
			WithTarget(spec.target).
			WithMetricsProvider(c.metricsProvider).
			WithLogger(c.logger)
		for _, sub := range spec.subscriptions {
			b = b.WithSubscription(sub)
		}
		cons, err := b.Build()
		if err != nil {
			consCancel()
			for _, c2 := range consumers {
				c2.Stop()
			}
			if kgoClient != nil {
				kgoClient.Close()
			}
			return fmt.Errorf("kafka client %q consumer %q: %w", c.Name, spec.name, err)
		}
		if err := cons.Start(consumerCtx); err != nil {
			consCancel()
			for _, c2 := range consumers {
				c2.Stop()
			}
			if kgoClient != nil {
				kgoClient.Close()
			}
			return fmt.Errorf("kafka client %q consumer %q start: %w", c.Name, spec.name, err)
		}
		consumers = append(consumers, cons)
	}

	c.mu.Lock()
	c.kgoClient = kgoClient
	c.producers = producers
	c.consumers = consumers
	c.consCancel = consCancel
	c.mu.Unlock()

	return nil
}

// Stop implements config.Stoppable. Stops all consumers, then flushes and
// closes the producer client. Called on graceful shutdown.
func (c *KafkaClient) Stop() error {
	c.mu.RLock()
	kgoClient := c.kgoClient
	consumers := c.consumers
	consCancel := c.consCancel
	c.mu.RUnlock()

	// Stop consumers first — each owns its own kgo.Client.
	if consCancel != nil {
		consCancel()
	}
	for _, cons := range consumers {
		cons.Stop()
	}

	// Flush and close the producer client.
	if kgoClient != nil {
		if err := kgoClient.Flush(context.Background()); err != nil {
			c.logger.Error("kafka: flush on shutdown failed", zap.String("client", c.Name), zap.Error(err))
		}
		kgoClient.Close()
	}

	return nil
}

// OnEvent implements bus.Subscriber. It dispatches to all producers; each
// producer applies its own topic_mapping rules (first match wins per producer).
func (c *KafkaClient) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	if len(c.prodSpecs) == 0 {
		return fmt.Errorf("kafka client %q: no producers configured", c.Name)
	}

	c.mu.RLock()
	producers := c.producers
	c.mu.RUnlock()

	if len(producers) == 0 {
		return fmt.Errorf("kafka client %q: not yet started", c.Name)
	}

	var errs []error
	for _, p := range producers {
		if err := p.OnEvent(ctx, topic, msg, fields); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) > 1 {
		return fmt.Errorf("kafka client %q: multiple produce errors: %v", c.Name, errs)
	}
	return nil
}

// ─── Config processing ────────────────────────────────────────────────────────

// ProcessKafkaClientBlock decodes and validates a client "kafka" block and
// returns a KafkaClient ready to be started.
func ProcessKafkaClientBlock(config *Config, block *hcl.Block, remainingBody hcl.Body) (Client, hcl.Diagnostics) {
	def := KafkaClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.evalCtx, &def)
	if diags.HasErrors() {
		return nil, diags
	}

	if len(def.Brokers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "kafka: brokers is required",
			Subject:  &def.DefRange,
		}}
	}

	if len(def.Producers) == 0 && len(def.Consumers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "kafka: at least one producer or consumer block is required",
			Subject:  &def.DefRange,
		}}
	}

	// Validate unique producer names
	seenProducers := make(map[string]struct{}, len(def.Producers))
	for _, p := range def.Producers {
		if _, dup := seenProducers[p.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("kafka: duplicate producer name %q", p.Name),
				Subject:  &p.DefRange,
			}}
		}
		seenProducers[p.Name] = struct{}{}
	}

	// Validate unique consumer names
	seenConsumers := make(map[string]struct{}, len(def.Consumers))
	for _, c := range def.Consumers {
		if _, dup := seenConsumers[c.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("kafka: duplicate consumer name %q", c.Name),
				Subject:  &c.DefRange,
			}}
		}
		seenConsumers[c.Name] = struct{}{}
	}

	// ── kgo.Client options (connection-level) ──────────────────────────────

	opts := []kgo.Opt{
		kgo.SeedBrokers(def.Brokers...),
	}

	// TLS
	if def.TLS != nil && def.TLS.Enabled {
		tlsCfg, err := def.TLS.BuildTLSClientConfig(config.BaseDir)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "kafka: invalid TLS config",
				Detail:   err.Error(),
				Subject:  &def.TLS.DefRange,
			}}
		}
		if tlsCfg != nil {
			opts = append(opts, kgo.DialTLSConfig(tlsCfg))
		}
	}

	// SASL
	if def.SASL != nil {
		saslOpt, saslDiags := buildSASLOpt(config, def.SASL)
		if saslDiags.HasErrors() {
			return nil, saslDiags
		}
		opts = append(opts, saslOpt)
	}

	// Timeouts
	if IsExpressionProvided(def.DialTimeout) {
		d, ddiags := config.ParseDuration(def.DialTimeout)
		if ddiags.HasErrors() {
			return nil, ddiags
		}
		opts = append(opts, kgo.DialTimeout(d))
	}

	if IsExpressionProvided(def.RequestTimeout) {
		d, ddiags := config.ParseDuration(def.RequestTimeout)
		if ddiags.HasErrors() {
			return nil, ddiags
		}
		opts = append(opts, kgo.RequestTimeoutOverhead(d))
	}

	if IsExpressionProvided(def.MetadataMaxAge) {
		d, ddiags := config.ParseDuration(def.MetadataMaxAge)
		if ddiags.HasErrors() {
			return nil, ddiags
		}
		opts = append(opts, kgo.MetadataMaxAge(d))
	}

	// ── Producer client-level options (only meaningful when producers present) ─

	if len(def.Producers) > 0 {
		acksOpt, err := parseAcks(def.Acks, def.Idempotent)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "kafka: invalid acks",
				Detail:   err.Error(),
				Subject:  &def.DefRange,
			}}
		}
		opts = append(opts, acksOpt)

		// idempotent = false explicitly disables the idempotent producer.
		// By default franz-go enables it when acks = "all".
		if def.Idempotent != nil && !*def.Idempotent {
			opts = append(opts, kgo.DisableIdempotentWrite())
		}

		compOpt, err := parseCompression(def.Compression)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "kafka: invalid compression",
				Detail:   err.Error(),
				Subject:  &def.DefRange,
			}}
		}
		opts = append(opts, compOpt)

		if IsExpressionProvided(def.Linger) {
			d, ddiags := config.ParseDuration(def.Linger)
			if ddiags.HasErrors() {
				return nil, ddiags
			}
			opts = append(opts, kgo.ProducerLinger(d))
		}

		if def.MaxRecords != nil {
			opts = append(opts, kgo.MaxBufferedRecords(*def.MaxRecords))
		}
	}

	// ── Per-producer specs (topic mappings, produce mode) ──────────────────

	prodSpecs, prodDiags := buildProducerSpecs(config, def.Producers)
	if prodDiags.HasErrors() {
		return nil, prodDiags
	}

	// ── Per-consumer specs ─────────────────────────────────────────────────

	consSpecs, consDiags := buildConsumerSpecs(config, def.Consumers)
	if consDiags.HasErrors() {
		return nil, consDiags
	}

	// ── Per-producer proxies (subscriber capsules for client.events.producer.name) ─

	producerProxies := make(map[string]*KafkaProducerProxy, len(prodSpecs))
	for _, spec := range prodSpecs {
		producerProxies[spec.name] = &KafkaProducerProxy{
			clientName:   block.Labels[1],
			producerName: spec.name,
		}
	}

	metricsProvider, metricsDiags := ResolveMetricsProvider(config, def.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}

	client := &KafkaClient{
		BaseClient: BaseClient{
			Name:     block.Labels[1],
			DefRange: def.DefRange,
		},
		kgoOpts:         opts,
		prodSpecs:       prodSpecs,
		consSpecs:       consSpecs,
		producerProxies: producerProxies,
		metricsProvider: metricsProvider,
		logger:          config.Logger,
	}

	config.Startables = append(config.Startables, client)
	config.Stoppables = append(config.Stoppables, client)

	return client, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func buildSASLOpt(config *Config, def *SASLDefinition) (kgo.Opt, hcl.Diagnostics) {
	var password string
	if IsExpressionProvided(def.Password) {
		val, diags := def.Password.Value(config.evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}
		if val.IsNull() || val.Type() != cty.String {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "kafka sasl: password must be a string",
				Subject:  &def.DefRange,
			}}
		}
		password = val.AsString()
	}

	switch def.Mechanism {
	case "PLAIN":
		return kgo.SASL(plain.Auth{User: def.Username, Pass: password}.AsMechanism()), nil
	case "SCRAM-SHA-256":
		return kgo.SASL(scram.Auth{User: def.Username, Pass: password}.AsSha256Mechanism()), nil
	case "SCRAM-SHA-512":
		return kgo.SASL(scram.Auth{User: def.Username, Pass: password}.AsSha512Mechanism()), nil
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "kafka sasl: unsupported mechanism",
			Detail:   fmt.Sprintf("%q is not supported; use PLAIN, SCRAM-SHA-256, or SCRAM-SHA-512", def.Mechanism),
			Subject:  &def.DefRange,
		}}
	}
}

func parseAcks(acks string, idempotent *bool) (kgo.Opt, error) {
	switch acks {
	case "", "all":
		return kgo.RequiredAcks(kgo.AllISRAcks()), nil
	case "leader":
		return kgo.RequiredAcks(kgo.LeaderAck()), nil
	case "none":
		return kgo.RequiredAcks(kgo.NoAck()), nil
	default:
		return nil, fmt.Errorf("%q is not valid; use all, leader, or none", acks)
	}
}

func parseCompression(comp string) (kgo.Opt, error) {
	switch comp {
	case "", "none":
		return kgo.ProducerBatchCompression(kgo.NoCompression()), nil
	case "gzip":
		return kgo.ProducerBatchCompression(kgo.GzipCompression()), nil
	case "snappy":
		return kgo.ProducerBatchCompression(kgo.SnappyCompression()), nil
	case "lz4":
		return kgo.ProducerBatchCompression(kgo.Lz4Compression()), nil
	case "zstd":
		return kgo.ProducerBatchCompression(kgo.ZstdCompression()), nil
	default:
		return nil, fmt.Errorf("%q is not valid; use none, gzip, snappy, lz4, or zstd", comp)
	}
}

func buildProducerSpecs(config *Config, defs []ProducerDefinition) ([]builtProducerSpec, hcl.Diagnostics) {
	specs := make([]builtProducerSpec, 0, len(defs))
	for _, def := range defs {
		spec, diags := buildProducerSpec(config, def)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildProducerSpec(config *Config, def ProducerDefinition) (builtProducerSpec, hcl.Diagnostics) {
	var spec builtProducerSpec
	spec.name = def.Name

	switch def.ProduceMode {
	case "", "sync":
		spec.produceMode = kproducer.ProduceModeSync
	case "async":
		spec.produceMode = kproducer.ProduceModeAsync
	default:
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka producer %q: invalid produce_mode", def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use sync or async", def.ProduceMode),
			Subject:  &def.DefRange,
		}}
	}

	switch def.DefaultTopicTransform {
	case "", "error":
		spec.defaultXform = kproducer.DefaultTopicError
	case "slash_to_dot":
		spec.defaultXform = kproducer.DefaultTopicSlashToDot
	case "ignore":
		spec.defaultXform = kproducer.DefaultTopicIgnore
	default:
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka producer %q: invalid default_topic_transform", def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use error, slash_to_dot, or ignore", def.DefaultTopicTransform),
			Subject:  &def.DefRange,
		}}
	}

	mappings := make([]kproducer.TopicMapping, 0, len(def.TopicMappings))
	for _, tmDef := range def.TopicMappings {
		tm := kproducer.TopicMapping{
			Pattern:    tmDef.Pattern,
			KafkaTopic: tmDef.KafkaTopic,
		}
		if IsExpressionProvided(tmDef.Key) {
			// Peek at the value: if it's a static null, no key; otherwise wrap in KeyFunc.
			staticVal, isStatic := IsConstantExpression(tmDef.Key)
			if isStatic && staticVal.IsNull() {
				// explicit null — no key (round-robin partitioning)
				tm.KeyFunc = nil
			} else {
				tm.KeyFunc = makeKafkaKeyFunc(config, tmDef.Key)
			}
		}
		mappings = append(mappings, tm)
	}
	spec.topicMappings = mappings

	return spec, nil
}

func buildConsumerSpecs(config *Config, defs []ConsumerDefinition) ([]builtConsumerSpec, hcl.Diagnostics) {
	specs := make([]builtConsumerSpec, 0, len(defs))
	for _, def := range defs {
		spec, diags := buildConsumerSpec(config, def)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildConsumerSpec(config *Config, def ConsumerDefinition) (builtConsumerSpec, hcl.Diagnostics) {
	var spec builtConsumerSpec
	spec.name = def.Name

	if def.GroupID == "" {
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka consumer %q: group_id is required", def.Name),
			Subject:  &def.DefRange,
		}}
	}
	spec.groupID = def.GroupID

	switch def.StartOffset {
	case "", "stored":
		spec.startOffset = kgo.NewOffset()
	case "earliest":
		spec.startOffset = kgo.NewOffset().AtStart()
	case "latest":
		spec.startOffset = kgo.NewOffset().AtEnd()
	default:
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka consumer %q: invalid start_offset", def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use stored, earliest, or latest", def.StartOffset),
			Subject:  &def.DefRange,
		}}
	}

	switch def.CommitMode {
	case "", "after_process":
		spec.commitMode = kconsumer.CommitAfterProcess
	case "periodic":
		spec.commitMode = kconsumer.CommitPeriodic
	case "manual":
		spec.commitMode = kconsumer.CommitManual
	default:
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka consumer %q: invalid commit_mode", def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use after_process, periodic, or manual", def.CommitMode),
			Subject:  &def.DefRange,
		}}
	}

	spec.dlqTopic = def.DLQTopic

	target, diags := GetSubscriberFromExpression(config, def.Target)
	if diags.HasErrors() {
		return spec, diags
	}
	spec.target = target

	if len(def.Subscriptions) == 0 {
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka consumer %q: at least one topic_subscription block is required", def.Name),
			Subject:  &def.DefRange,
		}}
	}

	subs := make([]kconsumer.TopicSubscription, 0, len(def.Subscriptions))
	for _, subDef := range def.Subscriptions {
		subs = append(subs, kconsumer.TopicSubscription{
			KafkaTopic:        subDef.KafkaTopic,
			VinculumTopicFunc: makeVinculumTopicFunc(config, subDef.VinculumTopic),
		})
	}
	spec.subscriptions = subs

	return spec, nil
}

// makeVinculumTopicFunc returns a VinculumTopicFunc that evaluates expr
// per-message with kafka_topic, key, fields, and msg in scope.
func makeVinculumTopicFunc(cfg *Config, expr hcl.Expression) kconsumer.VinculumTopicFunc {
	return func(kafkaTopic string, key *string, fields map[string]string, msg any) (string, error) {
		// []byte has no cty equivalent — convert to string before AnyToCty.
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("kafka consumer: convert msg: %w", err)
		}

		var ctyKey cty.Value
		if key == nil {
			ctyKey = cty.NullVal(cty.String)
		} else {
			ctyKey = cty.StringVal(*key)
		}

		ctxBuilder := NewContext(context.Background()).
			WithStringAttribute("kafka_topic", kafkaTopic).
			WithAttribute("key", ctyKey).
			WithAttribute("msg", ctyMsg)

		if len(fields) > 0 {
			ctyFields := make(map[string]cty.Value, len(fields))
			for k, v := range fields {
				ctyFields[k] = cty.StringVal(v)
			}
			ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
		}

		evalCtx, diags := ctxBuilder.BuildEvalContext(cfg.evalCtx)
		if diags.HasErrors() {
			return "", diags
		}

		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return "", diags
		}

		if val.IsNull() || val.Type() != cty.String {
			return "", fmt.Errorf("kafka consumer: vinculum_topic must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

// makeKafkaKeyFunc returns a KeyFunc that evaluates expr per-message with
// topic, msg, and fields in scope. Returns nil if the expression is null.
func makeKafkaKeyFunc(cfg *Config, expr hcl.Expression) kproducer.KeyFunc {
	return func(topic string, msg any, fields map[string]string) ([]byte, error) {
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return nil, fmt.Errorf("kafka key: convert msg: %w", err)
		}

		ctxBuilder := NewContext(context.Background()).
			WithStringAttribute("topic", topic).
			WithAttribute("msg", ctyMsg)

		if len(fields) > 0 {
			ctyFields := make(map[string]cty.Value, len(fields))
			for k, v := range fields {
				ctyFields[k] = cty.StringVal(v)
			}
			ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
		}

		evalCtx, diags := ctxBuilder.BuildEvalContext(cfg.evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}

		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return nil, diags
		}

		if val.IsNull() {
			return nil, nil
		}
		if val.Type() == cty.String {
			return []byte(val.AsString()), nil
		}
		return nil, fmt.Errorf("kafka key expression must return string or null, got %s", val.Type().FriendlyName())
	}
}
