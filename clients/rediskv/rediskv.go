// Package rediskv implements the `client "redis_kv"` block — a thin adapter
// that exposes Redis string operations through Vinculum's generic
// get()/set()/increment() interface via richcty.Gettable/Settable/Incrementable.

package rediskv

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	goredis "github.com/redis/go-redis/v9"
	"github.com/tsarna/go2cty2go"
	richcty "github.com/tsarna/rich-cty-types"
	redisclient "github.com/tsarna/vinculum/clients/redis"
	cfg "github.com/tsarna/vinculum/config"
	vtypes "github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterClientType("redis_kv", process)
}

type RedisKVDefinition struct {
	Connection    hcl.Expression `hcl:"connection"`
	KeyPrefix     string         `hcl:"key_prefix,optional"`
	DefaultTTL    hcl.Expression `hcl:"default_ttl,optional"`
	ValueEncoding string         `hcl:"value_encoding,optional"`
	HashMode      *bool          `hcl:"hash_mode,optional"`
	Metrics       hcl.Expression `hcl:"metrics,optional"`
	DefRange      hcl.Range      `hcl:",def_range"`
}

type valueEncoding int

const (
	encodingAuto valueEncoding = iota
	encodingRaw
	encodingJSON
)

// RedisKVClient is the runtime object exposed to HCL as a client capsule.
// It implements richcty.Gettable/Settable/Incrementable so that the generic
// get()/set()/increment() functions dispatch to the corresponding Redis
// commands.
type RedisKVClient struct {
	cfg.BaseClient
	connector  redisclient.RedisConnector
	keyPrefix  string
	defaultTTL time.Duration
	encoding   valueEncoding
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
	var defaultVal cty.Value = cty.NullVal(cty.DynamicPseudoType)
	if len(args) >= 2 {
		defaultVal = args[1]
	}

	raw, err := c.redis().Get(ctx, c.key(key)).Result()
	if err != nil {
		if err == goredis.Nil {
			return defaultVal, nil
		}
		return cty.NilVal, fmt.Errorf("redis_kv.get: %w", err)
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
	encoded, err := c.encode(args[1])
	if err != nil {
		return cty.NilVal, err
	}

	ttl, persist, err := c.resolveTTL(args)
	if err != nil {
		return cty.NilVal, err
	}

	if err := c.redis().Set(ctx, c.key(key), encoded, ttl).Err(); err != nil {
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
		v, err := c.redis().IncrBy(ctx, c.key(key), n).Result()
		if err != nil {
			return cty.NilVal, fmt.Errorf("redis_kv.increment: %w", err)
		}
		return cty.NumberIntVal(v), nil
	}
	f, _ := delta.Float64()
	v, err := c.redis().IncrByFloat(ctx, c.key(key), f).Result()
	if err != nil {
		return cty.NilVal, fmt.Errorf("redis_kv.increment: %w", err)
	}
	return cty.NumberFloatVal(v), nil
}

// --- encoding ---

func (c *RedisKVClient) encode(v cty.Value) (string, error) {
	// Bytes pass through verbatim in every mode — they are already an
	// opaque binary payload with no meaningful JSON representation, and
	// this matches MQTT/Kafka behavior.
	if b, ok := extractBytes(v); ok {
		return string(b), nil
	}

	switch c.encoding {
	case encodingRaw:
		if v.IsNull() {
			return "", nil
		}
		if v.Type() != cty.String {
			return "", fmt.Errorf("redis_kv.set: value_encoding=raw only accepts strings or bytes, got %s", v.Type().FriendlyName())
		}
		return v.AsString(), nil
	case encodingJSON:
		return jsonEncode(v)
	default: // auto
		if !v.IsNull() && v.Type() == cty.String {
			return v.AsString(), nil
		}
		return jsonEncode(v)
	}
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
	switch c.encoding {
	case encodingRaw:
		return cty.StringVal(raw), nil
	case encodingJSON:
		return jsonDecode(raw)
	default: // auto
		if looksLikeJSON(raw) {
			if v, err := jsonDecode(raw); err == nil {
				return v, nil
			}
		}
		return cty.StringVal(raw), nil
	}
}

func jsonEncode(v cty.Value) (string, error) {
	if v.IsNull() {
		return "null", nil
	}
	any, err := go2cty2go.CtyToAny(v)
	if err != nil {
		return "", fmt.Errorf("redis_kv: cty->any: %w", err)
	}
	b, err := json.Marshal(any)
	if err != nil {
		return "", fmt.Errorf("redis_kv: json encode: %w", err)
	}
	return string(b), nil
}

func jsonDecode(raw string) (cty.Value, error) {
	var any interface{}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&any); err != nil {
		return cty.NilVal, fmt.Errorf("redis_kv: json decode: %w", err)
	}
	return anyToCty(any)
}

