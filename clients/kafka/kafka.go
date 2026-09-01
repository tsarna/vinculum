package kafka

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"
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
	cfg.RegisterClientType("kafka", process,
		cfg.WithSchema(kafkaClientSchema), cfg.WithReadiness())
}

// ─── HCL definition structs ──────────────────────────────────────────────────

var kafkaClientSchema = cfg.TypeSchema{
	Sample:  &KafkaClientDefinition{},
	Summary: "A Kafka client bridging Kafka topics to the bus.",
	DocPage: "client-kafka.md",
	Doc: `Connects to a Kafka cluster, available in expressions as ` + "`client.<name>`" + `.

` + "`sender`" + ` blocks produce bus messages to Kafka topics; ` + "`receiver`" + ` blocks
consume from Kafka topics as part of a consumer group.`,
	Attrs: map[string]cfg.AttrMeta{
		"brokers": {
			Summary: "Bootstrap broker addresses.",
			Doc:     "For example `[\"kafka-1:9092\", \"kafka-2:9092\"]`.",
		},
		"acks": {
			Summary: "How many replicas must acknowledge a produced record.",
			Doc: "`all` waits for every in-sync replica, `leader` for the partition " +
				"leader alone, and `none` does not wait at all. Idempotent production " +
				"requires `all`.",
			Enum:    []string{"none", "leader", "all"},
			Default: "all",
		},
		"compression": {
			Summary: "Compression applied to produced records.",
			Enum:    []string{"none", "gzip", "snappy", "lz4", "zstd"},
			Default: "none",
		},
		"idempotent": {
			Summary: "Enable idempotent production, so retries cannot duplicate a record.",
			Doc:     "Requires `acks = \"all\"`.",
			Hint:    cfg.HintBool,
			Default: "true",
		},
		"linger": {
			Summary: "How long to wait for more records before sending a batch.",
			Doc: "Trades latency for throughput. The default is franz-go's; zero sends " +
				"each batch as soon as it is ready.",
			Hint:    cfg.HintDuration,
			Default: "10ms",
		},
		"max_records": {
			Summary: "Records that may be buffered awaiting production.",
			Doc: "A produce blocks once this many records are outstanding, which is what " +
				"bounds memory when the brokers cannot keep up. The default is " +
				"franz-go's.",
			Default: "10000",
		},
		"dial_timeout": {
			Summary: "Deadline for establishing a connection to a broker.",
			Doc:     "The default is franz-go's.",
			Hint:    cfg.HintDuration,
			Default: "10s",
		},
		"request_timeout": {
			Summary: "Deadline for a single broker request.",
			Doc: "Added to the timeout the request itself asks for, rather than replacing " +
				"it. The default is franz-go's.",
			Hint:    cfg.HintDuration,
			Default: "10s",
		},
		"metadata_max_age": {
			Summary: "How long cluster metadata may be reused before it is refreshed.",
			Doc:     "The default is franz-go's.",
			Hint:    cfg.HintDuration,
			Default: "5m",
		},
		"wire_format": cfg.WireFormatAttr,
		"metrics":     cfg.MetricsAttr,
		"tracing":     cfg.TracingAttr,
	},
	Blocks: map[string]cfg.TypeSchema{
		"sasl": {
			Summary: "SASL credentials presented to the brokers.",
			Attrs: map[string]cfg.AttrMeta{
				"mechanism": {
					Summary: "SASL mechanism to authenticate with.",
					Doc:     "Spelled as Kafka spells it, in upper case.",
					Enum:    []string{"PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"},
				},
				"username": {Summary: "Username to authenticate with."},
				"password": {Summary: "Password to authenticate with.", Doc: "Supply it from the environment rather than a literal."},
			},
		},
		"sender": {
			Summary: "Produces bus messages to Kafka topics.",
			Attrs: map[string]cfg.AttrMeta{
				"produce_mode": {
					Summary: "Whether to wait for the broker to acknowledge each record.",
					Doc: "`sync` surfaces failures to the caller and applies backpressure; " +
						"`async` returns as soon as the record is queued and logs any failure " +
						"instead of returning it.",
					Enum:    []string{"sync", "async"},
					Default: "sync",
				},
				"default_topic_transform": {
					Summary: "How to derive a Kafka topic from a bus topic with no `topic` block.",
					Doc: "`error` refuses the message, which is the default because a bus " +
						"topic is rarely a valid Kafka topic by accident; `slash_to_dot` " +
						"rewrites `a/b/c` as `a.b.c`; `ignore` drops it.",
					Enum:    []string{"error", "slash_to_dot", "ignore"},
					Default: "error",
				},
			},
			Blocks: map[string]cfg.TypeSchema{
				"topic": {
					Summary: "Maps bus topics matching a pattern to a Kafka topic.",
					Doc:     "The label is the bus topic pattern.",
					Attrs: map[string]cfg.AttrMeta{
						"kafka_topic": {Summary: "Kafka topic to produce to."},
						"key": {
							Summary: "Expression producing the record key.",
							Doc: "Records sharing a key land on the same partition, so a key is what " +
								"preserves per-entity ordering. Evaluated per message; null produces " +
								"a record with no key.",
							Context: "message",
						},
					},
				},
			},
		},
		"receiver": {
			Summary: "Consumes Kafka topics as part of a consumer group.",
			Attrs: cfg.MergeAttrs(cfg.SubscriberSourceAttrs, map[string]cfg.AttrMeta{
				"group_id": {
					Summary: "Consumer group this receiver joins.",
					Doc:     "Kafka distributes each topic's partitions across the members of a group.",
				},
				"start_offset": {
					Summary: "Where to start when the group has no committed offset.",
					Doc: "`stored` resumes from the group's committed offset, which is what " +
						"production wants; `earliest` replays the whole topic; `latest` skips " +
						"everything already there.",
					Enum:    []string{"stored", "earliest", "latest"},
					Default: "stored",
				},
				"ack": cfg.AckAttr.
					WithDoc("`auto` commits a record's offset once delivery succeeds, giving "+
						"at-least-once delivery; `periodic` commits on a timer regardless of "+
						"outcome, which can lose or duplicate messages across a crash. "+
						"Unlike the other receivers, this one has no per-record settler — "+
						"acknowledging one record is not the same as committing an offset, and "+
						"completing record 7 while 5 is still outstanding needs a low-water-mark "+
						"tracker this receiver does not have. So `auto` here can only settle "+
						"when delivery returns, which is why it is refused alongside "+
						"`queue_size`, and why manual settle is not available yet.").
					WithEnum(cfg.AckAuto, cfg.AckPeriodic),
				"dlq_topic": {
					Summary: "Kafka topic to publish messages that could not be handled.",
					Doc: "The record keeps its key and value and gains `vinculum-error`, " +
						"`vinculum-original-topic`, and `vinculum-timestamp` headers. The " +
						"offset is committed only once the dead-letter send succeeds, so a " +
						"failure there redelivers rather than drops.",
				},
				"on_decode_error": cfg.OnDecodeErrorAttr.WithContextFields(
					cfg.ContextField{
						Name: "kafka_topic", Type: "string",
						Summary: "The Kafka topic the record was read from.",
						Doc:     "Equal to `ctx.topic` here: a vinculum topic derived from the payload cannot be computed once the payload has failed to decode, so `ctx.topic` falls back to this.",
					},
					cfg.ContextField{Name: "partition", Type: "string", Summary: "Partition the record was read from."},
					cfg.ContextField{Name: "offset", Type: "string", Summary: "Offset of the record within its partition."},
					cfg.ContextField{
						Name: "key", Type: "string", Optional: true,
						Summary: "The record's key.",
						Doc:     "Absent when the record was produced without one.",
					},
				),
			}),
			Constraints: cfg.SubscriberSourceConstraints,
			Blocks: map[string]cfg.TypeSchema{
				"subscription": {
					Required: true,
					Summary:  "One Kafka topic to consume.",
					Doc:      "The label is the Kafka topic.",
					Attrs: map[string]cfg.AttrMeta{
						"vinculum_topic": {
							Summary: "Bus topic to publish arriving records to.",
							Doc: "Evaluated per record, so it can interpolate the record's own " +
								"identity and headers. `ctx.fields` is populated from the " +
								"record's Kafka headers.",
							Hint:    cfg.HintTopicPattern,
							Context: "inbound-message",
							ContextFields: []cfg.ContextField{
								{Name: "kafka_topic", Type: "string", Summary: "Kafka topic the record was read from."},
								{
									Name: "key", Type: "string", Optional: true,
									Summary: "The record's key.",
									Doc:     "Null when the record was produced without one.",
								},
							},
						},
					},
				},
			},
		},
	},
}

