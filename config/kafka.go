package config

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
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
	Acks                  string                   `hcl:"acks,optional"`
	Compression           string                   `hcl:"compression,optional"`
	ProduceMode           string                   `hcl:"produce_mode,optional"`
	Idempotent            *bool                    `hcl:"idempotent,optional"`
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

// KafkaClientDefinition holds the decoded HCL for a client "kafka" block.
type KafkaClientDefinition struct {
	Brokers        []string             `hcl:"brokers"`
	TLS            *TLSConfig           `hcl:"tls,block"`
	SASL           *SASLDefinition      `hcl:"sasl,block"`
	Producers      []ProducerDefinition `hcl:"producer,block"`
	DialTimeout    hcl.Expression       `hcl:"dial_timeout,optional"`
	RequestTimeout hcl.Expression       `hcl:"request_timeout,optional"`
	MetadataMaxAge hcl.Expression       `hcl:"metadata_max_age,optional"`
	DefRange       hcl.Range            `hcl:",def_range"`
}

// ─── Runtime struct ───────────────────────────────────────────────────────────

// builtProducerSpec holds what is known at config time about one producer,
// ready to be handed to kproducer.ProducerBuilder in Start().
type builtProducerSpec struct {
	topicMappings []kproducer.TopicMapping
	produceMode   kproducer.ProduceMode
	defaultXform  kproducer.DefaultTopicTransform
}

// KafkaClient manages a shared franz-go client and one or more KafkaProducers.
// It implements bus.Subscriber by dispatching OnEvent to all producers.
// Implements config.Startable — Start() must be called before any events flow.
type KafkaClient struct {
	BaseClient
	bus.BaseSubscriber

	kgoOpts   []kgo.Opt
	prodSpecs []builtProducerSpec
	logger    *zap.Logger

	mu        sync.RWMutex
	kgoClient *kgo.Client
	producers []*kproducer.KafkaProducer
}

// Start creates the franz-go client and initialises all producers.
// It is called by the vinculum lifecycle manager before events begin to flow.
func (c *KafkaClient) Start() error {
	client, err := kgo.NewClient(c.kgoOpts...)
	if err != nil {
		return fmt.Errorf("kafka client %q: create client: %w", c.Name, err)
	}

	producers := make([]*kproducer.KafkaProducer, 0, len(c.prodSpecs))
	for i, spec := range c.prodSpecs {
		b := kproducer.NewProducer().
			WithClient(client).
			WithProduceMode(spec.produceMode).
			WithDefaultTransform(spec.defaultXform).
			WithLogger(c.logger)
		for _, tm := range spec.topicMappings {
			b = b.WithTopicMapping(tm)
		}
		p, err := b.Build()
		if err != nil {
			client.Close()
			return fmt.Errorf("kafka client %q producer[%d]: %w", c.Name, i, err)
		}
		producers = append(producers, p)
	}

	c.mu.Lock()
	c.kgoClient = client
	c.producers = producers
	c.mu.Unlock()

	return nil
}

// OnEvent implements bus.Subscriber. It dispatches to all producers; each
// producer applies its own topic_mapping rules (first match wins per producer).
func (c *KafkaClient) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
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

	if len(def.Producers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "kafka: at least one producer block is required",
			Subject:  &def.DefRange,
		}}
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

	// ── Producer-level options ─────────────────────────────────────────────
	// acks, compression, and idempotent are kgo.Client-level settings. With
	// multiple producers sharing one client, the first producer's settings win.

	firstProd := def.Producers[0]

	acksOpt, err := parseAcks(firstProd.Acks, firstProd.Idempotent)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "kafka producer: invalid acks",
			Detail:   err.Error(),
			Subject:  &firstProd.DefRange,
		}}
	}
	opts = append(opts, acksOpt)

	compOpt, err := parseCompression(firstProd.Compression)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "kafka producer: invalid compression",
			Detail:   err.Error(),
			Subject:  &firstProd.DefRange,
		}}
	}
	opts = append(opts, compOpt)

	// ── Per-producer specs (topic mappings, produce mode) ──────────────────

	prodSpecs, prodDiags := buildProducerSpecs(config, def.Producers)
	if prodDiags.HasErrors() {
		return nil, prodDiags
	}

	client := &KafkaClient{
		BaseClient: BaseClient{
			Name:     block.Labels[1],
			DefRange: def.DefRange,
		},
		kgoOpts:   opts,
		prodSpecs: prodSpecs,
		logger:    config.Logger,
	}

	config.Startables = append(config.Startables, client)

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
	for i, def := range defs {
		spec, diags := buildProducerSpec(config, def, i)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildProducerSpec(config *Config, def ProducerDefinition, idx int) (builtProducerSpec, hcl.Diagnostics) {
	var spec builtProducerSpec

	switch def.ProduceMode {
	case "", "sync":
		spec.produceMode = kproducer.ProduceModeSync
	case "async":
		spec.produceMode = kproducer.ProduceModeAsync
	default:
		return spec, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("kafka producer[%d]: invalid produce_mode", idx),
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
			Summary:  fmt.Sprintf("kafka producer[%d]: invalid default_topic_transform", idx),
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
