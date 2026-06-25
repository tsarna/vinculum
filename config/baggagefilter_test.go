package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/hclutil"
	"go.opentelemetry.io/otel/baggage"
	"go.uber.org/zap"
)

// recordingSubscriber captures the context, topic, and delegation calls it sees.
type recordingSubscriber struct {
	bus.BaseSubscriber
	gotCtx        context.Context
	onEventCalls  int
	subscribed    string
	passedThrough bool
}

func (r *recordingSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	r.gotCtx = ctx
	r.onEventCalls++
	return nil
}

func (r *recordingSubscriber) OnSubscribe(ctx context.Context, topic string) error {
	r.subscribed = topic
	return nil
}

func (r *recordingSubscriber) PassThrough(msg bus.EventBusMessage) error {
	r.passedThrough = true
	return nil
}

func ctxWithBaggage(t *testing.T, kv map[string]string) context.Context {
	t.Helper()
	members := make([]baggage.Member, 0, len(kv))
	for k, v := range kv {
		m, err := baggage.NewMemberRaw(k, v)
		require.NoError(t, err)
		members = append(members, m)
	}
	bg, err := baggage.New(members...)
	require.NoError(t, err)
	return baggage.ContextWithBaggage(context.Background(), bg)
}

func deliveredKeys(t *testing.T, inner *recordingSubscriber) map[string]string {
	t.Helper()
	require.Equal(t, 1, inner.onEventCalls)
	out := map[string]string{}
	for _, m := range baggage.FromContext(inner.gotCtx).Members() {
		out[m.Key()] = m.Value()
	}
	return out
}

func TestBaggageFilterSubscriber_StripsByDefault(t *testing.T) {
	inner := &recordingSubscriber{}
	// nil filter == secure default == strip all.
	sub := NewBaggageFilterSubscriber(nil, inner, zap.NewNop())
	ctx := ctxWithBaggage(t, map[string]string{"tenant_id": "acme", "secret": "x"})

	require.NoError(t, sub.OnEvent(ctx, "topic", "msg", nil))
	assert.Empty(t, deliveredKeys(t, inner))
}

func TestBaggageFilterSubscriber_Passthrough(t *testing.T) {
	inner := &recordingSubscriber{}
	sub := NewBaggageFilterSubscriber(&hclutil.BaggageFilterConfig{Passthrough: true}, inner, zap.NewNop())
	ctx := ctxWithBaggage(t, map[string]string{"tenant_id": "acme", "secret": "x"})

	require.NoError(t, sub.OnEvent(ctx, "topic", "msg", nil))
	assert.Equal(t, map[string]string{"tenant_id": "acme", "secret": "x"}, deliveredKeys(t, inner))
}

func TestBaggageFilterSubscriber_Allow(t *testing.T) {
	inner := &recordingSubscriber{}
	sub := NewBaggageFilterSubscriber(&hclutil.BaggageFilterConfig{Allow: []string{"tenant_id"}}, inner, zap.NewNop())
	ctx := ctxWithBaggage(t, map[string]string{"tenant_id": "acme", "secret": "x"})

	require.NoError(t, sub.OnEvent(ctx, "topic", "msg", nil))
	assert.Equal(t, map[string]string{"tenant_id": "acme"}, deliveredKeys(t, inner))
}

func TestBaggageFilterSubscriber_Delegates(t *testing.T) {
	inner := &recordingSubscriber{}
	sub := NewBaggageFilterSubscriber(nil, inner, zap.NewNop())

	require.NoError(t, sub.OnSubscribe(context.Background(), "t/#"))
	assert.Equal(t, "t/#", inner.subscribed)

	require.NoError(t, sub.PassThrough(bus.EventBusMessage{}))
	assert.True(t, inner.passedThrough)
}
