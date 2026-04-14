// Package redispubsub implements the `client "redis_pubsub"` block — a
// child client that piggybacks on a `client "redis"` connection to publish
// (and in Phase 5, subscribe to) Redis channels.

package redispubsub

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-redis/pubsub"
	redisclient "github.com/tsarna/vinculum/clients/redis"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("redis_pubsub", process)
}

// ─── HCL schema ───────────────────────────────────────────────────────────────

type RedisPubSubDefinition struct {
	Connection   hcl.Expression `hcl:"connection"`
	OnConnect    hcl.Expression `hcl:"on_connect,optional"`
	OnDisconnect hcl.Expression `hcl:"on_disconnect,optional"`
	Metrics      hcl.Expression `hcl:"metrics,optional"`
	Publishers   []PublisherDef `hcl:"publisher,block"`
	DefRange     hcl.Range      `hcl:",def_range"`
}

type PublisherDef struct {
	Name                    string              `hcl:",label"`
	ChannelTransform        hcl.Expression      `hcl:"channel_transform,optional"`
	DefaultChannelTransform string              `hcl:"default_channel_transform,optional"`
	ChannelMappings         []ChannelMappingDef `hcl:"channel_mapping,block"`
	DefRange                hcl.Range           `hcl:",def_range"`
}

type ChannelMappingDef struct {
	Pattern  hcl.Expression `hcl:"pattern"`
	Channel  hcl.Expression `hcl:"channel,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

// ─── Runtime wrapper ──────────────────────────────────────────────────────────

// RedisPubSubClient wraps the base connection and the set of publishers
// built from the config. It implements bus.Subscriber by fanning OnEvent out
// to every publisher (used by the `client.rps.publishers` cty addressing).
type RedisPubSubClient struct {
	cfg.BaseClient
	bus.BaseSubscriber

	connector  redisclient.RedisConnector
	publishers map[string]*pubsub.RedisPubSubPublisher
	order      []string
}

// CtyValue exposes the client as `{publisher: {<name>: capsule}, publishers: capsule}`
// when any publishers are configured, matching the spec's addressing:
//   - client.rps.publisher.main — a single named publisher
//   - client.rps.publishers     — fan-out to all publishers on this client
func (c *RedisPubSubClient) CtyValue() cty.Value {
	if len(c.publishers) == 0 {
		return cfg.NewClientCapsule(c)
	}
	pubMap := make(map[string]cty.Value, len(c.publishers))
	for name, p := range c.publishers {
		pubMap[name] = cfg.NewSubscriberCapsule(p)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"publishers": cfg.NewSubscriberCapsule(c),
		"publisher":  cty.ObjectVal(pubMap),
	})
}

// OnEvent fans the event out to every publisher, surfacing a single error
// or a combined error if multiple publishers fail.
func (c *RedisPubSubClient) OnEvent(ctx context.Context, topic string, msg any, fields map[string]string) error {
	if len(c.publishers) == 0 {
		return fmt.Errorf("redis_pubsub client %q: no publishers configured", c.Name)
	}
	var errs []error
	var mu sync.Mutex
	for _, name := range c.order {
		p := c.publishers[name]
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
		return fmt.Errorf("redis_pubsub client %q: %d publish errors: %v", c.Name, len(errs), errs)
	}
	return nil
}

// ─── Config processing ────────────────────────────────────────────────────────

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := RedisPubSubDefinition{}
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
			Summary:  "redis_pubsub: connection must reference a client \"redis\" block",
			Detail:   fmt.Sprintf("got %T", baseClient),
			Subject:  &r,
		}}
	}

	if len(def.Publishers) == 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_pubsub: at least one publisher block is required",
			Detail:   "subscriber blocks land in a later phase; for now, configure at least one publisher",
			Subject:  &def.DefRange,
		}}
	}

	seen := make(map[string]struct{}, len(def.Publishers))
	publishers := make(map[string]*pubsub.RedisPubSubPublisher, len(def.Publishers))
	order := make([]string, 0, len(def.Publishers))

	for _, pdef := range def.Publishers {
		if _, dup := seen[pdef.Name]; dup {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("redis_pubsub: duplicate publisher name %q", pdef.Name),
				Subject:  &pdef.DefRange,
			}}
		}
		seen[pdef.Name] = struct{}{}

		p, pDiags := buildPublisher(config, connector, clientName, pdef)
		if pDiags.HasErrors() {
			return nil, pDiags
		}
		publishers[pdef.Name] = p
		order = append(order, pdef.Name)
	}

	// on_connect/on_disconnect are accepted in the schema but wired in
	// Phase 5 alongside subscribers; PUBLISH is stateless and piggybacks on
	// the base client's pool, so there is no connection event to fire here.
	_ = def.OnConnect
	_ = def.OnDisconnect
	_ = def.Metrics

	wrapper := &RedisPubSubClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		connector:  connector,
		publishers: publishers,
		order:      order,
	}

	return wrapper, nil
}

func buildPublisher(config *cfg.Config, connector redisclient.RedisConnector, clientName string, def PublisherDef) (*pubsub.RedisPubSubPublisher, hcl.Diagnostics) {
	b := pubsub.NewPublisher(def.Name, connector.UniversalClient()).
		WithLogger(config.Logger)

	switch def.DefaultChannelTransform {
	case "", "verbatim":
		b = b.WithDefaultTransform(pubsub.DefaultChannelVerbatim)
	case "ignore":
		b = b.WithDefaultTransform(pubsub.DefaultChannelIgnore)
	case "error":
		b = b.WithDefaultTransform(pubsub.DefaultChannelError)
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("redis_pubsub client %q publisher %q: invalid default_channel_transform", clientName, def.Name),
			Detail:   fmt.Sprintf("%q is not valid; use verbatim, ignore, or error", def.DefaultChannelTransform),
			Subject:  &def.DefRange,
		}}
	}

	if cfg.IsExpressionProvided(def.ChannelTransform) {
		b = b.WithChannelTransform(makeChannelFunc(config, def.ChannelTransform))
	}

	for _, mdef := range def.ChannelMappings {
		patternVal, patternDiags := mdef.Pattern.Value(config.EvalCtx())
		if patternDiags.HasErrors() {
			return nil, patternDiags
		}
		if patternVal.IsNull() || patternVal.Type() != cty.String {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "redis_pubsub: channel_mapping pattern must be a string",
				Subject:  &mdef.DefRange,
			}}
		}
		m := pubsub.ChannelMapping{Pattern: patternVal.AsString()}
		if cfg.IsExpressionProvided(mdef.Channel) {
			m.ChannelFunc = makeChannelFunc(config, mdef.Channel)
		}
		b = b.WithChannelMapping(m)
	}

	return b.Build(), nil
}

// makeChannelFunc wraps an HCL expression in a ChannelFunc that evaluates it
// per-message with topic, msg, and fields in scope — mirroring the MQTT
// sender's makeMQTTTopicFunc.
func makeChannelFunc(config *cfg.Config, expr hcl.Expression) pubsub.ChannelFunc {
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
			return "", fmt.Errorf("channel expression must return a string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}
