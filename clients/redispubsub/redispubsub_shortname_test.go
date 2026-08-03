package redispubsub_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestSubscriberStartsOnAShortChannelName covers a go-redis regression that
// Vinculum shipped: on v9.21.0 the runtime never came up at all for a config
// whose only Redis subscription named a short channel.
//
// v9.21.0 changed proto.Reader.PeekPushNotificationName from a peek clamped to
// what was already buffered into an unconditional bufio Peek(36). A subscribe
// confirmation is `>3\r\n$9\r\nsubscribe\r\n$N\r\n<channel>\r\n:1\r\n` — 29 bytes plus
// the channel name — so subscribing to a channel of six characters or fewer
// produced a frame with no 36th byte. Nothing more arrives until someone
// publishes, and the read carries no deadline, so Start() blocked forever on a
// config that parsed and validated fine. Fixed in v9.22.0; see
// https://github.com/redis/go-redis/issues/3935.
//
// TestSubscriberRedisToBus does not catch it. It declares an exact channel and
// a pattern, so two confirmations pipeline into the buffer and clear 36 bytes
// between them — it passes on a broken version by accident.
func TestSubscriberStartsOnAShortChannelName(t *testing.T) {
	mr := miniredis.RunT(t)

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
    }
}
`, mr.Addr())

	done := make(chan struct{})
	go func() {
		defer close(done)
		c := buildConfig(t, src)
		startLifecycle(t, c)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("startup hung subscribing to a single short channel; " +
			"check which version of github.com/redis/go-redis/v9 is in go.mod")
	}
}
