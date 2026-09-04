package vws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	vwspkg "github.com/tsarna/vinculum-vws"
	cfg "github.com/tsarna/vinculum/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
)

// A connected VWS client is untrusted, and every inbound frame carries its own
// headers. The connection extracts trace context from them per message, so
// without a filter anything a peer writes in `baggage` reaches ctx.baggage in
// every handler the message touches — and is re-propagated on outbound calls
// made from there.
//
// These tests go through a real WebSocket to a config-built server, because the
// mechanism is spread across three places that each look innocent alone: the
// global propagator a `client "otlp"` installs, vinculum-vws extracting through
// it, and vinculum publishing the derived context. Anything ending in a stand-in
// for one of them would pass while the real path leaked.

// recordingSubscriber captures the context each delivery arrives with.
type recordingSubscriber struct {
	bus.BaseSubscriber
	mu   sync.Mutex
	ctxs []context.Context
}

func (r *recordingSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctxs = append(r.ctxs, ctx)
	return nil
}

func (r *recordingSubscriber) delivered() []context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]context.Context(nil), r.ctxs...)
}

// publishWithBaggage boots the given vws server config, sends one client frame
// carrying a baggage header, and returns the baggage the bus subscriber saw.
func publishWithBaggage(t *testing.T, vcl string) map[string]string {
	t.Helper()

	// Baggage crosses a VWS hop only when something has installed a propagator
	// that carries it — in production, a `client "otlp"` block. Restore whatever
	// was there, so this test does not decide the behaviour of another.
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Build() has already started it.
	eventBus := c.Buses["main"]
	t.Cleanup(func() { _ = eventBus.Stop() })

	rec := &recordingSubscriber{}
	require.NoError(t, eventBus.Subscribe(context.Background(), "from/client", rec))

	srv := c.Servers["vws"]["ws"].(*VinculumWebsocketServer)
	ts := httptest.NewServer(srv.GetHandler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+ts.URL[len("http"):], nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	frame, err := json.Marshal(vwspkg.WireMessage{
		Kind:    vwspkg.MessageKindEvent,
		Topic:   "from/client",
		Data:    "hello",
		Headers: map[string]string{"baggage": "tenant_id=acme,internal_role=admin"},
	})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, frame))

	var seen []context.Context
	require.Eventually(t, func() bool {
		seen = rec.delivered()
		return len(seen) == 1
	}, 5*time.Second, 10*time.Millisecond, "the client's message should reach the bus")

	out := map[string]string{}
	for _, m := range baggage.FromContext(seen[0]).Members() {
		out[m.Key()] = m.Value()
	}
	return out
}

// The default. A public edge needs no block at all to be safe, which is what
// doc/baggage.md promises of every other inbound surface and did not deliver
// here.
func TestVwsStripsInboundBaggageByDefault(t *testing.T) {
	got := publishWithBaggage(t, `
bus "main" {}

server "vws" "ws" {
    bus        = bus.main
    allow_send = true
}
`)
	assert.Empty(t, got, "inbound baggage from an untrusted client must not reach the bus")
}

// An empty block is the same as no block: it declares the decision rather than
// changing it.
func TestVwsStripsInboundBaggageWithEmptyBlock(t *testing.T) {
	got := publishWithBaggage(t, `
bus "main" {}

server "vws" "ws" {
    bus        = bus.main
    allow_send = true

    baggage {}
}
`)
	assert.Empty(t, got, "an empty baggage block is the secure default written down")
}

// Opting in is what the block is for: a trusted peer's baggage passes through.
func TestVwsPassthroughKeepsInboundBaggage(t *testing.T) {
	got := publishWithBaggage(t, `
bus "main" {}

server "vws" "ws" {
    bus        = bus.main
    allow_send = true

    baggage { passthrough = true }
}
`)
	assert.Equal(t, map[string]string{"tenant_id": "acme", "internal_role": "admin"}, got,
		"passthrough should trust the peer entirely")
}

// And the middle ground, which is the one a real deployment wants: name the
// keys this configuration is willing to read.
func TestVwsAllowKeepsOnlyListedKeys(t *testing.T) {
	got := publishWithBaggage(t, `
bus "main" {}

server "vws" "ws" {
    bus        = bus.main
    allow_send = true

    baggage { allow = ["tenant_id"] }
}
`)
	assert.Equal(t, map[string]string{"tenant_id": "acme"}, got,
		"allow should keep exactly the named keys and drop the rest")
}
