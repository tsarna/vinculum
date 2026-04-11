package http

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
)

// ── Reentrancy marker ───────────────────────────────────────────────────────

func TestAuthMarker_AddAndCheck(t *testing.T) {
	ctx := context.Background()
	assert.False(t, IsAuthSuppressed(ctx, "foo"))

	ctx = WithAuthMarker(ctx, "foo")
	assert.True(t, IsAuthSuppressed(ctx, "foo"))
	assert.False(t, IsAuthSuppressed(ctx, "bar"))
}

func TestAuthMarker_NestedClients(t *testing.T) {
	ctx := WithAuthMarker(context.Background(), "foo")
	ctx = WithAuthMarker(ctx, "bar")
	assert.True(t, IsAuthSuppressed(ctx, "foo"))
	assert.True(t, IsAuthSuppressed(ctx, "bar"))
	assert.False(t, IsAuthSuppressed(ctx, "baz"))
}

func TestAuthMarker_ParentNotMutated(t *testing.T) {
	parent := WithAuthMarker(context.Background(), "foo")
	child := WithAuthMarker(parent, "bar")
	// Adding "bar" to child must not affect parent.
	assert.True(t, IsAuthSuppressed(parent, "foo"))
	assert.False(t, IsAuthSuppressed(parent, "bar"))
	assert.True(t, IsAuthSuppressed(child, "bar"))
}

// ── ParseAuthResult ─────────────────────────────────────────────────────────

func TestParseAuthResult_String(t *testing.T) {
	r, err := ParseAuthResult(cty.StringVal("Bearer abc"))
	require.NoError(t, err)
	assert.Equal(t, "Bearer abc", r.Value)
	assert.Equal(t, time.Duration(0), r.ExpiresIn)
}

func TestParseAuthResult_Object_ValueOnly(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"value": cty.StringVal("Bearer xyz"),
	})
	r, err := ParseAuthResult(v)
	require.NoError(t, err)
	assert.Equal(t, "Bearer xyz", r.Value)
	assert.Equal(t, time.Duration(0), r.ExpiresIn)
}

func TestParseAuthResult_Object_ExpiresIn(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"value":      cty.StringVal("Bearer xyz"),
		"expires_in": cty.StringVal("30s"),
	})
	r, err := ParseAuthResult(v)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, r.ExpiresIn)
}

func TestParseAuthResult_Object_ExpiresAt(t *testing.T) {
	future := time.Now().Add(45 * time.Second)
	v := cty.ObjectVal(map[string]cty.Value{
		"value":      cty.StringVal("Bearer xyz"),
		"expires_at": timecty.NewTimeCapsule(future),
	})
	r, err := ParseAuthResult(v)
	require.NoError(t, err)
	assert.InDelta(t, float64(45*time.Second), float64(r.ExpiresIn), float64(time.Second))
}

func TestParseAuthResult_Object_RefreshEarly(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"value":         cty.StringVal("Bearer xyz"),
		"expires_in":    cty.StringVal("60s"),
		"refresh_early": cty.StringVal("10s"),
	})
	r, err := ParseAuthResult(v)
	require.NoError(t, err)
	assert.Equal(t, 50*time.Second, r.ExpiresIn)
}

func TestParseAuthResult_Object_RefreshEarlyExceedsExpiresIn(t *testing.T) {
	// refresh_early larger than expires_in: result should be a tiny
	// non-zero TTL so the value still cached but immediately stale.
	v := cty.ObjectVal(map[string]cty.Value{
		"value":         cty.StringVal("Bearer xyz"),
		"expires_in":    cty.StringVal("5s"),
		"refresh_early": cty.StringVal("30s"),
	})
	r, err := ParseAuthResult(v)
	require.NoError(t, err)
	assert.Greater(t, r.ExpiresIn, time.Duration(0))
}

func TestParseAuthResult_Object_BothExpiresInAndAt(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"value":      cty.StringVal("Bearer xyz"),
		"expires_in": cty.StringVal("30s"),
		"expires_at": timecty.NewTimeCapsule(time.Now().Add(60 * time.Second)),
	})
	_, err := ParseAuthResult(v)
	assert.Error(t, err)
}

