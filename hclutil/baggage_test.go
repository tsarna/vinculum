package hclutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
)

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

func keys(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, m := range baggage.FromContext(ctx).Members() {
		out[m.Key()] = m.Value()
	}
	return out
}

func TestBaggageFilterAllow(t *testing.T) {
	c := &BaggageFilterConfig{Allow: []string{"tenant_id"}}
	ctx := ctxWithBaggage(t, map[string]string{"tenant_id": "acme", "secret": "x"})
	got := keys(c.FilterContext(ctx, nil))
	assert.Equal(t, map[string]string{"tenant_id": "acme"}, got)
}

func TestBaggageFilterDeny(t *testing.T) {
	c := &BaggageFilterConfig{Deny: []string{"internal.", "debug."}}
	ctx := ctxWithBaggage(t, map[string]string{
		"tenant_id":      "acme",
		"internal.trace": "1",
		"debug.flag":     "on",
	})
	got := keys(c.FilterContext(ctx, nil))
	assert.Equal(t, map[string]string{"tenant_id": "acme"}, got)
}

func TestBaggageFilterMaxEntries(t *testing.T) {
	one := 1
	// Trust all three via allow, but cap at 1 entry.
	c := &BaggageFilterConfig{Allow: []string{"a", "b", "c"}, MaxEntries: &one}
	ctx := ctxWithBaggage(t, map[string]string{"a": "1", "b": "2", "c": "3"})
	got := keys(c.FilterContext(ctx, nil))
	assert.Len(t, got, 1)
}

func TestBaggageFilterDefaultStrips(t *testing.T) {
	ctx := ctxWithBaggage(t, map[string]string{"a": "1"})
	// Nil receiver = secure default = strip everything.
	assert.Empty(t, keys((*BaggageFilterConfig)(nil).FilterContext(ctx, nil)))
	// Empty block = same as no block = strip everything.
	assert.Empty(t, keys((&BaggageFilterConfig{}).FilterContext(ctx, nil)))
}

func TestBaggageFilterPassthrough(t *testing.T) {
	c := &BaggageFilterConfig{Passthrough: true}
	ctx := ctxWithBaggage(t, map[string]string{"a": "1", "b": "2"})
	got := keys(c.FilterContext(ctx, nil))
	assert.Equal(t, map[string]string{"a": "1", "b": "2"}, got)
}

func TestBaggageFilterValidateMutualExclusion(t *testing.T) {
	c := &BaggageFilterConfig{Allow: []string{"a"}, Deny: []string{"b"}}
	diags := c.Validate()
	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), "either allow or deny")

	// passthrough cannot combine with allow/deny.
	pd := (&BaggageFilterConfig{Passthrough: true, Allow: []string{"a"}}).Validate()
	require.True(t, pd.HasErrors())
	assert.Contains(t, pd.Error(), "passthrough")

	// Each alone is fine; nil is fine.
	assert.False(t, (&BaggageFilterConfig{Allow: []string{"a"}}).Validate().HasErrors())
	assert.False(t, (&BaggageFilterConfig{Deny: []string{"b"}}).Validate().HasErrors())
	assert.False(t, (&BaggageFilterConfig{Passthrough: true}).Validate().HasErrors())
	assert.False(t, (*BaggageFilterConfig)(nil).Validate().HasErrors())
}
