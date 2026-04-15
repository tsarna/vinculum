package redisstream_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/clients/redisstream"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

func buildConfig(t *testing.T, src string) *cfg.Config {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	return c
}

func readAll(t *testing.T, addr, streamName string) []goredis.XMessage {
	t.Helper()
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	defer c.Close()
	res, err := c.XRange(context.Background(), streamName, "-", "+").Result()
	require.NoError(t, err)
	return res
}

func TestStreamProducerStaticName(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" {
        stream = "events"
        maxlen = 100
    }
}
`, mr.Addr())
	c := buildConfig(t, src)
	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)

	require.NoError(t, wrapper.OnEvent(context.Background(), "anything", map[string]any{"x": 1}, nil))
	entries := readAll(t, mr.Addr(), "events")
	require.Len(t, entries, 1)
	assert.JSONEq(t, `{"x":1}`, entries[0].Values["data"].(string))
	assert.Equal(t, "anything", entries[0].Values["topic"])
}

func TestStreamProducerDynamicNameFromFields(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" {
        stream = "events:${ctx.fields.region}"
    }
}
`, mr.Addr())
	c := buildConfig(t, src)
	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)

	require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi",
		map[string]string{"region": "us-east"}))
	entries := readAll(t, mr.Addr(), "events:us-east")
	require.Len(t, entries, 1)
}

func TestStreamProducerDefaultTopicTransform(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base
    producer "out" {}
}
`, mr.Addr())
	c := buildConfig(t, src)
	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)

	// No `stream` expression → "/" → ":" substitution.
	require.NoError(t, wrapper.OnEvent(context.Background(), "events/foo/bar", "hi", nil))
	entries := readAll(t, mr.Addr(), "events:foo:bar")
	require.Len(t, entries, 1)
}

func TestStreamProducerFieldsModeNested(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base
    producer "out" {
        stream      = "events"
        fields_mode = "nested"
    }
}
`, mr.Addr())
	c := buildConfig(t, src)
	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)

	require.NoError(t, wrapper.OnEvent(context.Background(), "x", "hi",
		map[string]string{"a": "1", "b": "2"}))
	entries := readAll(t, mr.Addr(), "events")
	require.Len(t, entries, 1)
	_, hasFlatA := entries[0].Values["a"]
	assert.False(t, hasFlatA)
	assert.Contains(t, entries[0].Values, "fields")
}

func TestStreamProducerInvalidFieldsModeRejected(t *testing.T) {
	src := `
client "redis" "base" { address = "localhost:1" }
client "redis_stream" "rs" {
    connection = client.base
    producer "out" {
        stream      = "events"
        fields_mode = "weird"
    }
}
`
	_, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "invalid fields_mode")
}
