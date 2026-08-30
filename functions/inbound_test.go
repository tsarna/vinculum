package functions

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeOps stands in for a receiver's protocol verbs. The settle-once and
// staleness rules are the bus's and are tested there; what matters here is only
// what reaches them and what comes back.
type fakeOps struct {
	mu            sync.Mutex
	acks          int
	nacks         int
	keepalives    int
	reasons       []string
	ackErr        error
	keepaliveOK   bool
	invalidReason string
}

func (o *fakeOps) Ack(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.acks++
	return o.ackErr
}

func (o *fakeOps) Nack(_ context.Context, reason string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.nacks++
	o.reasons = append(o.reasons, reason)
	return nil
}

func (o *fakeOps) Keepalive(context.Context) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.keepalives++
	return o.keepaliveOK, nil
}

func (o *fakeOps) Valid() (bool, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.invalidReason == "", o.invalidReason
}

// buildInbound returns a config and a ctx value shaped as an action expression
// would see one, carrying settler on the Go context underneath — which is
// exactly what a receiver does before it delivers.
func buildInbound(t *testing.T, settler bus.Settler) (*cfg.Config, cty.Value) {
	t.Helper()
	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%v", diags)

	goCtx := context.Background()
	if settler != nil {
		goCtx = bus.WithSettler(goCtx, settler)
	}
	evalCtx, err := hclutil.NewEvalContext(goCtx).BuildEvalContext(config.EvalCtx())
	require.NoError(t, err)
	return config, evalCtx.Variables["ctx"]
}

// The no-op-safety property, and the reason these take nothing but a ctx: a
// subscription that settles what it handled must be writable without knowing
// which receiver, if any, produced the message. Against an mqtt message — or
// anything else with no acknowledgement — every one of them does nothing and
// says so.
func TestInboundFunctionsAreNoOpsWithoutASettler(t *testing.T) {
	config, ctx := buildInbound(t, nil)

	assert.False(t, call(t, config, "inbound::ack", ctx).True())
	assert.False(t, call(t, config, "inbound::nack", ctx).True())
	assert.False(t, call(t, config, "inbound::nack", ctx, cty.StringVal("no reason")).True())
	assert.False(t, call(t, config, "inbound::keepalive", ctx).True())
}

func TestInboundAckSettlesOnce(t *testing.T) {
	ops := &fakeOps{}
	config, ctx := buildInbound(t, bus.NewSettler(ops))

	assert.True(t, call(t, config, "inbound::ack", ctx).True(),
		"the first ack should report that it settled the delivery")
	assert.False(t, call(t, config, "inbound::ack", ctx).True(),
		"a second ack should report that it did not")

	ops.mu.Lock()
	defer ops.mu.Unlock()
	assert.Equal(t, 1, ops.acks, "the broker should have been acked exactly once")
}

// The reason reaches the receiver verbatim, and is optional — an author who has
// nothing to say about a failure should not have to invent something.
func TestInboundNackCarriesItsReason(t *testing.T) {
	t.Run("with a reason", func(t *testing.T) {
		ops := &fakeOps{}
		config, ctx := buildInbound(t, bus.NewSettler(ops))

		assert.True(t, call(t, config, "inbound::nack", ctx, cty.StringVal("schema rejected it")).True())

		ops.mu.Lock()
		defer ops.mu.Unlock()
		assert.Equal(t, []string{"schema rejected it"}, ops.reasons)
	})

	t.Run("without one", func(t *testing.T) {
		ops := &fakeOps{}
		config, ctx := buildInbound(t, bus.NewSettler(ops))

		assert.True(t, call(t, config, "inbound::nack", ctx).True())

		ops.mu.Lock()
		defer ops.mu.Unlock()
		assert.Equal(t, []string{""}, ops.reasons)
	})

	t.Run("at most one", func(t *testing.T) {
		config, ctx := buildInbound(t, bus.NewSettler(&fakeOps{}))
		fn := config.EvalCtx().Functions["inbound::nack"]

		_, err := fn.Call([]cty.Value{ctx, cty.StringVal("one"), cty.StringVal("two")})
		require.Error(t, err)
	})
}

func TestInboundKeepaliveReportsWhetherALeaseWasExtended(t *testing.T) {
	t.Run("a protocol with a lease", func(t *testing.T) {
		config, ctx := buildInbound(t, bus.NewSettler(&fakeOps{keepaliveOK: true}))
		assert.True(t, call(t, config, "inbound::keepalive", ctx).True())
	})

	t.Run("a protocol without one", func(t *testing.T) {
		config, ctx := buildInbound(t, bus.NewSettler(&fakeOps{keepaliveOK: false}))
		assert.False(t, call(t, config, "inbound::keepalive", ctx).True())
	})
}

// A stale token means handling took longer than the configuration allows for,
// which is a fact about the configuration rather than an error in it. So the
// call reports that it settled nothing and the reason is logged where a VCL
// author will look for it, rather than failing the action over a message that
// has already gone back to the broker.
func TestAStaleSettleIsLoggedRatherThanFailed(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	ops := &fakeOps{invalidReason: "visibility timeout expired"}
	config, ctx := buildInbound(t, bus.NewSettler(ops))
	config.UserLogger = zap.New(core)

	assert.False(t, call(t, config, "inbound::ack", ctx).True())

	require.Equal(t, 1, logs.Len(), "a stale settle should say so once")
	entry := logs.All()[0]
	assert.Contains(t, entry.Message, "inbound::ack")
	assert.Contains(t, entry.ContextMap()["error"], "visibility timeout expired")

	ops.mu.Lock()
	defer ops.mu.Unlock()
	assert.Zero(t, ops.acks, "a stale settle must not reach the broker")
}

// A broker that could not be reached is a different matter: the message was not
// acknowledged, and a configuration that carried on believing it was is exactly
// the silence this design exists to remove. So it fails the action.
func TestAFailedSettleFailsTheAction(t *testing.T) {
	config, ctx := buildInbound(t, bus.NewSettler(&fakeOps{ackErr: errors.New("XACK failed")}))
	fn := config.EvalCtx().Functions["inbound::ack"]

	_, err := fn.Call([]cty.Value{ctx})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "XACK failed")
}

// A dynamic ctx argument without AllowDynamicType poisons the return type, so
// what reflection reports of these functions would stop being a bool — which is
// what help(), man, and the checker read.
func TestInboundFunctionsReturnAStaticBool(t *testing.T) {
	config, _ := buildInbound(t, nil)
	for _, name := range []string{"inbound::ack", "inbound::nack", "inbound::keepalive"} {
		fn, ok := config.EvalCtx().Functions[name]
		require.True(t, ok, "%s is not registered", name)

		got, err := fn.ReturnType([]cty.Type{cty.DynamicPseudoType})
		require.NoError(t, err, "%s", name)
		assert.Equal(t, cty.Bool, got, "%s should reflect as returning a bool", name)
	}
}
