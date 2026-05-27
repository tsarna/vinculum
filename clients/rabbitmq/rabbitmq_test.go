package rabbitmq_test

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rabbitmq "github.com/tsarna/vinculum/clients/rabbitmq"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/complete.vcl
var completeVCL []byte

func buildConfig(t *testing.T, src []byte) (*cfg.Config, hclErrors) {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources(src).WithLogger(zap.NewNop()).Build()
	return c, hclErrors{diags}
}

type hclErrors struct{ diags interface{ HasErrors() bool; Error() string } }

func TestCompleteExampleParses(t *testing.T) {
	c, errs := buildConfig(t, completeVCL)
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	require.Contains(t, c.Clients, "rabbitmq")
	require.Contains(t, c.Clients["rabbitmq"], "events")
	w, ok := c.Clients["rabbitmq"]["events"].(*rabbitmq.RMQClientWrapper)
	require.True(t, ok, "expected *RMQClientWrapper, got %T", c.Clients["rabbitmq"]["events"])
	assert.Equal(t, "events", w.GetName())
}

func TestCompleteExampleRegistersStartableAndStoppable(t *testing.T) {
	c, errs := buildConfig(t, completeVCL)
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	foundStartable, foundStoppable := false, false
	for _, s := range c.Startables {
		if _, ok := s.(*rabbitmq.RMQClientWrapper); ok {
			foundStartable = true
			break
		}
	}
	for _, s := range c.Stoppables {
		if _, ok := s.(*rabbitmq.RMQClientWrapper); ok {
			foundStoppable = true
			break
		}
	}
	assert.True(t, foundStartable, "wrapper should be a Startable")
	assert.True(t, foundStoppable, "wrapper should be a Stoppable")
}

func TestCapsuleShape_SenderAndSendersExposed(t *testing.T) {
	c, errs := buildConfig(t, completeVCL)
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	val, ok := c.CtyClientMap["events"]
	require.True(t, ok, "client.events should be in CtyClientMap")
	require.True(t, val.Type().IsObjectType(), "expected object, got %s", val.Type().FriendlyName())

	// Both keys present.
	objMap := val.AsValueMap()
	require.Contains(t, objMap, "senders")
	require.Contains(t, objMap, "sender")

	// `senders` is a subscriber capsule (fan-out).
	assert.Equal(t, "subscriber", objMap["senders"].Type().FriendlyName())

	// `sender` is an object keyed by name.
	require.True(t, objMap["sender"].Type().IsObjectType())
	senderMap := objMap["sender"].AsValueMap()
	require.Contains(t, senderMap, "out")
	assert.Equal(t, "subscriber", senderMap["out"].Type().FriendlyName())
}

func TestCapsuleShape_NoSendersReturnsPlainCapsule(t *testing.T) {
	src := []byte(`
bus "main" {}
client "rabbitmq" "r" {
  brokers = ["amqp://localhost:5672/"]
  receiver "in" {
    queue      = "q"
    subscriber = bus.main
  }
}
`)
	c, errs := buildConfig(t, src)
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	val := c.CtyClientMap["r"]
	// With no senders, the capsule is a plain client capsule, not an object.
	assert.False(t, val.Type().IsObjectType())
	assert.Equal(t, "client", val.Type().FriendlyName())
}

func TestMissingBrokersIsAnError(t *testing.T) {
	src := []byte(`
client "rabbitmq" "x" {
  brokers = []
}
`)
	_, errs := buildConfig(t, src)
	require.True(t, errs.diags.HasErrors())
	assert.Contains(t, errs.diags.Error(), "brokers")
}

func TestInvalidSchemeIsRejected(t *testing.T) {
	src := []byte(`
client "rabbitmq" "x" {
  brokers = ["http://localhost:5672/"]
}
`)
	_, errs := buildConfig(t, src)
	require.True(t, errs.diags.HasErrors())
	assert.Contains(t, errs.diags.Error(), "scheme")
}

func TestDuplicateSenderNamesRejected(t *testing.T) {
	src := []byte(`
bus "main" {}
client "rabbitmq" "x" {
  brokers = ["amqp://localhost:5672/"]
  sender "dup" { exchange = "a" }
  sender "dup" { exchange = "b" }
}
`)
	_, errs := buildConfig(t, src)
	require.True(t, errs.diags.HasErrors())
	assert.Contains(t, errs.diags.Error(), "duplicate sender")
}

func TestInvalidDefaultTopicTransform(t *testing.T) {
	src := []byte(`
client "rabbitmq" "x" {
  brokers = ["amqp://localhost:5672/"]
  sender "s" {
    exchange                = "e"
    default_topic_transform = "bogus"
  }
}
`)
	_, errs := buildConfig(t, src)
	require.True(t, errs.diags.HasErrors())
	assert.Contains(t, errs.diags.Error(), "default_topic_transform")
}

func TestInvalidDefaultRoutingKeyTransform(t *testing.T) {
	src := []byte(`
bus "main" {}
client "rabbitmq" "x" {
  brokers = ["amqp://localhost:5672/"]
  receiver "r" {
    queue                          = "q"
    subscriber                     = bus.main
    default_routing_key_transform  = "bogus"
  }
}
`)
	_, errs := buildConfig(t, src)
	require.True(t, errs.diags.HasErrors())
	assert.Contains(t, errs.diags.Error(), "default_routing_key_transform")
}

func TestTLS_AmqpWithTlsEnabledIsError(t *testing.T) {
	src := []byte(`
bus "main" {}
client "rabbitmq" "x" {
  brokers = ["amqp://localhost:5672/"]
  tls { enabled = true }
  receiver "r" {
    queue      = "q"
    subscriber = bus.main
  }
}
`)
	_, errs := buildConfig(t, src)
	require.True(t, errs.diags.HasErrors())
	assert.Contains(t, errs.diags.Error(), "amqp://")
}

func TestTLS_AmqpsWithoutTlsBlockIsImplicit(t *testing.T) {
	src := []byte(`
bus "main" {}
client "rabbitmq" "x" {
  brokers = ["amqps://broker.example.com:5671/"]
  receiver "r" {
    queue      = "q"
    subscriber = bus.main
  }
}
`)
	_, errs := buildConfig(t, src)
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())
}

func TestTLS_AmqpsWithTlsDisabledIsForcedOn(t *testing.T) {
	src := []byte(`
bus "main" {}
client "rabbitmq" "x" {
  brokers = ["amqps://broker.example.com:5671/"]
  tls { enabled = false }
  receiver "r" {
    queue      = "q"
    subscriber = bus.main
  }
}
`)
	// Build succeeds (warning logged, not an error).
	_, errs := buildConfig(t, src)
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())
}

func TestSubscriberSource_RequiresSubscriberOrAction(t *testing.T) {
	src := []byte(`
client "rabbitmq" "x" {
  brokers = ["amqp://localhost:5672/"]
  receiver "r" { queue = "q" }
}
`)
	_, errs := buildConfig(t, src)
	require.True(t, errs.diags.HasErrors())
	// Error from SubscriberSource.Resolve.
	assert.True(t, errs.diags.HasErrors())
}

func TestCapsule_ResolvesViaHCL(t *testing.T) {
	// Verifies that a subsequent block can reference client.events.sender.out
	// in an expression. The completeVCL fixture has a subscription block that
	// references it; if Build succeeds, the reference resolved.
	c, errs := buildConfig(t, completeVCL)
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	val := c.CtyClientMap["events"].AsValueMap()["sender"].AsValueMap()["out"]
	assert.Equal(t, cfg.SubscriberCapsuleType, val.Type())
	_ = cty.NilVal // keep cty import live
}