type TopicMappingDefinition struct {
	Pattern    string         `hcl:"pattern,label"`
	KafkaTopic string         `hcl:"kafka_topic"`
	Key        hcl.Expression `hcl:"key,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type ProducerDefinition struct {
	Name                  string                   `hcl:"name,label"`
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
	KafkaTopic    string         `hcl:"kafka_topic,label"`
	VinculumTopic hcl.Expression `hcl:"vinculum_topic"`
	DefRange      hcl.Range      `hcl:",def_range"`
}

type ConsumerDefinition struct {
	Name           string                        `hcl:"name,label"`
	GroupID        string                        `hcl:"group_id"`
	StartOffset    string                        `hcl:"start_offset,optional"`
	Subscriber     hcl.Expression                `hcl:"subscriber,optional"`
	Action         hcl.Expression                `hcl:"action,optional"`
	Transforms     hcl.Expression                `hcl:"transforms,optional"`
	OnDecodeError  hcl.Expression                `hcl:"on_decode_error,optional"`
	QueueSize      *int                          `hcl:"queue_size,optional"`
	Ack            string                        `hcl:"ack,optional"`
	AckRange       hcl.Range                     `hcl:"ack,attr_range"`
	QueueSizeRange hcl.Range                     `hcl:"queue_size,attr_range"`
	DLQTopic       string                        `hcl:"dlq_topic,optional"`
	Baggage        *hclutil.BaggageFilterConfig  `hcl:"baggage,block"`
	Subscriptions  []TopicSubscriptionDefinition `hcl:"subscription,block"`
	DefRange       hcl.Range                     `hcl:",def_range"`
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
	onDecodeError wire.DecodeErrorHook
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

// Unwrap reports the producer this stands in for, so a settle point asking what
// a delivery's return will mean sees the producer's answer rather than this
// proxy's silence.
//
// It matters here more than on most wrappers: a producer with
// produce_mode = "async" returns before the broker has the record and says so,
// and a proxy that swallowed that would have the inbound message acknowledged
// at the hand-off — which is exactly the guarantee that mode is documented to
// keep. Nil before Start, which reads correctly: there is nothing behind this
// yet, and OnEvent above returns an error in that state anyway.
func (p *KafkaProducerProxy) Unwrap() bus.Subscriber {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.producer == nil {
		return nil
	}
	return p.producer
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
			WithClientName(c.Name).
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
			WithClientName(c.Name).
			WithGroupID(spec.groupID).
			WithStartOffset(spec.startOffset).
			WithCommitMode(spec.commitMode).
			WithDLQTopic(spec.dlqTopic).
			WithSubscriber(spec.subscriber).
			WithWireFormat(c.wireFormat).
			WithDecodeErrorHook(spec.onDecodeError).
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

// Ready implements cfg.Readyable: at least one broker answers.
//
// franz-go retries in the background and never fails Start, so "started" says
// nothing about whether the cluster is reachable. Ping is what distinguishes
// the two, and a failure here is recoverable — the retry loop is already
// running, so the client comes back into rotation on its own.
func (c *KafkaClient) Ready(ctx context.Context) error {
	c.mu.RLock()
	kgoClient := c.kgoClient
	c.mu.RUnlock()

	if kgoClient == nil {
		return errors.New("not connected")
	}
	return kgoClient.Ping(ctx)
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

// DeliveryDisposition reduces the producers this fans out to.
//
// One deferring producer is enough to make this whole call's nil return mean
// less than "handled", so it dominates — the conservative direction, since
// claiming to have handled what has only been queued is the unrecoverable
// mistake. Unwrap cannot express this: there are N subscribers behind this one,
// not one.
func (c *KafkaClient) DeliveryDisposition() bus.Disposition {
	c.mu.RLock()
	producers := c.producers
	c.mu.RUnlock()

	for _, p := range producers {
		if bus.DispositionOf(p) == bus.Deferred {
			return bus.Deferred
		}
	}
	return bus.Handled
}

// ─── Config processing ────────────────────────────────────────────────────────

// renamedReceiverAttrs retires `commit_mode`, whose three values were the one
// receiver-side settle policy under a Kafka-specific name — and whose third
// value, `manual`, was already rejected because nothing could commit an offset
// explicitly.
var renamedReceiverAttrs = cfg.RenameSpec{
	Blocks: []cfg.RenamedInBlock{{
		Type:   "receiver",
		Labels: []string{"name"},
		Spec: cfg.RenameSpec{
			Attrs: map[string]cfg.RenamedAttr{
				"commit_mode": {
					Now:   "ack",
					Since: "0.46.0",
					Note: "`commit_mode = \"after_process\"` was the default and is now the " +
						"default `ack = \"auto\"`. `commit_mode = \"periodic\"` is " +
						"`ack = \"periodic\"`. `commit_mode = \"manual\"` was already " +
						"rejected and remains unavailable; see " +
						"[deprecations](deprecations.md#commit_mode--manual).",
				},
			},
		},
	}},
}

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	// Before decoding, so a configuration still saying commit_mode is told what
	// it became rather than that the argument is not expected here.
	if diags := cfg.CheckRenamedAttrs(remainingBody, renamedReceiverAttrs); diags.HasErrors() {
		return nil, diags
	}

	def := KafkaClientDefinition{}
	diags := cfg.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

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

	mp, metricsDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if metricsDiags.HasErrors() {
		return nil, metricsDiags
	}

	tracerProvider, tracingDiags := config.ResolveTracerProvider(def.Tracing)
	if tracingDiags.HasErrors() {
		return nil, tracingDiags
	}

	consSpecs, consDiags := buildConsumerSpecs(config, block.Labels[1], def.Consumers, tracerProvider)
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

	// A decode failure leaves the offset uncommitted, so without a DLQ the
	// consumer re-fetches the same record forever and the partition never
	// advances. Warn at load rather than refusing to start: dlq_topic
	// already exists and is the right answer.
	if cfg.IsStrictWireFormat(wf.Name()) {
		for _, spec := range consSpecs {
			if spec.dlqTopic == "" {
				config.UserLogger.Warn(
					"kafka receiver uses a strict wire_format with no dlq_topic; "+
						"a malformed record will stall partition progress. "+
						"Set dlq_topic, or use wire_format = \"auto\" for best-effort decoding.",
					zap.String("client", block.Labels[1]),
					zap.String("receiver", spec.name),
					zap.String("wire_format", wf.Name()),
				)
			}
		}
	}

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

