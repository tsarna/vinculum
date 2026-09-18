package redis_test

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	redisclient "github.com/tsarna/vinculum/clients/redis"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// acceptCounter is a listener that counts the connections made to it and
// closes each, which is all a dial needs to be observed.
func acceptCounter(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	var n atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			conn.Close()
		}
	}()
	return ln.Addr().String(), &n
}

// min_idle_conns makes go-redis dial as the client is constructed. A config
// that is built and never started — `vinculum check`, man::check — must not
// connect to the address it names.
func TestRedisClientDoesNotDialBeforeStart(t *testing.T) {
	addr, accepted := acceptCounter(t)
	src := fmt.Appendf(nil, `
client "redis" "r" {
  address        = %q
  min_idle_conns = 2
}
`, addr)

	t.Run("built and discarded, never started", func(t *testing.T) {
		c, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
		require.False(t, diags.HasErrors(), "%s", diags)

		time.Sleep(200 * time.Millisecond)
		assert.Zero(t, accepted.Load(), "no connection before Start")

		c.Discard()
		time.Sleep(100 * time.Millisecond)
		assert.Zero(t, accepted.Load(), "and none on the way out")
	})

	// `vinculum test --no-serve` uses a config it never starts.
	t.Run("used without being started, it connects", func(t *testing.T) {
		mr := miniredis.RunT(t)
		c, diags := cfg.NewConfig().WithSources(fmt.Appendf(nil, `
client "redis" "r" {
  address        = %q
  min_idle_conns = 2
}
`, mr.Addr())).WithLogger(zap.NewNop()).Build()
		require.False(t, diags.HasErrors(), "%s", diags)
		t.Cleanup(c.Discard)

		client := c.Clients["redis"]["r"].(*redisclient.RedisClient).UniversalClient()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Set(ctx, "k", "v", 0).Err())
	})

	t.Run("started, the held dials go ahead", func(t *testing.T) {
		c, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
		require.False(t, diags.HasErrors(), "%s", diags)
		t.Cleanup(c.Discard)

		// The listener is not Redis, so the ping fails; the dials are what count.
		for _, s := range c.Startables {
			_ = s.Start()
		}
		assert.Eventually(t, func() bool { return accepted.Load() > 0 },
			5*time.Second, 20*time.Millisecond)
	})
}
