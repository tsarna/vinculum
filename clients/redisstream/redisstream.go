// Package redisstream implements the `client "redis_stream"` block — a
// child of `client "redis"` that produces (and, in a later phase, consumes)
// Redis Streams.
//
// Phase 6: producers only. Consumer sub-blocks land in Phase 7.
package redisstream

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-redis/stream"
	redisclient "github.com/tsarna/vinculum/clients/redis"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

func init() {
	cfg.RegisterClientType("redis_stream", process)
}

// ─── HCL schema ───────────────────────────────────────────────────────────────

type RedisStreamDefinition struct {
	Connection hcl.Expression `hcl:"connection"`
	Metrics    hcl.Expression `hcl:"metrics,optional"`
	Producers  []ProducerDef  `hcl:"producer,block"`
	DefRange   hcl.Range      `hcl:",def_range"`
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

	connector redisclient.RedisConnector
	producers map[string]*stream.RedisStreamProducer
	order     []string
}

func (c *RedisStreamClient) CtyValue() cty.Value {
	if len(c.producers) == 0 {
		return cfg.NewClientCapsule(c)
	}
	pmap := make(map[string]cty.Value, len(c.producers))
	for name, p := range c.producers {
		pmap[name] = cfg.NewSubscriberCapsule(p)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"producers": cfg.NewSubscriberCapsule(c),
		"producer":  cty.ObjectVal(pmap),
	})
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

	if len(def.Producers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_stream: at least one producer block is required",
			Detail:   "consumer blocks land in a later phase; for now, configure at least one producer",
			Subject:  &def.DefRange,
		}}
	}

	_ = def.Metrics // Phase 10

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

		p, pDiags := buildProducer(config, connector, clientName, pdef)
		if pDiags.HasErrors() {
			return nil, pDiags
		}
		producers[pdef.Name] = p
		order = append(order, pdef.Name)
	}

	wrapper := &RedisStreamClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		connector: connector,
		producers: producers,
		order:     order,
	}

	return wrapper, nil
}

func buildProducer(config *cfg.Config, connector redisclient.RedisConnector, clientName string, def ProducerDef) (*stream.RedisStreamProducer, hcl.Diagnostics) {
	b := stream.NewProducer(def.Name, connector.UniversalClient()).
		WithLogger(config.Logger)

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