// anyToCty converts JSON-decoded values (with UseNumber) into cty values.
// go2cty2go handles the main types but expects float64 for numbers; using
// json.Number preserves precision for integer counters round-tripped through
// Redis.
func anyToCty(v interface{}) (cty.Value, error) {
	switch x := v.(type) {
	case nil:
		return cty.NullVal(cty.DynamicPseudoType), nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return cty.NumberIntVal(i), nil
		}
		if f, err := x.Float64(); err == nil {
			return cty.NumberFloatVal(f), nil
		}
		bf, _, err := big.ParseFloat(x.String(), 10, 512, big.ToNearestEven)
		if err != nil {
			return cty.NilVal, fmt.Errorf("redis_kv: bad number %q: %w", x.String(), err)
		}
		return cty.NumberVal(bf), nil
	case string:
		return cty.StringVal(x), nil
	case bool:
		return cty.BoolVal(x), nil
	case []interface{}:
		if len(x) == 0 {
			return cty.EmptyTupleVal, nil
		}
		elems := make([]cty.Value, len(x))
		for i, e := range x {
			ev, err := anyToCty(e)
			if err != nil {
				return cty.NilVal, err
			}
			elems[i] = ev
		}
		return cty.TupleVal(elems), nil
	case map[string]interface{}:
		if len(x) == 0 {
			return cty.EmptyObjectVal, nil
		}
		m := make(map[string]cty.Value, len(x))
		for k, e := range x {
			ev, err := anyToCty(e)
			if err != nil {
				return cty.NilVal, err
			}
			m[k] = ev
		}
		return cty.ObjectVal(m), nil
	default:
		return cty.NilVal, fmt.Errorf("redis_kv: unsupported JSON type %T", v)
	}
}

func looksLikeJSON(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		switch r {
		case '{', '[', '"', '-', 't', 'f', 'n':
			return true
		}
		if r >= '0' && r <= '9' {
			return true
		}
		return false
	}
	return false
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

	if def.HashMode != nil && *def.HashMode {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_kv: hash_mode not yet supported",
			Detail:   "hash_mode is planned for a future phase",
			Subject:  &def.DefRange,
		}}
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
			Summary:  "redis_kv: connection must reference a client \"redis\" block",
			Detail:   fmt.Sprintf("got %T", baseClient),
			Subject:  &r,
		}}
	}

	enc := encodingAuto
	switch def.ValueEncoding {
	case "", "auto":
		enc = encodingAuto
	case "raw":
		enc = encodingRaw
	case "json":
		enc = encodingJSON
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "redis_kv: invalid value_encoding",
			Detail:   fmt.Sprintf("value_encoding must be \"auto\", \"raw\", or \"json\"; got %q", def.ValueEncoding),
			Subject:  &def.DefRange,
		}}
	}

	var defaultTTL time.Duration
	if cfg.IsExpressionProvided(def.DefaultTTL) {
		d, dDiags := config.ParseDuration(def.DefaultTTL)
		if dDiags.HasErrors() {
			return nil, dDiags
		}
		defaultTTL = d
	}

	client := &RedisKVClient{
		BaseClient: cfg.BaseClient{
			Name:     clientName,
			DefRange: def.DefRange,
		},
		connector:  connector,
		keyPrefix:  def.KeyPrefix,
		defaultTTL: defaultTTL,
		encoding:   enc,
	}

	return client, nil
}

// compile-time interface checks
var (
	_ richcty.Gettable      = (*RedisKVClient)(nil)
	_ richcty.Settable      = (*RedisKVClient)(nil)
	_ richcty.Incrementable = (*RedisKVClient)(nil)
)
