// Package rediskv implements the `client "redis_kv"` block — a thin adapter
// that exposes Redis string operations through Vinculum's generic
// get()/set()/increment() interface via richcty.Gettable/Settable/Incrementable.

package rediskv

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	goredis "github.com/redis/go-redis/v9"
	richcty "github.com/tsarna/rich-cty-types"
	wire "github.com/tsarna/vinculum-wire"
	redisclient "github.com/tsarna/vinculum/clients/redis"
	cfg "github.com/tsarna/vinculum/config"
	vtypes "github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("redis_kv", process)
}

type RedisKVDefinition struct {
	Connection hcl.Expression `hcl:"connection"`
	KeyPrefix  string         `hcl:"key_prefix,optional"`
	DefaultTTL hcl.Expression `hcl:"default_ttl,optional"`
	WireFormat hcl.Expression `hcl:"wire_format,optional"`
	HashMode   *bool          `hcl:"hash_mode,optional"`
	Metrics    hcl.Expression `hcl:"metrics,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

// RedisKVClient is the runtime object exposed to HCL as a client capsule.
// It implements richcty.Gettable/Settable/Incrementable so that the generic
// get()/set()/increment() functions dispatch to the corresponding Redis
// commands.
type RedisKVClient struct {
	cfg.BaseClient
	connector  redisclient.RedisConnector
	keyPrefix  string
	defaultTTL time.Duration
	wireFormat *cfg.CtyWireFormat
	hashMode   bool
	metrics    *kvMetrics
}

func (c *RedisKVClient) redis() goredis.UniversalClient {
	return c.connector.UniversalClient()
}

func (c *RedisKVClient) key(k string) string {
	return c.keyPrefix + k
}

// --- richcty.Gettable ---

func (c *RedisKVClient) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) < 1 {
		return cty.NilVal, fmt.Errorf("redis_kv.get: key argument required")
	}
	key, err := asString(args[0], "key")
	if err != nil {
		return cty.NilVal, err
	}

	if c.hashMode {
		return c.hashGet(ctx, key, args[1:])
	}

	var defaultVal cty.Value = cty.NullVal(cty.DynamicPseudoType)
	if len(args) >= 2 {
		defaultVal = args[1]
	}

	defer c.metrics.Timed(ctx, "GET")()
	raw, err := c.redis().Get(ctx, c.key(key)).Result()
	if err != nil {
		if err == goredis.Nil {
			c.metrics.RecordMiss(ctx)
			return defaultVal, nil
		}
		c.metrics.RecordError(ctx, "GET", "get")
		return cty.NilVal, fmt.Errorf("redis_kv.get: %w", err)
	}
	c.metrics.RecordHit(ctx)
	return c.decode(raw)
}

// hashGet implements the hash-mode behavior of get():
//   - get(c, key)         → HGETALL key as a cty object with decoded values
//   - get(c, key, field)  → HGET key field, decoded per value_encoding
//
// The default-value form from string mode is not available in hash mode:
// HGET on a missing field returns null, not a default. That asymmetry is
// intentional — a default only makes sense when the shape of the result
// is known (strings), and HGETALL returns an object.
func (c *RedisKVClient) hashGet(ctx context.Context, key string, rest []cty.Value) (cty.Value, error) {
	if len(rest) == 0 {
		defer c.metrics.Timed(ctx, "HGETALL")()
		all, err := c.redis().HGetAll(ctx, c.key(key)).Result()
		if err != nil {
			return cty.NilVal, fmt.Errorf("redis_kv.get (hash): %w", err)
		}
		if len(all) == 0 {
			return cty.NullVal(cty.DynamicPseudoType), nil
		}
		out := make(map[string]cty.Value, len(all))
		for f, v := range all {
			dv, err := c.decode(v)
			if err != nil {
				return cty.NilVal, fmt.Errorf("redis_kv.get (hash) field %q: %w", f, err)
			}
			out[f] = dv
		}
		return cty.ObjectVal(out), nil
	}
	field, err := asString(rest[0], "field")
	if err != nil {
		return cty.NilVal, err
	}
	defer c.metrics.Timed(ctx, "HGET")()
	raw, err := c.redis().HGet(ctx, c.key(key), field).Result()
	if err != nil {
		if err == goredis.Nil {
			return cty.NullVal(cty.DynamicPseudoType), nil
		}
		return cty.NilVal, fmt.Errorf("redis_kv.get (hash): %w", err)
	}
	return c.decode(raw)
}

// --- richcty.Settable ---

func (c *RedisKVClient) Set(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) < 2 {
		return cty.NilVal, fmt.Errorf("redis_kv.set: key and value arguments required")
	}
	key, err := asString(args[0], "key")
	if err != nil {
		return cty.NilVal, err
	}

	if c.hashMode {
		if len(args) < 3 {
			return cty.NilVal, fmt.Errorf("redis_kv.set (hash): set(c, key, field, value) — field argument required")
		}
		field, err := asString(args[1], "field")
		if err != nil {
			return cty.NilVal, err
		}
		encoded, err := c.encode(args[2])
		if err != nil {
			return cty.NilVal, err
		}
		defer c.metrics.Timed(ctx, "HSET")()
		if err := c.redis().HSet(ctx, c.key(key), field, encoded).Err(); err != nil {
			c.metrics.RecordError(ctx, "HSET", "hset")
			return cty.NilVal, fmt.Errorf("redis_kv.set (hash): %w", err)
		}
		return cty.NullVal(cty.DynamicPseudoType), nil
	}

	encoded, err := c.encode(args[1])
	if err != nil {
		return cty.NilVal, err
	}

	ttl, persist, err := c.resolveTTL(args)
	if err != nil {
		return cty.NilVal, err
	}

	defer c.metrics.Timed(ctx, "SET")()
	if err := c.redis().Set(ctx, c.key(key), encoded, ttl).Err(); err != nil {
		c.metrics.RecordError(ctx, "SET", "set")
		return cty.NilVal, fmt.Errorf("redis_kv.set: %w", err)
	}
	if persist {
		if err := c.redis().Persist(ctx, c.key(key)).Err(); err != nil {
			return cty.NilVal, fmt.Errorf("redis_kv.set: persist: %w", err)
		}
	}
	return cty.NullVal(cty.DynamicPseudoType), nil
}

// resolveTTL returns (ttl, persist, err). persist=true means the caller passed
// an explicit 0/"0" and we should call PERSIST to strip any existing expiry.
func (c *RedisKVClient) resolveTTL(args []cty.Value) (time.Duration, bool, error) {
	if len(args) < 3 {
		return c.defaultTTL, false, nil
	}
	ttlArg := args[2]
	if ttlArg.IsNull() {
		return c.defaultTTL, false, nil
	}
	switch ttlArg.Type() {
	case cty.String:
		s := ttlArg.AsString()
		if s == "" || s == "0" {
			return 0, true, nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, false, fmt.Errorf("redis_kv.set: invalid ttl %q: %w", s, err)
		}
		return d, false, nil
	case cty.Number:
		secs, _ := ttlArg.AsBigFloat().Float64()
		if secs == 0 {
			return 0, true, nil
		}
		return time.Duration(secs * float64(time.Second)), false, nil
	default:
		return 0, false, fmt.Errorf("redis_kv.set: ttl must be a string or number, got %s", ttlArg.Type().FriendlyName())
	}
}

// --- richcty.Incrementable ---

func (c *RedisKVClient) Increment(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) < 2 {
		return cty.NilVal, fmt.Errorf("redis_kv.increment: key and delta arguments required")
	}
	key, err := asString(args[0], "key")
	if err != nil {
		return cty.NilVal, err
	}
	if args[1].Type() != cty.Number {
		return cty.NilVal, fmt.Errorf("redis_kv.increment: delta must be a number, got %s", args[1].Type().FriendlyName())
	}
	delta := args[1].AsBigFloat()
	if delta.IsInt() {
		n, _ := delta.Int64()
		defer c.metrics.Timed(ctx, "INCRBY")()
		v, err := c.redis().IncrBy(ctx, c.key(key), n).Result()
		if err != nil {
			c.metrics.RecordError(ctx, "INCRBY", "incrby")
			return cty.NilVal, fmt.Errorf("redis_kv.increment: %w", err)
		}
		return cty.NumberIntVal(v), nil
	}
	f, _ := delta.Float64()
	defer c.metrics.Timed(ctx, "INCRBYFLOAT")()
	v, err := c.redis().IncrByFloat(ctx, c.key(key), f).Result()
	if err != nil {
		c.metrics.RecordError(ctx, "INCRBYFLOAT", "incrbyfloat")
		return cty.NilVal, fmt.Errorf("redis_kv.increment: %w", err)
	}
	return cty.NumberFloatVal(v), nil
}

// --- encoding ---

func (c *RedisKVClient) encode(v cty.Value) (string, error) {
	// Bytes pass through verbatim — they are already an opaque binary
	// payload with no meaningful JSON representation.
	if b, ok := extractBytes(v); ok {
		return string(b), nil
	}
	return c.wireFormat.SerializeString(v)
}

// extractBytes returns the underlying bytes for a bytes capsule or bytes
// object value. It tolerates both shapes exported by the types package.
func extractBytes(v cty.Value) ([]byte, bool) {
	if v.IsNull() {
		return nil, false
	}
	t := v.Type()
	if t == vtypes.BytesCapsuleType {
		if b, err := vtypes.GetBytesFromCapsule(v); err == nil {
			return b.Data, true
		}
		return nil, false
	}
	if t.IsObjectType() && t.HasAttribute("_capsule") {
		if b, err := vtypes.GetBytesFromValue(v); err == nil {
			return b.Data, true
		}
	}
	return nil, false
}

func (c *RedisKVClient) decode(raw string) (cty.Value, error) {
	result, err := c.wireFormat.Deserialize([]byte(raw))
	if err != nil {
		return cty.NilVal, fmt.Errorf("redis_kv: decode: %w", err)
	}
	cv, ok := result.(cty.Value)
	if !ok {
		return cty.NilVal, fmt.Errorf("redis_kv: decode: expected cty.Value, got %T", result)
	}
	return cv, nil
}

func asString(v cty.Value, name string) (string, error) {
	if v.IsNull() {
		return "", fmt.Errorf("redis_kv: %s must not be null", name)
	}
	if v.Type() != cty.String {
		return "", fmt.Errorf("redis_kv: %s must be a string, got %s", name, v.Type().FriendlyName())
	}
	return v.AsString(), nil
}

// --- config processing ---

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	def := RedisKVDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &def)
	if diags.HasErrors() {
		return nil, diags
	}

	hashMode := def.HashMode != nil && *def.HashMode

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
			Summary:  "redis_kv: connection must reference a client \"redis\" block",
			Detail:   fmt.Sprintf("got %T", baseClient),
			Subject:  &r,
		}}
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
				Summary:  "redis_kv: invalid wire_format",
				Detail:   err.Error(),
				Subject:  def.WireFormat.Range().Ptr(),
			}}
		}
		wf = resolved
	}
	ctyWF := &cfg.CtyWireFormat{Inner: wf}

	var defaultTTL time.Duration
	if cfg.IsExpressionProvided(def.DefaultTTL) {
		d, dDiags := config.ParseDuration(def.DefaultTTL)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		defaultTTL = d
	}

	mp, mpDiags := cfg.ResolveMeterProvider(config, def.Metrics)
	if mpDiags.HasErrors() {
		return nil, mpDiags
	}

	client := &RedisKVClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		connector:  connector,
		keyPrefix:  def.KeyPrefix,
		defaultTTL: defaultTTL,
		wireFormat: ctyWF,
		hashMode:   hashMode,
		metrics:    newKVMetrics(clientName, mp),
	}

	return client, nil
}

// compile-time interface checks
var (
	_ richcty.Gettable      = (*RedisKVClient)(nil)
	_ richcty.Settable      = (*RedisKVClient)(nil)
	_ richcty.Incrementable = (*RedisKVClient)(nil)
)
