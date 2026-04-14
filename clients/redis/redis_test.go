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

//go:embed testdata/cluster_rejected.vcl
var clusterVCL []byte

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

func TestRedisClientClusterRejected(t *testing.T) {
	_, diags := cfg.NewConfig().WithSources(clusterVCL).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "not yet supported")
}

func TestRedisClientMissingAddress(t *testing.T) {
	src := []byte(`client "redis" "x" {}`)
	_, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "address is required")
}
