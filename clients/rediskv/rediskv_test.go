package rediskv_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bytescty "github.com/tsarna/bytes-cty-type"
	"github.com/tsarna/vinculum/clients/rediskv"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// ctyAttr extracts a named attribute from a cty object or map value.
func ctyAttr(v cty.Value, name string) cty.Value {
	if v.Type().IsObjectType() {
		return v.GetAttr(name)
	}
	return v.Index(cty.StringVal(name))
}

func buildConfig(t *testing.T, src string) *cfg.Config {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	return c
}

func withMiniredis(t *testing.T, vclTpl string) (*cfg.Config, *miniredis.Miniredis, *rediskv.RedisKVClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(vclTpl, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["kv"].(*rediskv.RedisKVClient)
	return c, mr, kv
}

const basicTpl = `
client "redis" "base" {
    address = "%s"
}
client "redis_kv" "kv" {
    connection = client.base
    key_prefix = "app:"
}
`

func TestSetGetAuto(t *testing.T) {
	_, mr, kv := withMiniredis(t, basicTpl)
	ctx := context.Background()

	// string → verbatim
	_, err := kv.Set(ctx, []cty.Value{cty.StringVal("hello"), cty.StringVal("world")})
	require.NoError(t, err)
	raw, _ := mr.Get("app:hello")
	assert.Equal(t, "world", raw)

	v, err := kv.Get(ctx, []cty.Value{cty.StringVal("hello")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("world"), v)

	// object → JSON encode; auto-decode round-trips
	obj := cty.ObjectVal(map[string]cty.Value{
		"a": cty.NumberIntVal(1),
		"b": cty.StringVal("x"),
	})
	_, err = kv.Set(ctx, []cty.Value{cty.StringVal("obj"), obj})
	require.NoError(t, err)
	raw, _ = mr.Get("app:obj")
	assert.Contains(t, raw, `"a":1`)

	got, err := kv.Get(ctx, []cty.Value{cty.StringVal("obj")})
	require.NoError(t, err)
	// JSON round-trip produces float64 → cty number; compare numerically
	a := ctyAttr(got, "a")
	assert.True(t, a.AsBigFloat().IsInt())
	ai, _ := a.AsBigFloat().Int64()
	assert.Equal(t, int64(1), ai)
	assert.Equal(t, "x", ctyAttr(got, "b").AsString())
}

func TestGetMissingReturnsDefault(t *testing.T) {
	_, _, kv := withMiniredis(t, basicTpl)
	ctx := context.Background()

	v, err := kv.Get(ctx, []cty.Value{cty.StringVal("nope")})
	require.NoError(t, err)
	assert.True(t, v.IsNull())

	v, err = kv.Get(ctx, []cty.Value{cty.StringVal("nope"), cty.StringVal("fallback")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("fallback"), v)
}

func TestSetWithTTL(t *testing.T) {
	_, mr, kv := withMiniredis(t, basicTpl)
	ctx := context.Background()

	_, err := kv.Set(ctx, []cty.Value{cty.StringVal("k"), cty.StringVal("v"), cty.StringVal("5m")})
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, mr.TTL("app:k"))

	// numeric ttl (seconds)
	_, err = kv.Set(ctx, []cty.Value{cty.StringVal("n"), cty.StringVal("v"), cty.NumberIntVal(30)})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, mr.TTL("app:n"))

	// "0" → PERSIST strips expiry
	_, err = kv.Set(ctx, []cty.Value{cty.StringVal("k"), cty.StringVal("v2"), cty.StringVal("0")})
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), mr.TTL("app:k"))
}

func TestDefaultTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "kv" {
    connection  = client.base
    default_ttl = "10s"
}
`, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["kv"].(*rediskv.RedisKVClient)

	_, err := kv.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.StringVal("v")})
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, mr.TTL("k"))
}

func TestIncrementInt(t *testing.T) {
	_, mr, kv := withMiniredis(t, basicTpl)
	ctx := context.Background()

	v, err := kv.Increment(ctx, []cty.Value{cty.StringVal("ctr"), cty.NumberIntVal(1)})
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(1), v)

	v, err = kv.Increment(ctx, []cty.Value{cty.StringVal("ctr"), cty.NumberIntVal(4)})
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(5), v)

	raw, _ := mr.Get("app:ctr")
	assert.Equal(t, "5", raw)
}

func TestIncrementFloat(t *testing.T) {
	_, _, kv := withMiniredis(t, basicTpl)
	ctx := context.Background()

	v, err := kv.Increment(ctx, []cty.Value{cty.StringVal("f"), cty.NumberFloatVal(1.5)})
	require.NoError(t, err)
	f, _ := v.AsBigFloat().Float64()
	assert.InDelta(t, 1.5, f, 1e-9)
}

func TestBytesPassthrough(t *testing.T) {
	_, mr, kv := withMiniredis(t, basicTpl)
	ctx := context.Background()

	data := []byte{0x00, 0x01, 0xff, 'h', 'i'}
	bytesVal := bytescty.NewBytesCapsule(data, "application/octet-stream")

	_, err := kv.Set(ctx, []cty.Value{cty.StringVal("b"), bytesVal})
	require.NoError(t, err)
	raw, _ := mr.Get("app:b")
	assert.Equal(t, string(data), raw)
}

func TestStringWireFormat(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "kv" {
    connection  = client.base
    wire_format = "string"
}
`, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["kv"].(*rediskv.RedisKVClient)

	// Numbers serialize to their canonical string form
	_, err := kv.Set(context.Background(), []cty.Value{cty.StringVal("n"), cty.NumberIntVal(5)})
	require.NoError(t, err)
	raw, _ := mr.Get("n")
	assert.Equal(t, "5", raw)

	_, err = kv.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.StringVal("{not decoded}")})
	require.NoError(t, err)
	v, err := kv.Get(context.Background(), []cty.Value{cty.StringVal("k")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("{not decoded}"), v)
}

func TestJSONWireFormat(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "kv" {
    connection  = client.base
    wire_format = "json"
}
`, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["kv"].(*rediskv.RedisKVClient)

	// Strings get JSON-quoted under json mode.
	_, err := kv.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.StringVal("hi")})
	require.NoError(t, err)
	raw, _ := mr.Get("k")
	assert.Equal(t, `"hi"`, raw)

	v, err := kv.Get(context.Background(), []cty.Value{cty.StringVal("k")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("hi"), v)
}

func TestHashModeSetAndHGet(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "devices" {
    connection = client.base
    key_prefix = "dev:"
    hash_mode  = true
}
`, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["devices"].(*rediskv.RedisKVClient)
	ctx := context.Background()

	_, err := kv.Set(ctx, []cty.Value{cty.StringVal("abc"), cty.StringVal("last_seen"), cty.StringVal("2026-04-14")})
	require.NoError(t, err)

	v, err := kv.Get(ctx, []cty.Value{cty.StringVal("abc"), cty.StringVal("last_seen")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("2026-04-14"), v)

	assert.Equal(t, "2026-04-14", mr.HGet("dev:abc", "last_seen"))
}

func TestHashModeHGetAll(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "devices" {
    connection = client.base
    hash_mode  = true
}
`, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["devices"].(*rediskv.RedisKVClient)
	ctx := context.Background()

	// Populate two fields via HSET directly.
	mr.HSet("devA", "last_seen", "t1")
	mr.HSet("devA", "region", "us-east")

	got, err := kv.Get(ctx, []cty.Value{cty.StringVal("devA")})
	require.NoError(t, err)
	require.True(t, got.Type().IsObjectType())
	assert.Equal(t, cty.StringVal("t1"), got.GetAttr("last_seen"))
	assert.Equal(t, cty.StringVal("us-east"), got.GetAttr("region"))
}

func TestHashModeHGetMissingField(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "devices" {
    connection = client.base
    hash_mode  = true
}
`, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["devices"].(*rediskv.RedisKVClient)

	v, err := kv.Get(context.Background(), []cty.Value{cty.StringVal("devA"), cty.StringVal("missing")})
	require.NoError(t, err)
	assert.True(t, v.IsNull())
}

func TestMultipleKVSharedConnection(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "sessions" {
    connection = client.base
    key_prefix = "sess:"
}
client "redis_kv" "rate_limit" {
    connection = client.base
    key_prefix = "rl:"
}
`, mr.Addr())
	c := buildConfig(t, src)
	sessions := c.Clients["redis_kv"]["sessions"].(*rediskv.RedisKVClient)
	rate := c.Clients["redis_kv"]["rate_limit"].(*rediskv.RedisKVClient)

	ctx := context.Background()
	_, err := sessions.Set(ctx, []cty.Value{cty.StringVal("abc"), cty.StringVal("S")})
	require.NoError(t, err)
	_, err = rate.Set(ctx, []cty.Value{cty.StringVal("abc"), cty.StringVal("R")})
	require.NoError(t, err)

	s, _ := mr.Get("sess:abc")
	r, _ := mr.Get("rl:abc")
	assert.Equal(t, "S", s)
	assert.Equal(t, "R", r)
}
