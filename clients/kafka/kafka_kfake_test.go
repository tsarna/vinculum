package kafka_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	_ "github.com/tsarna/vinculum/clients/otlp" // register client "otlp" for the test config
	cfg "github.com/tsarna/vinculum/config"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
)

// captureSubscriber records the baggage on the context of the first event it
// receives and signals via a channel.
type captureSubscriber struct {
	bus.BaseSubscriber
	once sync.Once
	got  chan map[string]string
}

func (c *captureSubscriber) OnEvent(ctx context.Context, _ string, _ any, _ map[string]string) error {
	m := map[string]string{}
	for _, mem := range baggage.FromContext(ctx).Members() {
		m[mem.Key()] = mem.Value()
	}
	c.once.Do(func() { c.got <- m })
	return nil
}

// runKfakeBaggageRoundTrip produces a Kafka record carrying a baggage header,
// consumes it through a real vinculum kafka receiver (with the given baggage{}
// block), and returns the baggage the delivered action context actually sees.
// This exercises the whole inbound path: record header → kotel extraction →
// the receiver's baggage filter → subscriber.
func runKfakeBaggageRoundTrip(t *testing.T, baggageBlock string) map[string]string {
	t.Helper()

	// kotel extracts via the global propagator; install the composite that
	// includes Baggage, and restore it afterward.
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	// In-process fake Kafka broker with the source topic pre-created.
	cluster, err := kfake.NewCluster(kfake.SeedTopics(1, "intopic"))
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	addr := cluster.ListenAddrs()[0]

	// Quietly absorb OTLP exports so the client has a real TracerProvider
	// (required for the kotel extraction hook to be installed) without noise.
	otlpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(otlpSrv.Close)

	src := fmt.Sprintf(`
bus "main" {}

client "otlp" "t" {
  endpoint     = %q
  service_name = "kfake-test"
}

client "kafka" "k" {
  brokers = [%q]
  tracing = client.t

  receiver "r" {
    group_id     = "g"
    start_offset = "earliest"
    subscriber   = bus.main
%s
    subscription "intopic" {
      vinculum_topic = "in"
    }
  }
}
`, otlpSrv.URL, addr, baggageBlock)

	config, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	for _, s := range config.Startables {
		require.NoError(t, s.Start())
	}
	t.Cleanup(func() {
		for i := len(config.Stoppables) - 1; i >= 0; i-- {
			_ = config.Stoppables[i].Stop()
		}
	})

	cap := &captureSubscriber{got: make(chan map[string]string, 1)}
	require.NoError(t, config.Buses["main"].Subscribe(context.Background(), "#", cap))

	// Produce one record with a W3C baggage header.
	prod, err := kgo.NewClient(kgo.SeedBrokers(addr))
	require.NoError(t, err)
	t.Cleanup(prod.Close)
	rec := &kgo.Record{
		Topic: "intopic",
		Value: []byte(`"hello"`),
		Headers: []kgo.RecordHeader{
			{Key: "baggage", Value: []byte("tenant_id=acme,secret=x")},
		},
	}
	require.NoError(t, prod.ProduceSync(context.Background(), rec).FirstErr())

	select {
	case got := <-cap.got:
		return got
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for the consumed message")
		return nil
	}
}

func TestKfakeConsumeStripsBaggageByDefault(t *testing.T) {
	// No baggage block → secure default → all inbound baggage stripped.
	got := runKfakeBaggageRoundTrip(t, "")
	assert.Empty(t, got)
}

func TestKfakeConsumeAllowsListedBaggage(t *testing.T) {
	// allow = ["tenant_id"] → only that key survives; "secret" is dropped.
	got := runKfakeBaggageRoundTrip(t, `    baggage { allow = ["tenant_id"] }`)
	assert.Equal(t, map[string]string{"tenant_id": "acme"}, got)
}

func TestKfakeConsumePassthroughTrustsAll(t *testing.T) {
	got := runKfakeBaggageRoundTrip(t, `    baggage { passthrough = true }`)
	assert.Equal(t, map[string]string{"tenant_id": "acme", "secret": "x"}, got)
}
