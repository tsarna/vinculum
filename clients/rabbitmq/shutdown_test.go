package rabbitmq_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoReceivers is a client with two receivers, one of which feeds an async
// queue — the shape that registers two holders for one block, and the shape
// where a shared name would be unreadable.
const twoReceivers = `
bus "main" {}

client "rabbitmq" "r" {
  brokers = ["amqp://localhost:5672/"]

  receiver "orders" {
    queue      = "orders"
    queue_size = 100
    action     = ctx.topic
  }

  receiver "audit" {
    queue      = "audit"
    subscriber = bus.main
  }
}
`

// The phase can only wait for what registered, so registration is the part that
// silently rots. Every holder is named here: the bus, the queue in front of the
// first receiver's action, and each receiver behind its own — which is the hop
// past the queue, because the acknowledgement is not owed until the work is
// done.
//
// Behaviour lives in vinculum-rabbitmq, which has a fake channel to exercise it
// against, and in the integration tests, which have a real broker. What this
// covers is the wiring: a drain nothing calls and a holder nothing waits on
// both fail silently and identically.
func TestReceiversRegisterAsHoldersAndTheClientAsADrainable(t *testing.T) {
	c, errs := buildConfig(t, []byte(twoReceivers))
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	names := make([]string, 0, len(c.InFlight))
	for _, holder := range c.InFlight {
		names = append(names, holder.Name)
		assert.NotNil(t, holder.Pending, "%s registered nothing to wait on", holder.Name)
		if strings.HasSuffix(holder.Name, " unsettled") {
			assert.Nil(t, holder.Close,
				"a receiver holds deliveries belonging to a broker; only a settle finishes one")
		}
	}
	assert.Equal(t, []string{
		"bus.main",
		"rabbitmq/r/orders",
		"rabbitmq/r/orders unsettled",
		"rabbitmq/r/audit unsettled",
	}, names, "each receiver should register its own unsettled deliveries, distinguishably")

	assert.Len(t, c.Drainables, 1,
		"a client whose receivers never drain keeps consuming until the connection closes")
}

// The invariant behind the assertion above, stated over every holder a
// configuration can produce. One block registering several is exactly the case
// that makes a duplicate name easy to write and impossible to read in a log
// line.
func TestEveryInFlightHolderIsNamedDistinctly(t *testing.T) {
	c, errs := buildConfig(t, []byte(twoReceivers))
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	seen := make(map[string]int, len(c.InFlight))
	for _, holder := range c.InFlight {
		seen[holder.Name]++
	}
	for name, count := range seen {
		assert.Equal(t, 1, count,
			"%d holders share the name %q, so the shutdown log cannot tell them apart", count, name)
	}
}

// The count is read through the wrapper, because the library receivers do not
// exist until Start builds them and the holder is registered long before that.
// A holder that panicked or misreported before startup would take the whole
// teardown with it, since nothing else runs after a panic in that phase.
func TestAHolderReportsNothingBeforeTheClientStarts(t *testing.T) {
	c, errs := buildConfig(t, []byte(twoReceivers))
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	var checked int
	for _, holder := range c.InFlight {
		if holder.Name == "rabbitmq/r/orders unsettled" || holder.Name == "rabbitmq/r/audit unsettled" {
			assert.Zero(t, holder.Pending(), "%s", holder.Name)
			checked++
		}
	}
	require.Equal(t, 2, checked, "the receivers' own holders were not registered")
}

// Draining is what a client with no receivers has nothing to do, and it must
// still be safe: a send-only client is a `Drainable` that would drain nothing,
// so it does not register as one.
func TestASendOnlyClientDoesNotRegisterADrain(t *testing.T) {
	c, errs := buildConfig(t, []byte(`
client "rabbitmq" "r" {
  brokers = ["amqp://localhost:5672/"]

  sender "out" {
    exchange = "events"
  }
}
`))
	require.False(t, errs.diags.HasErrors(), errs.diags.Error())

	assert.Empty(t, c.Drainables)
	for _, holder := range c.InFlight {
		assert.NotContains(t, holder.Name, "rabbitmq/",
			"a client with no receivers owes the broker nothing")
	}
}