func TestParseAuthResult_Object_MissingValue(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"expires_in": cty.StringVal("30s"),
	})
	_, err := ParseAuthResult(v)
	assert.Error(t, err)
}

func TestParseAuthResult_Object_NonStringValue(t *testing.T) {
	v := cty.ObjectVal(map[string]cty.Value{
		"value": cty.NumberIntVal(42),
	})
	_, err := ParseAuthResult(v)
	assert.Error(t, err)
}

func TestParseAuthResult_Null(t *testing.T) {
	_, err := ParseAuthResult(cty.NullVal(cty.String))
	assert.Error(t, err)
}

// ── authCache state machine ─────────────────────────────────────────────────

// stubEval returns an AuthEvaluator that always returns the same result.
// The counter pointer is incremented on each call so tests can verify
// how many times the hook was actually invoked.
func stubEval(value string, ttl time.Duration, calls *int32) AuthEvaluator {
	return func(_ AuthAttempt) (AuthResult, error) {
		atomic.AddInt32(calls, 1)
		return AuthResult{Value: value, ExpiresIn: ttl}, nil
	}
}

// erroringEval always returns an error.
func erroringEval(calls *int32) AuthEvaluator {
	return func(_ AuthAttempt) (AuthResult, error) {
		atomic.AddInt32(calls, 1)
		return AuthResult{}, errors.New("simulated hook failure")
	}
}

func TestAuthCache_FirstCallEvaluates(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())
	v, err := c.Get(context.Background(), "initial", 0, stubEval("Bearer abc", 0, &calls))
	require.NoError(t, err)
	assert.Equal(t, "Bearer abc", v)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestAuthCache_SecondCallReturnsCached(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())
	eval := stubEval("Bearer abc", 0, &calls)

	_, err := c.Get(context.Background(), "initial", 0, eval)
	require.NoError(t, err)
	_, err = c.Get(context.Background(), "initial", 0, eval)
	require.NoError(t, err)
	_, err = c.Get(context.Background(), "initial", 0, eval)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "cache should not re-evaluate without invalidation")
}

func TestAuthCache_InvalidateForcesReEval(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())
	eval := stubEval("Bearer abc", 0, &calls)

	_, _ = c.Get(context.Background(), "initial", 0, eval)
	c.Invalidate()
	_, _ = c.Get(context.Background(), "unauthorized", 401, eval)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestAuthCache_TTLExpiry(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())
	eval := stubEval("Bearer abc", 20*time.Millisecond, &calls)

	_, _ = c.Get(context.Background(), "initial", 0, eval)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))

	// Within TTL → cached.
	_, _ = c.Get(context.Background(), "initial", 0, eval)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))

	// Past TTL → re-eval.
	time.Sleep(30 * time.Millisecond)
	_, _ = c.Get(context.Background(), "initial", 0, eval)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestAuthCache_MaxLifetimeCapsExpressionTTL(t *testing.T) {
	var calls int32
	// expression says 1 hour, but client cap is 20ms
	c := newAuthCache("test", nil, 20*time.Millisecond, 5, defaultAuthBackoff())
	eval := stubEval("Bearer abc", 1*time.Hour, &calls)

	_, _ = c.Get(context.Background(), "initial", 0, eval)
	time.Sleep(30 * time.Millisecond)
	_, _ = c.Get(context.Background(), "initial", 0, eval)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "max lifetime should win when shorter")
}

func TestAuthCache_RecordResult_NonAuthResetsFailures(t *testing.T) {
	c := newAuthCache("test", nil, 0, 3, defaultAuthBackoff())
	// Force two failures.
	c.recordFailureLocked(time.Now())
	c.recordFailureLocked(time.Now())
	assert.Equal(t, 2, c.consecutiveFailures)

	c.RecordResult(200)
	snap := c.Snapshot()
	assert.Equal(t, 0, snap.ConsecutiveFailures)
	assert.False(t, snap.InFailedState)
}

