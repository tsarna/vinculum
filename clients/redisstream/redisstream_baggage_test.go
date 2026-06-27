package redisstream_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/clients/redisstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
)

type baggageRecorder struct {
	bus.BaseSubscriber
	once sync.Once
	got  chan map[string]string
}

func (r *baggageRecorder) OnEvent(ctx context.Context, _ string, _ any, _ map[string]string) error {
	m := map[string]string{}
	for _, mem := range baggage.FromContext(ctx).Members() {
		m[mem.Key()] = mem.Value()
	}
	r.once.Do(func() { r.got <- m })
	return nil
}

// runRedisStreamBaggageRoundTrip produces an entry with baggage on the context
// (the producer injects it as a stream field), consumes it through a real
// redis_stream consumer with the given baggage{} block, and returns the baggage
// the delivered action context actually sees. Exercises the whole inbound path:
// stream field → extraction → the consumer's baggage filter → bus → subscriber.
func runRedisStreamBaggageRoundTrip(t *testing.T, baggageBlock string) map[string]string {
	t.Helper()

	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	mr := miniredis.RunT(t)
	src := fmt.Sprintf(`
bus "main" {}

client "redis" "base" { address = "%s" }
client "redis_stream" "rs" {
    connection = client.base

    producer "out" { stream = "events" }

    consumer "in" {
        stream        = "events"
        group         = "g"
        block_timeout = "100ms"
        subscriber    = bus.main
%s
    }
}
`, mr.Addr(), baggageBlock)
	c := buildConfig(t, src)

	rec := &baggageRecorder{got: make(chan map[string]string, 1)}
	require.NoError(t, c.Buses["main"].Subscribe(context.Background(), "#", rec))

	startLifecycle(t, c)

	// Produce with baggage on the context; the producer injects it as a stream
	// entry field via the global propagator.
	m0, _ := baggage.NewMemberRaw("tenant_id", "acme")
	m1, _ := baggage.NewMemberRaw("secret", "x")
	bg, _ := baggage.New(m0, m1)
	ctx := baggage.ContextWithBaggage(context.Background(), bg)

	wrapper := c.Clients["redis_stream"]["rs"].(*redisstream.RedisStreamClient)
	require.NoError(t, wrapper.OnEvent(ctx, "whatever", map[string]any{"k": 1}, nil))

	select {
	case got := <-rec.got:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the consumed message")
		return nil
	}
}

func TestStreamConsumeStripsBaggageByDefault(t *testing.T) {
	assert.Empty(t, runRedisStreamBaggageRoundTrip(t, ""))
}

func TestStreamConsumeAllowsListedBaggage(t *testing.T) {
	got := runRedisStreamBaggageRoundTrip(t, `        baggage { allow = ["tenant_id"] }`)
	assert.Equal(t, map[string]string{"tenant_id": "acme"}, got)
}

func TestStreamConsumePassthroughTrustsAll(t *testing.T) {
	got := runRedisStreamBaggageRoundTrip(t, `        baggage { passthrough = true }`)
	assert.Equal(t, map[string]string{"tenant_id": "acme", "secret": "x"}, got)
}
