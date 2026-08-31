package config

import (
	"context"
	"testing"
	"time"

	_ "embed"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	richcty "github.com/tsarna/rich-cty-types"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/bus.vcl
var bustest []byte

func TestBus(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	config, diags := NewConfig().WithSources(bustest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

	assert.Contains(t, config.Constants, "bus")

	assert.Contains(t, config.Buses, "main")
	assert.Contains(t, config.Buses, "ws")

	assert.Contains(t, config.CtyBusMap, "main")
	assert.Contains(t, config.CtyBusMap, "ws")
}

// A bus block used to carry a `,remain` body that nothing read, so it was the
// one block type that accepted any attribute at all: `queue_sizee = 500` parsed
// clean and the bus quietly took its default. Reintroducing the field would
// make this pass again.
func TestBusRejectsUnknownAttribute(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().
		WithSources([]byte("bus \"b\" {\n queue_sizee = 500\n}\n")).
		WithLogger(logger).
		Build()

	assert.True(t, diags.HasErrors(), "a misspelled bus attribute must be reported")
	assert.Contains(t, diags.Error(), "queue_sizee")
}

// busHandleFor builds a config from src and returns the handle behind
// `bus.<name>`, reached the way an expression reaches it.
func busHandleFor(t *testing.T, src string, name string) *BusHandle {
	t.Helper()

	config, diags := NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	val, ok := config.CtyBusMap[name]
	require.True(t, ok, "no bus.%s", name)

	handle, ok := val.EncapsulatedValue().(*BusHandle)
	require.True(t, ok, "bus.%s is not a BusHandle", name)
	return handle
}

func TestBusIsGettable(t *testing.T) {
	handle := busHandleFor(t, "bus \"b\" {\n queue_size = 200\n}\n", "b")

	var gettable richcty.Gettable = handle

	get := func(member string) cty.Value {
		t.Helper()
		val, err := gettable.Get(context.Background(), []cty.Value{cty.StringVal(member)})
		require.NoError(t, err)
		return val
	}

	assert.True(t, get("queue_depth").RawEquals(cty.NumberIntVal(0)))
	assert.True(t, get("queue_capacity").RawEquals(cty.NumberIntVal(200)))
	assert.True(t, get("dropped").RawEquals(cty.NumberIntVal(0)))
	assert.True(t, get("undelivered").RawEquals(cty.NumberIntVal(0)))

	// Derived, and the number a threshold is written against: it is comparable
	// across buses of different sizes, which a depth is not.
	ratio, _ := get("queue_ratio").AsBigFloat().Float64()
	assert.Equal(t, 0.0, ratio)
}

func TestBusGetRejectsWhatItCannotAnswer(t *testing.T) {
	handle := busHandleFor(t, "bus \"b\" {}\n", "b")

	_, err := handle.Get(context.Background(), nil)
	require.Error(t, err, "a bus has no single value, so get() must be told which member")
	assert.Contains(t, err.Error(), "queue_depth")

	_, err = handle.Get(context.Background(), []cty.Value{cty.NumberIntVal(1)})
	require.Error(t, err)

	_, err = handle.Get(context.Background(), []cty.Value{cty.StringVal("queue_dpeth")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue_dpeth", "the diagnostic must name what was asked for")
	assert.Contains(t, err.Error(), "queue_depth", "and what could have been")
}

// The handle wraps the bus rather than replacing it, so everything that already
// resolved a bus out of `bus.<name>` still does.
func TestBusHandleIsStillABusAndASubscriber(t *testing.T) {
	config, diags := NewConfig().
		WithSources([]byte("bus \"b\" {}\n")).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	val := config.CtyBusMap["b"]

	eventBus, err := GetEventBusFromCapsule(val)
	require.NoError(t, err)
	assert.Equal(t, 1000, eventBus.QueueCapacity())

	subscriber, err := GetSubscriberFromCapsule(val)
	require.NoError(t, err)
	assert.NotNil(t, subscriber)

	// One handle per bus: a subscriber's identity inside the bus is the value
	// itself, so Config.Buses and the capsule must hold the same one.
	assert.Same(t, config.Buses["b"], eventBus)
}

func TestBusUndeliverableIsOptIn(t *testing.T) {
	off := busHandleFor(t, "bus \"b\" {}\n", "b")
	on := busHandleFor(t, "bus \"b\" {\n undeliverable = true\n}\n", "b")

	catch := func(handle *BusHandle) chan string {
		t.Helper()
		seen := make(chan string, 4)
		_, err := handle.SubscribeFunc(context.Background(), bus.UndeliverableTopic,
			func(ctx context.Context, topic string, payload any, fields map[string]string) error {
				original, _ := bus.UndeliverableTopicFromContext(ctx)
				seen <- original
				return nil
			})
		require.NoError(t, err)
		return seen
	}

	offSeen, onSeen := catch(off), catch(on)

	require.NoError(t, off.Publish(context.Background(), "sensor/typo", "payload"))
	require.NoError(t, on.Publish(context.Background(), "sensor/typo", "payload"))

	assert.Equal(t, "sensor/typo", <-onSeen)

	// The counter is kept either way — it is the diagnostic, and it is what
	// tells the author there is something to catch.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, uint64(1), off.UndeliveredTotal())
	}, time.Second, 5*time.Millisecond)

	select {
	case topic := <-offSeen:
		t.Fatalf("republished %q without undeliverable = true", topic)
	default:
	}
}

// End to end: a message nothing matched reaches an ordinary subscription, whose
// action can name the topic that failed to route.
func TestUndeliverableReachesASubscriptionAction(t *testing.T) {
	config, diags := NewConfig().
		WithSources([]byte(`
bus "main" {
    undeliverable = true
}

var "caught" { value = "" }

subscription "unroutable" {
    target = bus.main
    topics = ["$undeliverable"]
    action = set(ctx, var.caught, ctx.undeliverable_topic)
}
`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	require.NoError(t, config.Buses["main"].Publish(context.Background(), "senosr/typo", "payload"))

	caught, err := types.GetVariableFromCapsule(config.CtyVarMap["caught"])
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		val, err := caught.Get(context.Background(), nil)
		assert.NoError(c, err)
		assert.Equal(c, "senosr/typo", val.AsString())
	}, time.Second, 5*time.Millisecond)
}

// TestNoBusIsDeclaredCreatesNoBus asserts that a configuration with no bus
// block builds no bus, and so starts no delivery goroutine and allocates no
// queue. A bus-free configuration is the common case — three of the four
// shipped examples use none.
func TestNoBusIsDeclaredCreatesNoBus(t *testing.T) {
	config, diags := NewConfig().
		WithSources([]byte(`const { greeting = "hi" }`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	assert.Empty(t, config.Buses)
	assert.Empty(t, config.CtyBusMap)

	// The `bus` root still exists, and is empty rather than absent. That is what
	// lets the reference checker say "No bus is declared by this configuration"
	// instead of failing to recognise `bus` as a root at all.
	root, ok := config.Constants["bus"]
	require.True(t, ok, "the bus root must exist even when no bus does")
	assert.True(t, root.Type().IsObjectType())
	assert.Empty(t, root.Type().AttributeTypes())
}

// TestUndeclaredMainBusIsReported pins the diagnostic for the mistake an
// upgrade produces: `bus.main` in a configuration that declares no bus fails at
// load, naming the fix, rather than at the first event.
func TestUndeclaredMainBusIsReported(t *testing.T) {
	_, diags := NewConfig().
		WithSources([]byte(`
subscription "s" {
    target = bus.main
    topics = ["in"]
    action = log::info("x")
}
`)).
		WithLogger(zap.NewNop()).
		Build()

	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), `No bus named "main"`)
	assert.Contains(t, diags.Error(), "No bus is declared by this configuration.")
}

// TestSubscriptionTargetIsRequired pins `target` as required rather than
// defaulted, and pins the wording, since the tag alone cannot enforce it —
// config.DecodeBody does.
func TestSubscriptionTargetIsRequired(t *testing.T) {
	_, diags := NewConfig().
		WithSources([]byte(`
bus "main" {}

subscription "s" {
    topics = ["in"]
    action = log::info("x")
}
`)).
		WithLogger(zap.NewNop()).
		Build()

	require.True(t, diags.HasErrors())
	assert.Contains(t, diags.Error(), `The argument "target" is required`)
}
