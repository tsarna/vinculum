package types

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/baggage"
)

// newTestBaggage returns a baggage capsule over a fresh context cell plus the
// pointer, so tests can observe writes the capsule makes through it.
func newTestBaggage() (*Baggage, *context.Context) {
	ctx := context.Background()
	p := &ctx
	return &Baggage{ctxp: p}, p
}

func memberValue(ctx context.Context, key string) (string, bool) {
	m := baggage.FromContext(ctx).Member(key)
	if m.Key() == "" {
		return "", false
	}
	return m.Value(), true
}

func TestBaggageSetGet(t *testing.T) {
	b, p := newTestBaggage()

	// set(key, value) returns the value and writes through the pointer.
	got, err := b.Set(context.Background(), []cty.Value{cty.StringVal("tenant_id"), cty.StringVal("acme")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("acme"), got)
	v, ok := memberValue(*p, "tenant_id")
	assert.True(t, ok)
	assert.Equal(t, "acme", v)

	// get(key) reads it back.
	got, err = b.Get(context.Background(), []cty.Value{cty.StringVal("tenant_id")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("acme"), got)

	// get(absent) → null; get(absent, default) → default.
	got, err = b.Get(context.Background(), []cty.Value{cty.StringVal("nope")})
	require.NoError(t, err)
	assert.True(t, got.IsNull())
	got, err = b.Get(context.Background(), []cty.Value{cty.StringVal("nope"), cty.StringVal("dflt")})
	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("dflt"), got)
}

func TestBaggageGetFullMapAndEmpty(t *testing.T) {
	b, _ := newTestBaggage()

	// Empty → typed empty map, never null.
	got, err := b.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, got.Type().IsMapType())
	assert.Equal(t, 0, got.LengthInt())

	_, err = b.Set(context.Background(), []cty.Value{cty.StringVal("a"), cty.StringVal("1")})
	require.NoError(t, err)
	_, err = b.Set(context.Background(), []cty.Value{cty.StringVal("b"), cty.StringVal("2")})
	require.NoError(t, err)

	got, err = b.Get(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]cty.Value{
		"a": cty.StringVal("1"),
		"b": cty.StringVal("2"),
	}, got.AsValueMap())
}

func TestBaggageSetNullRemoves(t *testing.T) {
	b, p := newTestBaggage()
	_, err := b.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.StringVal("v")})
	require.NoError(t, err)

	got, err := b.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.NullVal(cty.String)})
	require.NoError(t, err)
	assert.True(t, got.IsNull())
	_, ok := memberValue(*p, "k")
	assert.False(t, ok)
}

func TestBaggageSetMapMerge(t *testing.T) {
	b, p := newTestBaggage()
	_, err := b.Set(context.Background(), []cty.Value{cty.StringVal("keep"), cty.StringVal("x")})
	require.NoError(t, err)

	merged, err := b.Set(context.Background(), []cty.Value{cty.ObjectVal(map[string]cty.Value{
		"a":    cty.StringVal("1"),
		"keep": cty.NullVal(cty.String), // null removes
	})})
	require.NoError(t, err)

	// Returned map reflects only the added/overwritten (non-null) entries.
	assert.Equal(t, map[string]cty.Value{"a": cty.StringVal("1")}, merged.AsValueMap())

	// "a" added, "keep" removed via null.
	v, ok := memberValue(*p, "a")
	assert.True(t, ok)
	assert.Equal(t, "1", v)
	_, ok = memberValue(*p, "keep")
	assert.False(t, ok)
}

func TestBaggageSetRejectsNonString(t *testing.T) {
	b, _ := newTestBaggage()
	_, err := b.Set(context.Background(), []cty.Value{cty.StringVal("k"), cty.NumberIntVal(42)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a string")
}

func TestBaggageSetRejectsInvalidKey(t *testing.T) {
	b, _ := newTestBaggage()
	// An empty key is rejected by baggage.NewMemberRaw.
	_, err := b.Set(context.Background(), []cty.Value{cty.StringVal(""), cty.StringVal("v")})
	require.Error(t, err)
}

func TestBaggageDelete(t *testing.T) {
	b, p := newTestBaggage()
	_, err := b.Set(context.Background(), []cty.Value{cty.StringVal("a"), cty.StringVal("1")})
	require.NoError(t, err)
	_, err = b.Set(context.Background(), []cty.Value{cty.StringVal("b"), cty.StringVal("2")})
	require.NoError(t, err)

	// Single key: removes just that key.
	got, err := b.Delete(context.Background(), []cty.Value{cty.StringVal("a")})
	require.NoError(t, err)
	assert.True(t, got.IsNull())
	_, ok := memberValue(*p, "a")
	assert.False(t, ok)
	_, ok = memberValue(*p, "b")
	assert.True(t, ok)

	// No args: removes everything.
	_, err = b.Delete(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, baggage.FromContext(*p).Len())
}

func TestBaggageDeleteRejectsMultipleKeys(t *testing.T) {
	b, _ := newTestBaggage()
	_, err := b.Delete(context.Background(), []cty.Value{cty.StringVal("a"), cty.StringVal("b")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flat")
}

func TestBaggageClearLengthToString(t *testing.T) {
	b, _ := newTestBaggage()
	_, err := b.Set(context.Background(), []cty.Value{cty.StringVal("a"), cty.StringVal("1")})
	require.NoError(t, err)

	n, err := b.Length(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	s, err := b.ToString(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "a=1", s)

	require.NoError(t, b.Clear(context.Background()))
	n, err = b.Length(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestBaggageCapsuleRoundTrip(t *testing.T) {
	ctx := context.Background()
	val := NewBaggageCapsule(&ctx)
	assert.Equal(t, BaggageCapsuleType, val.Type())
	b, err := GetBaggageFromCapsule(val)
	require.NoError(t, err)
	require.NotNil(t, b)
}
