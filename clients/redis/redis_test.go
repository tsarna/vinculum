package redis_test

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	redisclient "github.com/tsarna/vinculum/clients/redis"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

//go:embed testdata/basic.vcl
var basicVCL []byte

//go:embed testdata/full.vcl
var fullVCL []byte


func TestRedisClientRegistered(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(basicVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	assert.Contains(t, c.Clients, "redis")
	assert.Contains(t, c.Clients["redis"], "myredis")

	rc, ok := c.Clients["redis"]["myredis"].(*redisclient.RedisClient)
	require.True(t, ok, "expected *RedisClient, got %T", c.Clients["redis"]["myredis"])
	assert.NotNil(t, rc.UniversalClient())
	assert.Equal(t, "myredis", rc.GetName())
}

func TestRedisClientConnectorInterface(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(basicVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	var conn redisclient.RedisConnector = c.Clients["redis"]["myredis"].(*redisclient.RedisClient)
	assert.NotNil(t, conn.UniversalClient())
}

func TestRedisClientFullConfig(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(fullVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	rc := c.Clients["redis"]["myredis"].(*redisclient.RedisClient)
	assert.NotNil(t, rc.UniversalClient())
}

func TestRedisClientInStartables(t *testing.T) {
	c, diags := cfg.NewConfig().WithSources(basicVCL).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	found := false
	for _, s := range c.Startables {
		if _, ok := s.(*redisclient.RedisClient); ok {
			found = true
			break
		}
	}
	assert.True(t, found, "RedisClient should be registered as Startable")
}

func TestRedisClientMissingAddress(t *testing.T) {
	src := []byte(`client "redis" "x" {}`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "address is required")
}

func TestRedisClientClusterParses(t *testing.T) {
	src := []byte(`
client "redis" "c" {
    mode      = "cluster"
    addresses = ["redis1:6379", "redis2:6379", "redis3:6379"]
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	rc := c.Clients["redis"]["c"].(*redisclient.RedisClient)
	assert.NotNil(t, rc.UniversalClient())
}

func TestRedisClientClusterRejectsDatabase(t *testing.T) {
	src := []byte(`
client "redis" "c" {
    mode      = "cluster"
    addresses = ["r1:6379"]
    database  = 2
}
`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "database not supported in cluster mode")
}

func TestRedisClientClusterRejectsAddress(t *testing.T) {
	src := []byte(`
client "redis" "c" {
    mode    = "cluster"
    address = "r1:6379"
}
`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "address not valid in cluster mode")
}

func TestRedisClientSentinelParses(t *testing.T) {
	src := []byte(`
client "redis" "s" {
    mode        = "sentinel"
    addresses   = ["sent1:26379", "sent2:26379"]
    master_name = "mymaster"
}
`)
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	rc := c.Clients["redis"]["s"].(*redisclient.RedisClient)
	assert.NotNil(t, rc.UniversalClient())
}

func TestRedisClientSentinelRequiresMasterName(t *testing.T) {
	src := []byte(`
client "redis" "s" {
    mode      = "sentinel"
    addresses = ["sent1:26379"]
}
`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "master_name")
}

func TestRedisClientInvalidMode(t *testing.T) {
	src := []byte(`
client "redis" "x" {
    mode    = "bogus"
    address = "localhost:6379"
}
`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "invalid mode")
}
