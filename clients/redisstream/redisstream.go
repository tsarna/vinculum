// Package redisstream implements the `client "redis_stream"` block — a
// child of `client "redis"` that produces to and consumes from Redis
// Streams.
package redisstream

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-redis/stream"
	wire "github.com/tsarna/vinculum-wire"
	redisclient "github.com/tsarna/vinculum/clients/redis"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterClientType("redis_stream", process)
}

// ─── HCL schema ───────────────────────────────────────────────────────────────

type RedisStreamDefinition struct {
	Connection hcl.Expression `hcl:"connection"`
	WireFormat hcl.Expression `hcl:"wire_format,optional"`
	Metrics    hcl.Expression `hcl:"metrics,optional"`
	Tracing    hcl.Expression `hcl:"tracing,optional"`
	Producers  []ProducerDef  `hcl:"producer,block"`
	Consumers  []ConsumerDef  `hcl:"consumer,block"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type ConsumerDef struct {
	Name             string         `hcl:",label"`
	Stream           hcl.Expression `hcl:"stream"`
	Group            string         `hcl:"group"`
	ConsumerName     hcl.Expression `hcl:"consumer_name,optional"`
	VinculumTopic    hcl.Expression `hcl:"vinculum_topic,optional"`
	Subscriber       hcl.Expression `hcl:"subscriber,optional"`
	Action           hcl.Expression `hcl:"action,optional"`
	BatchSize        *int64         `hcl:"batch_size,optional"`
	BlockTimeout     hcl.Expression `hcl:"block_timeout,optional"`
	AutoAck          *bool          `hcl:"auto_ack,optional"`
	GroupCreate      string         `hcl:"group_create,optional"`
	PayloadField     *string        `hcl:"payload_field,optional"`
	TopicField       *string        `hcl:"topic_field,optional"`
	ContentTypeField *string        `hcl:"content_type_field,optional"`
	FieldsMode       string         `hcl:"fields_mode,optional"`
	ReclaimPending   *bool          `hcl:"reclaim_pending,optional"`
	ReclaimMinIdle   hcl.Expression `hcl:"reclaim_min_idle,optional"`
	DeadLetterStream string         `hcl:"dead_letter_stream,optional"`
	DeadLetterAfter  *int64         `hcl:"dead_letter_after,optional"`
	DefRange         hcl.Range      `hcl:",def_range"`
}

type ProducerDef struct {
	Name                   string         `hcl:",label"`
	Stream                 hcl.Expression `hcl:"stream,optional"`
	MaxLen                 *int64         `hcl:"maxlen,optional"`
	ApproximateMaxLen      *bool          `hcl:"approximate_maxlen,optional"`
	DefaultStreamTransform string         `hcl:"default_stream_transform,optional"`
	PayloadField           *string        `hcl:"payload_field,optional"`
	TopicField             *string        `hcl:"topic_field,optional"`
	ContentTypeField       *string        `hcl:"content_type_field,optional"`
	FieldsMode             string         `hcl:"fields_mode,optional"`
	DefRange               hcl.Range      `hcl:",def_range"`
}

// ─── Runtime wrapper ──────────────────────────────────────────────────────────

// RedisStreamClient holds the producers built from the config and exposes
// them via the `client.<name>.producer.<p>` / `client.<name>.producers`
// addressing, mirroring the redis_pubsub publisher shape.
type RedisStreamClient struct {
	cfg.BaseClient
	bus.BaseSubscriber

	connector      redisclient.RedisConnector
	producers      map[string]*stream.RedisStreamProducer
	order          []string
	consumers      []*stream.RedisStreamConsumer
	consumersByKey map[string]*stream.RedisStreamConsumer
}

// Start brings up every configured consumer.
func (c *RedisStreamClient) Start() error {
	ctx := context.Background()
	for _, cc := range c.consumers {
		if err := cc.Start(ctx); err != nil {
			for _, prev := range c.consumers {
				if prev == cc {
					break
				}
				_ = prev.Stop()
			}
			return fmt.Errorf("redis_stream client %q: %w", c.Name, err)
		}
	}
	return nil
}

func (c *RedisStreamClient) Stop() error {
	for _, cc := range c.consumers {
		_ = cc.Stop()
	}
	return nil
}

func (c *RedisStreamClient) CtyValue() cty.Value {
	if len(c.producers) == 0 && len(c.consumersByKey) == 0 {
		return cfg.NewClientCapsule(c)
	}
	fields := map[string]cty.Value{}
	if len(c.producers) > 0 {
		pmap := make(map[string]cty.Value, len(c.producers))
		for name, p := range c.producers {
			pmap[name] = cfg.NewSubscriberCapsule(p)
		}
		fields["producers"] = cfg.NewSubscriberCapsule(c)
		fields["producer"] = cty.ObjectVal(pmap)
	}
	if len(c.consumersByKey) > 0 {
		cmap := make(map[string]cty.Value, len(c.consumersByKey))
		for name, cc := range c.consumersByKey {
			cmap[name] = stream.NewConsumerCapsule(cc)
		}
		fields["consumer"] = cty.ObjectVal(cmap)
	}
	return cty.ObjectVal(fields)
}

// OnEvent fans out to every producer.
func (c *RedisStreamClient) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	if len(c.producers) == 0 {
		return fmt.Errorf("redis_stream client %q: no producers configured", c.Name)
	}
	var errs []error
	var mu sync.Mutex
	for _, name := range c.order {
		p := c.producers[name]
		if err := p.OnEvent(ctx, topic, msg, fields); err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}
	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) > 1 {
		return fmt.Errorf("redis_stream client %q: %d xadd errors: %v", c.Name, len(errs), errs)
	}
	return nil
}

// ─── Config processing ────────────────────────────────────────────────────────

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := RedisStreamDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}
	def.DefRange = block.DefRange

	clientName := block.Labels[1]

	baseClient, baseDiags := cfg.GetClientFromExpression(config, def.Connection)
	if baseDiags.HasErrors() {
		return nil, baseDiags
	}
	connector, ok := baseClient.(redisclient.RedisConnector)
	if !ok {
		r := def.Connection.Range()
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_stream: connection must reference a client \"redis\" block",
			Detail:   fmt.Sprintf("got %T", baseClient),
			Subject:  &r,
		}}
	}

	if len(def.Producers) == 0 && len(def.Consumers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_stream: at least one producer or consumer block is required",
			Subject:  &def.DefRange,
		}}
	}

	mp, mpDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if mpDiags.HasErrors() {
		return nil, mpDiags
	}
	tp, tpDiags := config.ResolveTracerProvider(def.Tracing)
	if tpDiags.HasErrors() {
		return nil, tpDiags
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
				Summary:  "redis_stream: invalid wire_format",
				Detail:   err.Error(),
				Subject:  def.WireFormat.Range().Ptr(),
			}}
		}
		wf = resolved
	}
	ctyWF := &cfg.CtyWireFormat{Inner: wf}

	seen := make(map[string]struct{}, len(def.Producers))
	producers := make(map[string]*stream.RedisStreamProducer, len(def.Producers))
	order := make([]string, 0, len(def.Producers))
	for _, pdef := range def.Producers {
		if _, dup := seen[pdef.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("redis_stream: duplicate producer name %q", pdef.Name),
				Subject:  &pdef.DefRange,
			}}
		}
		seen[pdef.Name] = struct{}{}

		p, pDiags := buildProducer(config, connector, clientName, pdef, ctyWF, mp, tp)
		if pDiags.HasErrors() {
			return nil, pDiags
		}
		producers[pdef.Name] = p
		order = append(order, pdef.Name)
	}

	seenC := make(map[string]struct{}, len(def.Consumers))
	consumers := make([]*stream.RedisStreamConsumer, 0, len(def.Consumers))
	consumersByKey := make(map[string]*stream.RedisStreamConsumer, len(def.Consumers))
	for _, cdef := range def.Consumers {
		if _, dup := seenC[cdef.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("redis_stream: duplicate consumer name %q", cdef.Name),
				Subject:  &cdef.DefRange,
			}}
		}
		seenC[cdef.Name] = struct{}{}

		cc, cDiags := buildConsumer(config, connector, clientName, cdef, ctyWF, mp, tp)
		if cDiags.HasErrors() {
			return nil, cDiags
		}
		consumers = append(consumers, cc)
		consumersByKey[cdef.Name] = cc
	}

	wrapper := &RedisStreamClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		connector:      connector,
		producers:      producers,
		order:          order,
		consumers:      consumers,
		consumersByKey: consumersByKey,
	}

	if len(consumers) > 0 {
		config.Startables = append(config.Startables, wrapper)
		config.Stoppables = append(config.Stoppables, wrapper)
	}

	return wrapper, nil
}

func buildProducer(config *cfg.Config, connector redisclient.RedisConnector, clientName string, def ProducerDef, wf wire.WireFormat, mp metric.MeterProvider, tp trace.TracerProvider) (*stream.RedisStreamProducer, hcl.Diagnostics) {
	b := stream.NewProducer(def.Name, connector.UniversalClient()).
		WithClientName(clientName).
		WithWireFormat(wf).
		WithLogger(config.Logger).
		WithMeterProvider(mp).
		WithTracerProvider(tp)

	switch def.DefaultStreamTransform {
	case "", "error":
		b = b.WithDefaultTransform(stream.DefaultStreamError)
	case "ignore":
		b = b.WithDefaultTransform(stream.DefaultStreamIgnore)
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("redis_stream client %q producer %q: invalid default_stream_transform", clientName, def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use error or ignore", def.DefaultStreamTransform),
			Subject:  &def.DefRange,
		}}
	}

	if cfg.IsExpressionProvided(def.Stream) {
		b = b.WithStreamFunc(makeStreamFunc(config, def.Stream))
	}

	if def.MaxLen != nil {
		if *def.MaxLen < 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis_stream: maxlen must be non-negative",
				Subject:  &def.DefRange,
			}}
		}
		b = b.WithMaxLen(*def.MaxLen)
	}
	if def.ApproximateMaxLen != nil {
		b = b.WithApproximateMaxLen(*def.ApproximateMaxLen)
	}
	if def.PayloadField != nil {
		b = b.WithPayloadField(*def.PayloadField)
	}
	if def.TopicField != nil {
		b = b.WithTopicField(*def.TopicField)
	}
	if def.ContentTypeField != nil {
		b = b.WithContentTypeField(*def.ContentTypeField)
	}

	switch def.FieldsMode {
	case "", "flat":
		b = b.WithFieldsMode(stream.FieldsFlat)
	case "nested":
		b = b.WithFieldsMode(stream.FieldsNested)
	case "omit":
		b = b.WithFieldsMode(stream.FieldsOmit)
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("redis_stream client %q producer %q: invalid fields_mode", clientName, def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use flat, nested, or omit", def.FieldsMode),
			Subject:  &def.DefRange,
		}}
	}

	// Warn at config time if a custom field name collides with a reserved
	// stream entry field. We cannot inspect the runtime fields map here,
	// but we can catch the obvious case where a user explicitly sets
	// payload/topic/content-type field names to reserved values that
	// don't match the standard ones, or names that overlap with the
	// trace-context fields.
	for _, kv := range []struct {
		setting string
		val     string
	}{
		{"payload_field", strDeref(def.PayloadField)},
		{"topic_field", strDeref(def.TopicField)},
		{"content_type_field", strDeref(def.ContentTypeField)},
	} {
		if kv.val == "" {
			continue
		}
		if kv.val == "traceparent" || kv.val == "tracestate" || kv.val == "fields" {
			config.Logger.Warn("redis_stream: field name overlaps a reserved stream entry field — Vinculum reserved names always win at runtime",
				zap.String("client", clientName),
				zap.String("producer", def.Name),
				zap.String("setting", kv.setting),
				zap.String("value", kv.val))
		}
	}

	return b.Build(), nil
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// makeStreamFunc wraps an HCL expression into a StreamFunc evaluated per
// message with ctx.topic/ctx.msg/ctx.fields in scope.
func makeStreamFunc(config *cfg.Config, expr hcl.Expression) stream.StreamFunc {
	return func(topic string, msg any, fields map[string]string) (string, error) {
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("convert msg: %w", err)
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
			return "", fmt.Errorf("stream expression must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

func buildConsumer(config *cfg.Config, connector redisclient.RedisConnector, clientName string, def ConsumerDef, wf wire.WireFormat, mp metric.MeterProvider, tp trace.TracerProvider) (*stream.RedisStreamConsumer, hcl.Diagnostics) {
	hasSubscriber := cfg.IsExpressionProvided(def.Subscriber)
	hasAction := cfg.IsExpressionProvided(def.Action)
	if hasSubscriber == hasAction {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("redis_stream client %q consumer %q: exactly one of subscriber or action must be specified", clientName, def.Name),
			Subject:  &def.DefRange,
		}}
	}

	var target bus.Subscriber
	if hasSubscriber {
		sub, diags := cfg.GetSubscriberFromExpression(config, def.Subscriber)
		if diags.HasErrors() {
			return nil, diags
		}
		target = sub
	} else {
		target = cfg.NewActionSubscriber(config, def.Action)
	}

	streamVal, sDiags := def.Stream.Value(config.EvalCtx())
	if sDiags.HasErrors() {
		return nil, sDiags
	}
	if streamVal.IsNull() || streamVal.Type() != cty.String || streamVal.AsString() == "" {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_stream: consumer stream must be a non-empty string",
			Subject:  &def.DefRange,
		}}
	}
	streamName := streamVal.AsString()

	if def.Group == "" {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_stream: consumer group is required",
			Subject:  &def.DefRange,
		}}
	}

	consumerName, cnDiags := resolveConsumerName(config, def.ConsumerName, clientName, def.Name)
	if cnDiags.HasErrors() {
		return nil, cnDiags
	}

	b := stream.NewConsumer(def.Name, connector.UniversalClient()).
		WithClientName(clientName).
		WithStream(streamName).
		WithGroup(def.Group).
		WithConsumerName(consumerName).
		WithTarget(target).
		WithWireFormat(wf).
		WithLogger(config.Logger).
		WithMeterProvider(mp).
		WithTracerProvider(tp)

	if def.BatchSize != nil {
		if *def.BatchSize <= 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis_stream: batch_size must be positive",
				Subject:  &def.DefRange,
			}}
		}
		b = b.WithBatchSize(*def.BatchSize)
	}
	if cfg.IsExpressionProvided(def.BlockTimeout) {
		d, dDiags := config.ParseDuration(def.BlockTimeout)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		b = b.WithBlockTimeout(d)
	}
	if def.AutoAck != nil {
		b = b.WithAutoAck(*def.AutoAck)
	}

	switch def.GroupCreate {
	case "", "create_if_missing":
		b = b.WithGroupCreatePolicy(stream.GroupCreateIfMissing)
	case "require_existing":
		b = b.WithGroupCreatePolicy(stream.GroupRequireExisting)
	case "create_from_start":
		b = b.WithGroupCreatePolicy(stream.GroupCreateFromStart)
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("redis_stream client %q consumer %q: invalid group_create", clientName, def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use create_if_missing, require_existing, or create_from_start", def.GroupCreate),
			Subject:  &def.DefRange,
		}}
	}

	if def.PayloadField != nil {
		b = b.WithPayloadField(*def.PayloadField)
	}
	if def.TopicField != nil {
		b = b.WithTopicField(*def.TopicField)
	}
	if def.ContentTypeField != nil {
		b = b.WithContentTypeField(*def.ContentTypeField)
	}
	switch def.FieldsMode {
	case "", "flat":
		b = b.WithFieldsMode(stream.FieldsFlat)
	case "nested":
		b = b.WithFieldsMode(stream.FieldsNested)
	case "omit":
		b = b.WithFieldsMode(stream.FieldsOmit)
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("redis_stream client %q consumer %q: invalid fields_mode", clientName, def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use flat, nested, or omit", def.FieldsMode),
			Subject:  &def.DefRange,
		}}
	}

	if cfg.IsExpressionProvided(def.VinculumTopic) {
		b = b.WithTopicFunc(makeVinculumTopicFunc(config, def.VinculumTopic))
	}

	if def.ReclaimPending != nil {
		b = b.WithReclaimPending(*def.ReclaimPending)
	}
	if cfg.IsExpressionProvided(def.ReclaimMinIdle) {
		d, dDiags := config.ParseDuration(def.ReclaimMinIdle)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		b = b.WithReclaimMinIdle(d)
	}
	if def.DeadLetterStream != "" {
		if def.DeadLetterAfter == nil || *def.DeadLetterAfter <= 0 {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis_stream: dead_letter_stream requires a positive dead_letter_after",
				Subject:  &def.DefRange,
			}}
		}
		b = b.WithDeadLetterStream(def.DeadLetterStream).
			WithDeadLetterAfter(*def.DeadLetterAfter)
	} else if def.DeadLetterAfter != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_stream: dead_letter_after has no effect without dead_letter_stream",
			Subject:  &def.DefRange,
		}}
	}

	return b.Build(), nil
}

// resolveConsumerName evaluates consumer_name or falls back to a hostname-
// based default. The default is stable within a process so restart sees
// the same pending messages under the same consumer.
func resolveConsumerName(config *cfg.Config, expr hcl.Expression, clientName, consumerName string) (string, hcl.Diagnostics) {
	if cfg.IsExpressionProvided(expr) {
		val, diags := expr.Value(config.EvalCtx())
		if diags.HasErrors() {
			return "", diags
		}
		if val.IsNull() || val.Type() != cty.String || val.AsString() == "" {
			r := expr.Range()
			return "", hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis_stream: consumer_name must be a non-empty string",
				Subject:  &r,
			}}
		}
		return val.AsString(), nil
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "vinculum"
	}
	return fmt.Sprintf("%s-%s-%s", host, clientName, consumerName), nil
}

// makeVinculumTopicFunc builds a resolver that evaluates the HCL expression
// per message with ctx.topic (stream name), ctx.message_id, ctx.msg, and
// ctx.fields in scope.
func makeVinculumTopicFunc(config *cfg.Config, expr hcl.Expression) stream.VinculumTopicFromStreamFunc {
	return func(streamName, entryID string, msg any, fields map[string]string) (string, error) {
		if b, ok := msg.([]byte); ok {
			msg = string(b)
		}
		ctyMsg, err := go2cty2go.AnyToCty(msg)
		if err != nil {
			return "", fmt.Errorf("convert msg: %w", err)
		}

		ctxBuilder := hclutil.NewEvalContext(context.Background()).
			WithStringAttribute("topic", streamName).
			WithStringAttribute("message_id", entryID).
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
		if val.IsNull() {
			return "", nil
		}
		if val.Type() != cty.String {
			return "", fmt.Errorf("vinculum_topic must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}
