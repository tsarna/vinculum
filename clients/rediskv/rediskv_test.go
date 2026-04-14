package rediskv_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/clients/rediskv"
	cfg "github.com/tsarna/vinculum/config"
	vtypes "github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

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
	assert.Equal(t, cty.NumberIntVal(1), got.GetAttr("a"))
	assert.Equal(t, cty.StringVal("x"), got.GetAttr("b"))
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
	bytesVal := vtypes.NewBytesCapsule(data, "application/octet-stream")

	_, err := kv.Set(ctx, []cty.Value{cty.StringVal("b"), bytesVal})
	require.NoError(t, err)
	raw, _ := mr.Get("app:b")
	assert.Equal(t, string(data), raw)
}

func TestRawEncoding(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "kv" {
    connection     = client.base
    value_encoding = "raw"
}
`, mr.Addr())
	c := buildConfig(t, src)
	kv := c.Clients["redis_kv"]["kv"].(*rediskv.RedisKVClient)

	_, err := kv.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.NumberIntVal(5)})
	assert.Error(t, err, "raw encoding should reject non-strings")

	_, err = kv.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.StringVal("{not decoded}")})
	require.NoError(t, err)
	v, err := kv.Get(context.Background(), []cty.Value{cty.StringVal("k")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("{not decoded}"), v)
}

func TestJSONEncoding(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_kv" "kv" {
    connection     = client.base
    value_encoding = "json"
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

func TestHashModeRejected(t *testing.T) {
	src := `
client "redis" "base" { address = "localhost:1" }
client "redis_kv" "kv" {
    connection = client.base
    hash_mode  = true
}
`
	_, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "hash_mode not yet supported")
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
