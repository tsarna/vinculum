package kafka

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	kconsumer "github.com/tsarna/vinculum-kafka/consumer"
	kproducer "github.com/tsarna/vinculum-kafka/producer"
	wire "github.com/tsarna/vinculum-wire"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
	"github.com/twmb/franz-go/plugin/kotel"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterClientType("kafka", process)
}

// ─── HCL definition structs ──────────────────────────────────────────────────

type TopicMappingDefinition struct {
	Pattern    string         `hcl:",label"`
	KafkaTopic string         `hcl:"kafka_topic"`
	Key        hcl.Expression `hcl:"key,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type ProducerDefinition struct {
	Name                  string                   `hcl:",label"`
	ProduceMode           string                   `hcl:"produce_mode,optional"`
	TopicMappings         []TopicMappingDefinition `hcl:"topic,block"`
	DefaultTopicTransform string                   `hcl:"default_topic_transform,optional"`
	DefRange              hcl.Range                `hcl:",def_range"`
}

type SASLDefinition struct {
	Mechanism string         `hcl:"mechanism"`
	Username  string         `hcl:"username,optional"`
	Password  hcl.Expression `hcl:"password,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type TopicSubscriptionDefinition struct {
	KafkaTopic    string         `hcl:",label"`
	VinculumTopic hcl.Expression `hcl:"vinculum_topic"`
	DefRange      hcl.Range      `hcl:",def_range"`
}

type ConsumerDefinition struct {
	Name          string                        `hcl:",label"`
	GroupID       string                        `hcl:"group_id"`
	StartOffset   string                        `hcl:"start_offset,optional"`
	Subscriber    hcl.Expression                `hcl:"subscriber,optional"`
	Action        hcl.Expression                `hcl:"action,optional"`
	CommitMode    string                        `hcl:"commit_mode,optional"`
	DLQTopic      string                        `hcl:"dlq_topic,optional"`
	Subscriptions []TopicSubscriptionDefinition `hcl:"subscription,block"`
	DefRange      hcl.Range                     `hcl:",def_range"`
}

type KafkaClientDefinition struct {
	Brokers        []string             `hcl:"brokers"`
	TLS            *cfg.TLSConfig       `hcl:"tls,block"`
	SASL           *SASLDefinition      `hcl:"sasl,block"`
	Producers      []ProducerDefinition `hcl:"sender,block"`
	Consumers      []ConsumerDefinition `hcl:"receiver,block"`
	Acks           string               `hcl:"acks,optional"`
	Compression    string               `hcl:"compression,optional"`
	Idempotent     *bool                `hcl:"idempotent,optional"`
	Linger         hcl.Expression       `hcl:"linger,optional"`
	MaxRecords     *int                 `hcl:"max_records,optional"`
	DialTimeout    hcl.Expression       `hcl:"dial_timeout,optional"`
	RequestTimeout hcl.Expression       `hcl:"request_timeout,optional"`
	MetadataMaxAge hcl.Expression       `hcl:"metadata_max_age,optional"`
	WireFormat     hcl.Expression       `hcl:"wire_format,optional"`
	Metrics        hcl.Expression       `hcl:"metrics,optional"`
	Tracing        hcl.Expression       `hcl:"tracing,optional"`
	DefRange       hcl.Range            `hcl:",def_range"`
}

// ─── Runtime structs ──────────────────────────────────────────────────────────

type builtProducerSpec struct {
	name          string
	topicMappings []kproducer.TopicMapping
	produceMode   kproducer.ProduceMode
	defaultXform  kproducer.DefaultTopicTransform
}

type builtConsumerSpec struct {
	name          string
	groupID       string
	startOffset   kgo.Offset
	commitMode    kconsumer.CommitMode
	dlqTopic      string
	subscriptions []kconsumer.TopicSubscription
	subscriber    bus.Subscriber
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
// and zero or more KafkaConsumers.
type KafkaClient struct {
	cfg.BaseClient
	bus.BaseSubscriber

	kgoOpts         []kgo.Opt
	prodSpecs       []builtProducerSpec
	consSpecs       []builtConsumerSpec
	producerProxies map[string]*KafkaProducerProxy
	wireFormat      wire.WireFormat
	meterProvider   metric.MeterProvider
	tracerProvider  trace.TracerProvider
	logger          *zap.Logger

	mu         sync.RWMutex
	kgoClient  *kgo.Client
	producers  []*kproducer.KafkaProducer
	consumers  []*kconsumer.KafkaConsumer
	consCancel context.CancelFunc
}

func (c *KafkaClient) CtyValue() cty.Value {
	if len(c.producerProxies) == 0 {
		return cfg.NewClientCapsule(c)
	}
	prodMap := make(map[string]cty.Value, len(c.producerProxies))
	for name, proxy := range c.producerProxies {
		prodMap[name] = cfg.NewSubscriberCapsule(proxy)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"senders": cfg.NewSubscriberCapsule(c),
		"sender":  cty.ObjectVal(prodMap),
	})
}