func TestAuthCache_RecordResult_401InvalidatesAndCounts(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())
	eval := stubEval("Bearer abc", 0, &calls)

	_, _ = c.Get(context.Background(), "initial", 0, eval)
	assert.True(t, c.Snapshot().HasCachedValue)

	c.RecordResult(401)
	snap := c.Snapshot()
	assert.False(t, snap.HasCachedValue, "401 must invalidate the cache")
	assert.Equal(t, 1, snap.ConsecutiveFailures)
}

func TestAuthCache_FailedStateAfterMaxFailures(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 2, AuthBackoff{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
		Factor:       2.0,
	})
	eval := erroringEval(&calls)

	// Two failures from the hook returning an error → enters failed state.
	_, err := c.Get(context.Background(), "initial", 0, eval)
	assert.Error(t, err)
	_, err = c.Get(context.Background(), "initial", 0, eval)
	assert.Error(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))

	// Third call should fail fast (no eval, no increment).
	_, err = c.Get(context.Background(), "initial", 0, eval)
	assert.Error(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "third call should not invoke the hook")

	snap := c.Snapshot()
	assert.True(t, snap.InFailedState)
}

func TestAuthCache_FailedState_RecoversAfterBackoff(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 1, AuthBackoff{
		InitialDelay: 20 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		Factor:       2.0,
	})

	// Single failure trips the failed state (max=1).
	_, err := c.Get(context.Background(), "initial", 0, erroringEval(&calls))
	assert.Error(t, err)

	// Immediately retrying returns failed-state error.
	_, err = c.Get(context.Background(), "initial", 0, erroringEval(&calls))
	assert.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "second call must not invoke hook")

	// After the backoff elapses, the next call invokes the hook again.
	time.Sleep(30 * time.Millisecond)
	successCalls := int32(0)
	v, err := c.Get(context.Background(), "initial", 0, stubEval("Bearer ok", 0, &successCalls))
	require.NoError(t, err)
	assert.Equal(t, "Bearer ok", v)
	assert.Equal(t, int32(1), atomic.LoadInt32(&successCalls))
}

func TestAuthCache_ReentrancyGetReturnsEmpty(t *testing.T) {
	var calls int32
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())
	ctx := WithAuthMarker(context.Background(), "test")

	v, err := c.Get(ctx, "initial", 0, stubEval("Bearer abc", 0, &calls))
	require.NoError(t, err)
	assert.Equal(t, "", v, "reentrancy must not run the hook")
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls))
}

func TestAuthCache_Singleflight_ConcurrentCallers(t *testing.T) {
	// Many concurrent Get callers should result in exactly one hook
	// invocation. The eval function blocks briefly to ensure goroutines
	// pile up on the mutex.
	var calls int32
	eval := func(_ AuthAttempt) (AuthResult, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		return AuthResult{Value: "Bearer abc"}, nil
	}
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())

	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Get(context.Background(), "initial", 0, eval)
			assert.NoError(t, err)
			assert.Equal(t, "Bearer abc", v)
		}()
	}
	wg.Wait()

	// All callers serialize behind the mutex; only the first one whose
	// turn comes finds the cache empty and runs eval. The rest see the
	// populated cache.
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestAuthCache_AuthAttemptFields(t *testing.T) {
	// The AuthAttempt passed to the eval should reflect the failure
	// counter and the previous status from the caller.
	c := newAuthCache("test", nil, 0, 5, defaultAuthBackoff())
	c.recordFailureLocked(time.Now())
	c.recordFailureLocked(time.Now())

	var got AuthAttempt
	eval := func(a AuthAttempt) (AuthResult, error) {
		got = a
		return AuthResult{Value: "Bearer ok"}, nil
	}
	_, err := c.Get(context.Background(), "unauthorized", 401, eval)
	require.NoError(t, err)
	assert.Equal(t, "unauthorized", got.Reason)
	assert.Equal(t, 2, got.Failures)
	assert.Equal(t, 401, got.PreviousStatus)
}
