package redispubsub_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/clients/redispubsub"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

func buildConfig(t *testing.T, src string) *cfg.Config {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())
	return c
}

// rawSubscribe subscribes via a separate goredis client so we can assert on
// delivered messages out-of-band from the vinculum bus.
func rawSubscribe(t *testing.T, addr string, channels ...string) (<-chan *goredis.Message, func()) {
	t.Helper()
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	ps := c.Subscribe(context.Background(), channels...)
	_, err := ps.Receive(context.Background())
	require.NoError(t, err)
	return ps.Channel(), func() {
		_ = ps.Close()
		_ = c.Close()
	}
}

func recv(t *testing.T, ch <-chan *goredis.Message) *goredis.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
		return nil
	}
}

func TestPublisherAddressingFanout(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_pubsub" "rps" {
    connection = client.base
    publisher "main" {}
    publisher "alt"  {}
}
`, mr.Addr())
	c := buildConfig(t, src)

	wrapper, ok := c.Clients["redis_pubsub"]["rps"].(*redispubsub.RedisPubSubClient)
	require.True(t, ok)

	// Fan-out publish via the wrapper sends to both publishers → same channel.
	mainCh, stopMain := rawSubscribe(t, mr.Addr(), "fan/out")
	defer stopMain()

	require.NoError(t, wrapper.OnEvent(context.Background(), "fan/out", "x", nil))

	// Two publishers both publish verbatim → same channel receives twice.
	recv(t, mainCh)
	recv(t, mainCh)
}

func TestPublisherDynamicChannelMapping(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_pubsub" "rps" {
    connection = client.base

    publisher "main" {
        channel_mapping {
            pattern = "device/+deviceId/status"
            channel = "devices.${ctx.fields.deviceId}.status"
        }
    }
}
`, mr.Addr())
	c := buildConfig(t, src)
	wrapper := c.Clients["redis_pubsub"]["rps"].(*redispubsub.RedisPubSubClient)

	ch, stop := rawSubscribe(t, mr.Addr(), "devices.abc123.status")
	defer stop()

	require.NoError(t, wrapper.OnEvent(context.Background(), "device/abc123/status", "up", nil))
	m := recv(t, ch)
	assert.Equal(t, "devices.abc123.status", m.Channel)
	assert.JSONEq(t, `"up"`, m.Payload)
}

func TestPublisherDefaultIgnore(t *testing.T) {
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
client "redis" "base" { address = "%s" }
client "redis_pubsub" "rps" {
    connection = client.base
    publisher "main" {
        default_channel_transform = "ignore"
    }
}
`, mr.Addr())
	c := buildConfig(t, src)
	wrapper := c.Clients["redis_pubsub"]["rps"].(*redispubsub.RedisPubSubClient)

	// No subscriber, no error — just silently drop.
	require.NoError(t, wrapper.OnEvent(context.Background(), "anything", "x", nil))
}

func TestPublisherViaBusSubscription(t *testing.T) {
	// End-to-end: a `subscription` block routes bus events to the publisher
	// capsule, confirming the cty fan-out addressing works.
	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
bus "main" {}

client "redis" "base" { address = "%s" }
client "redis_pubsub" "rps" {
    connection = client.base
    publisher "main" {}
}

subscription "to_redis" {
    target     = bus.main
    topics     = ["alerts/#"]
    subscriber = client.rps.publisher.main
}
`, mr.Addr())
	c := buildConfig(t, src)

	// Start the bus and run lifecycle hooks so subscriptions get wired.
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	t.Cleanup(func() {
		for i := len(c.Stoppables) - 1; i >= 0; i-- {
			_ = c.Stoppables[i].Stop()
		}
	})

	ch, stop := rawSubscribe(t, mr.Addr(), "alerts/fire")
	defer stop()

	b := c.Buses["main"]
	require.NoError(t, b.PublishSync(context.Background(), "alerts/fire", map[string]any{"level": "high"}))
	m := recv(t, ch)
	assert.Equal(t, "alerts/fire", m.Channel)
	assert.Contains(t, string(m.Payload), `"level":"high"`)
}

// Compile-time check that the wrapper really is a bus.Subscriber.
var _ bus.Subscriber = (*redispubsub.RedisPubSubClient)(nil)

// ── Phase 5: subscribers ──────────────────────────────────────────────────────

func startLifecycle(t *testing.T, c *cfg.Config) {
	t.Helper()
	for _, s := range c.Startables {
		require.NoError(t, s.Start())
	}
	t.Cleanup(func() {
		for i := len(c.Stoppables) - 1; i >= 0; i-- {
			_ = c.Stoppables[i].Stop()
		}
	})
}

func TestSubscriberRedisToBus(t *testing.T) {
	mr := miniredis.RunT(t)

	// Topic transform remaps every `devices.*` channel to `device/<segment>/seen`.
	src := fmt.Sprintf(`
bus "main" {}

client "redis" "base" { address = "%s" }
client "redis_pubsub" "rps" {
    connection = client.base

    subscriber "in" {
        subscriber = bus.main

        channel_subscription {
            channel = "alerts"
        }
        channel_subscription {
            channel        = "devices.*"
            vinculum_topic = "device/${ctx.topic}/seen"
        }
    }
}
`, mr.Addr())
	c := buildConfig(t, src)
	startLifecycle(t, c)

	received := make(chan struct {
		topic string
		msg   any
	}, 4)
	err := c.Buses["main"].Subscribe(context.Background(), "#", busSubFunc(func(topic string, msg any) {
		received <- struct {
			topic string
			msg   any
		}{topic, msg}
	}))
	require.NoError(t, err)

	// Publish two channels via a fresh goredis client.
	pub := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer pub.Close()
	require.NoError(t, pub.Publish(context.Background(), "alerts", `{"lvl":"high"}`).Err())
	require.NoError(t, pub.Publish(context.Background(), "devices.abc", "up").Err())

	got := map[string]any{}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-received:
			got[ev.topic] = ev.msg
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for event %d (got so far: %v)", i+1, got)
		}
	}
	assert.Equal(t, map[string]any{"lvl": "high"}, got["alerts"])
	assert.Equal(t, []byte("up"), got["device/devices.abc/seen"])
}

// busSubFunc wraps a handler into a bus.Subscriber.
type busSubFuncT struct {
	bus.BaseSubscriber
	fn func(topic string, msg any)
}

func (b *busSubFuncT) OnEvent(_ context.Context, topic string, msg any, _ map[string]string) error {
	b.fn(topic, msg)
	return nil
}
func busSubFunc(fn func(topic string, msg any)) bus.Subscriber {
	return &busSubFuncT{fn: fn}
}