func (c *KafkaClient) Start() error {
	// Build the effective kgo opts, prepending the kotel tracing hook when a
	// TracerProvider is configured. Both the producer client and each consumer
	// client receive the same hook so trace context flows bidirectionally.
	kgoOpts := c.kgoOpts
	if c.tracerProvider != nil {
		kotelTracer := kotel.NewTracer(
			kotel.TracerProvider(c.tracerProvider),
			kotel.TracerPropagator(otel.GetTextMapPropagator()),
			kotel.LinkSpans(),
		)
		kgoOpts = append(kgoOpts, kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(kotelTracer)).Hooks()...))
	}

	var kgoClient *kgo.Client
	if len(c.prodSpecs) > 0 {
		client, err := kgo.NewClient(kgoOpts...)
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
			WithWireFormat(c.wireFormat).
			WithMeterProvider(c.meterProvider).
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

	consumerCtx, consCancel := context.WithCancel(context.Background())
	consumers := make([]*kconsumer.KafkaConsumer, 0, len(c.consSpecs))

	for _, spec := range c.consSpecs {
		b := kconsumer.NewConsumer().
			WithBaseOpts(kgoOpts).
			WithGroupID(spec.groupID).
			WithStartOffset(spec.startOffset).
			WithCommitMode(spec.commitMode).
			WithDLQTopic(spec.dlqTopic).
			WithSubscriber(spec.subscriber).
			WithWireFormat(c.wireFormat).
			WithMeterProvider(c.meterProvider).
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

func (c *KafkaClient) Stop() error {
	c.mu.RLock()
	kgoClient := c.kgoClient
	consumers := c.consumers
	consCancel := c.consCancel
	c.mu.RUnlock()

	if consCancel != nil {
		consCancel()
	}
	for _, cons := range consumers {
		cons.Stop()
	}

	if kgoClient != nil {
		if err := kgoClient.Flush(context.Background()); err != nil {
			c.logger.Error("kafka: flush on shutdown failed", zap.String("client", c.Name), zap.Error(err))
		}
		kgoClient.Close()
	}

	return nil
}

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

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := KafkaClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
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

	opts := []kgo.Opt{
		kgo.SeedBrokers(def.Brokers...),
	}

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

	if def.SASL != nil {
		saslOpt, saslDiags := buildSASLOpt(config, def.SASL)
		if saslDiags.HasErrors() {
			return nil, saslDiags
		}
		opts = append(opts, saslOpt)
	}

	if cfg.IsExpressionProvided(def.DialTimeout) {
		d, ddiags := config.ParseDuration(def.DialTimeout)
		if ddiags.HasErrors() {
			return nil, ddiags
		}
		opts = append(opts, kgo.DialTimeout(d))
	}

	if cfg.IsExpressionProvided(def.RequestTimeout) {
		d, ddiags := config.ParseDuration(def.RequestTimeout)
		if ddiags.HasErrors() {
			return nil, ddiags
		}
		opts = append(opts, kgo.RequestTimeoutOverhead(d))
	}

	if cfg.IsExpressionProvided(def.MetadataMaxAge) {
		d, ddiags := config.ParseDuration(def.MetadataMaxAge)
		if ddiags.HasErrors() {
			return nil, ddiags
		}
		opts = append(opts, kgo.MetadataMaxAge(d))
	}

	if len(def.Producers) > 0 {
		acksOpt, err := parseAcks(def.Acks)
		if err != nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "kafka: invalid acks",
				Detail:   err.Error(),
				Subject:  &def.DefRange,
			}}
		}
		opts = append(opts, acksOpt)

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

		if cfg.IsExpressionProvided(def.Linger) {
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

	prodSpecs, prodDiags := buildProducerSpecs(config, def.Producers)
	if prodDiags.HasErrors() {
		return nil, prodDiags
	}

	consSpecs, consDiags := buildConsumerSpecs(config, def.Consumers)
	if consDiags.HasErrors() {
		return nil, consDiags
	}

	producerProxies := make(map[string]*KafkaProducerProxy, len(prodSpecs))
	for _, spec := range prodSpecs {
		producerProxies[spec.name] = &KafkaProducerProxy{
			clientName:   block.Labels[1],
			producerName: spec.name,
		}
	}

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
				Summary:  "kafka: invalid wire_format",
				Detail:   err.Error(),
				Subject:  def.WireFormat.Range().Ptr(),
			}}
		}
		wf = resolved
	}
	// Wrap with CtyWireFormat so cty.Value payloads are converted transparently.
	ctyWF := &cfg.CtyWireFormat{Inner: wf}

	client := &KafkaClient{
		BaseClient: cfg.BaseClient{
			Name:     block.Labels[1],
			DefRange: def.DefRange,
		},
		kgoOpts:         opts,
		prodSpecs:       prodSpecs,
		consSpecs:       consSpecs,
		producerProxies: producerProxies,
		wireFormat:      ctyWF,
		meterProvider:   mp,
		tracerProvider:  tracerProvider,
		logger:          config.Logger,
	}

	config.Startables = append(config.Startables, client)
	config.Stoppables = append(config.Stoppables, client)

	return client, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func buildSASLOpt(config *cfg.Config, def *SASLDefinition) (kgo.Opt, hcl.Diagnostics) {
	var password string
	if cfg.IsExpressionProvided(def.Password) {
		val, diags := def.Password.Value(config.EvalCtx())
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

func parseAcks(acks string) (kgo.Opt, error) {
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

func buildProducerSpecs(config *cfg.Config, defs []ProducerDefinition) ([]builtProducerSpec, hcl.Diagnostics) {
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

func buildProducerSpec(config *cfg.Config, def ProducerDefinition) (builtProducerSpec, hcl.Diagnostics) {
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
			Summary:  fmt.Sprintf("kafka sender %q: invalid produce_mode", def.Name),
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
			Summary:  fmt.Sprintf("kafka sender %q: invalid default_topic_transform", def.Name),
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
		if cfg.IsExpressionProvided(tmDef.Key) {
			staticVal, isStatic := cfg.IsConstantExpression(tmDef.Key)
			if isStatic && staticVal.IsNull() {
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

func buildConsumerSpecs(config *cfg.Config, defs []ConsumerDefinition) ([]builtConsumerSpec, hcl.Diagnostics) {
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

func buildConsumerSpec(config *cfg.Config, def ConsumerDefinition) (builtConsumerSpec, hcl.Diagnostics) {
	var spec builtConsumerSpec
	spec.name = def.Name

	if def.GroupID == "" {
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka receiver %q: group_id is required", def.Name),
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
			Summary:  fmt.Sprintf("kafka receiver %q: invalid start_offset", def.Name),
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
			Summary:  fmt.Sprintf("kafka receiver %q: invalid commit_mode", def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use after_process, periodic, or manual", def.CommitMode),
			Subject:  &def.DefRange,
		}}
	}

	spec.dlqTopic = def.DLQTopic

	hasSubscriber := cfg.IsExpressionProvided(def.Subscriber)
	hasAction := cfg.IsExpressionProvided(def.Action)
	if hasSubscriber == hasAction {
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka receiver %q: exactly one of subscriber or action must be specified", def.Name),
			Subject:  &def.DefRange,
		}}
	}

	var subscriber bus.Subscriber
	if hasSubscriber {
		var diags hcl.Diagnostics
		subscriber, diags = cfg.GetSubscriberFromExpression(config, def.Subscriber)
		if diags.HasErrors() {
			return spec, diags
		}
	} else {
		subscriber = cfg.NewActionSubscriber(config, def.Action)
	}
	spec.subscriber = subscriber

	if len(def.Subscriptions) == 0 {
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka receiver %q: at least one subscription block is required", def.Name),
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

func makeVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) kconsumer.VinculumTopicFunc {
	return func(kafkaTopic string, key *string, fields map[string]string, msg any) (string, error) {
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("kafka receiver: convert msg: %w", err)
		}

		var ctyKey cty.Value
		if key == nil {
			ctyKey = cty.NullVal(cty.String)
		} else {
			ctyKey = cty.StringVal(*key)
		}

		ctxBuilder := hclutil.NewEvalContext(context.Background()).
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

		evalCtx, err := ctxBuilder.BuildEvalContext(config.EvalCtx())
		if err != nil {
			return "", err
		}

		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return "", diags
		}

		if val.IsNull() || val.Type() != cty.String {
			return "", fmt.Errorf("kafka receiver: vinculum_topic must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

func makeKafkaKeyFunc(config *cfg.Config, expr hcl.Expression) kproducer.KeyFunc {
	return func(topic string, msg any, fields map[string]string) ([]byte, error) {
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return nil, fmt.Errorf("kafka key: convert msg: %w", err)
		}

		ctxBuilder := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("topic", topic).
			WithAttribute("msg", ctyMsg)

		if len(fields) > 0 {
			ctyFields := make(map[string]cty.Value, len(fields))
			for k, v := range fields {
				ctyFields[k] = cty.StringVal(v)
			}
			ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
		}

		evalCtx, err := ctxBuilder.BuildEvalContext(config.EvalCtx())
		if err != nil {
			return nil, err
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