func buildConsumerSpecs(config *cfg.Config, clientName string, defs []ConsumerDefinition, tp trace.TracerProvider) ([]builtConsumerSpec, hcl.Diagnostics) {
	specs := make([]builtConsumerSpec, 0, len(defs))
	for _, def := range defs {
		spec, diags := buildConsumerSpec(config, clientName, def, tp)
		if diags.HasErrors() {
			return nil, diags
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func buildConsumerSpec(config *cfg.Config, clientName string, def ConsumerDefinition, tp trace.TracerProvider) (builtConsumerSpec, hcl.Diagnostics) {
	var spec builtConsumerSpec
	spec.name = def.Name
	spec.onDecodeError = cfg.MakeDecodeErrorHook(config, def.OnDecodeError,
		fmt.Sprintf("kafka receiver %q", def.Name))

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

	// "manual" is refused rather than accepted-and-ignored, which is what the
	// old commit_mode did: it did not merely fail to commit explicitly, it fell
	// through to the Kafka client's own periodic autocommit, making it an alias
	// for the weakest mode in the enum while documented as the strongest.
	policy, ackDiags := config.ResolveAck(cfg.AckRequest{
		Receiver:       fmt.Sprintf("kafka client %q receiver %q", clientName, def.Name),
		Value:          def.Ack,
		ValueRange:     def.AckRange,
		QueueSize:      def.QueueSize,
		QueueSizeRange: def.QueueSizeRange,
		Extra:          cfg.AckPeriodic,
		NoSettler: "Acknowledging one record is not the same as committing an offset: " +
			"completing record 7 while 5 is still outstanding cannot commit anything " +
			"without a low-water-mark tracker, which this receiver does not have. Use " +
			"\"auto\" without a queue for at-least-once delivery, where the offset " +
			"advances only once the record has been handled, or \"periodic\" to commit " +
			"on a timer regardless of the outcome.",
		DefRange: def.DefRange,
	})
	if ackDiags.HasErrors() {
		return spec, ackDiags
	}
	if policy.Mode == cfg.AckPeriodic {
		spec.commitMode = kconsumer.CommitPeriodic
	} else {
		spec.commitMode = kconsumer.CommitAfterProcess
	}

	spec.dlqTopic = def.DLQTopic

	if baggageDiags := def.Baggage.Validate(); baggageDiags.HasErrors() {
		return spec, baggageDiags
	}

	subscriber, diags := cfg.SubscriberSource{
		Subscriber: def.Subscriber,
		Action:     def.Action,
		Transforms: def.Transforms,
		QueueSize:  def.QueueSize,
	}.Resolve(config, def.DefRange, "kafka/"+clientName+"/"+def.Name, tp)
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

// makeVinculumTopicFunc builds the per-record HCL evaluator for a receiver's
// `subscription { vinculum_topic = ... }` expression, against the shared
// `inbound-message` shape plus the record's Kafka identity.
func makeVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) kconsumer.VinculumTopicFunc {
	return func(kafkaTopic string, key *string, fields map[string]string, msg any) (string, error) {
		ctxBuilder, err := cfg.NewInboundContext(msg, fields)
		if err != nil {
			return "", fmt.Errorf("kafka receiver: %w", err)
		}

		ctyKey := cty.NullVal(cty.String)
		if key != nil {
			ctyKey = cty.StringVal(*key)
		}

		evalCtx, err := ctxBuilder.
			WithStringAttribute("kafka_topic", kafkaTopic).
			WithAttribute("key", ctyKey).
			BuildEvalContext(config.EvalCtx())
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

		ctyFields := make(map[string]cty.Value, len(fields))
		for k, v := range fields {
			ctyFields[k] = cty.StringVal(v)
		}
		ctxBuilder = ctxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))

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
