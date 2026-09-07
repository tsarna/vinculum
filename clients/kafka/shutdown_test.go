package kafka_test

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

client "kafka" "k" {
  brokers = ["localhost:9092"]

  receiver "orders" {
    group_id   = "g1"
    queue_size = 100
    action     = ctx.topic

    subscription "orders" {
      vinculum_topic = "orders"
    }
  }

  receiver "audit" {
    group_id   = "g2"
    subscriber = bus.main

    subscription "audit" {
      vinculum_topic = "audit"
    }
  }
}
`

// The phase can only wait for what registered, so registration is the part that
// silently rots. Every holder is named here: the bus, the queue in front of the
// first receiver's action, and each receiver behind its own — which is the hop
// past the queue, because the mark a record moves is not owed until the work is
// done.
//
// Behaviour lives in vinculum-kafka, which drives an in-process broker. What
// this covers is the wiring: a drain nothing calls and a holder nothing waits
// on both fail silently and identically.
func TestReceiversRegisterAsHoldersAndTheClientAsADrainable(t *testing.T) {
	c, hasErr, msg := build(t, twoReceivers)
	require.False(t, hasErr, msg)

	names := make([]string, 0, len(c.InFlight))
	for _, holder := range c.InFlight {
		names = append(names, holder.Name)
		assert.NotNil(t, holder.Pending, "%s registered nothing to wait on", holder.Name)
		if strings.HasSuffix(holder.Name, " unsettled") {
			assert.Nil(t, holder.Close,
				"a receiver holds records belonging to a broker; only a settle finishes one")
		}
	}
	assert.Equal(t, []string{
		"bus.main",
		"kafka/k/orders",
		"kafka/k/orders unsettled",
		"kafka/k/audit unsettled",
	}, names, "each receiver should register its own unsettled records, distinguishably")

	assert.Len(t, c.Drainables, 1,
		"a client whose receivers never drain keeps consuming until it leaves the group")
}

// The invariant behind the assertion above, stated over every holder a
// configuration can produce. One block registering several is exactly the case
// that makes a duplicate name easy to write and impossible to read in a log
// line.
func TestEveryInFlightHolderIsNamedDistinctly(t *testing.T) {
	c, hasErr, msg := build(t, twoReceivers)
	require.False(t, hasErr, msg)

	seen := make(map[string]int, len(c.InFlight))
	for _, holder := range c.InFlight {
		seen[holder.Name]++
	}
	for name, count := range seen {
		assert.Equal(t, 1, count,
			"%d holders share the name %q, so the shutdown log cannot tell them apart", count, name)
	}
}

// The count is read through the client, because the library consumers do not
// exist until Start builds them and the holder is registered long before that.
// A holder that panicked before startup would take the whole teardown with it,
// since nothing else runs after a panic in that phase.
func TestAHolderReportsNothingBeforeTheClientStarts(t *testing.T) {
	c, hasErr, msg := build(t, twoReceivers)
	require.False(t, hasErr, msg)

	var checked int
	for _, holder := range c.InFlight {
		if strings.HasSuffix(holder.Name, " unsettled") {
			assert.Zero(t, holder.Pending(), "%s", holder.Name)
			checked++
		}
	}
	require.Equal(t, 2, checked, "the receivers' own holders were not registered")
}

// A send-only client has nothing to drain, so it does not register as a
// Drainable and owes the broker nothing to wait for.
func TestASendOnlyClientDoesNotRegisterADrain(t *testing.T) {
	c, hasErr, msg := build(t, `
client "kafka" "k" {
  brokers = ["localhost:9092"]

  sender "out" {
    topic "#" {
      kafka_topic = "events"
    }
  }
}
`)
	require.False(t, hasErr, msg)

	assert.Empty(t, c.Drainables)

	// Stated as a count rather than as a property of each holder: this config
	// produces no holders at all, so a loop asserting something about every one
	// of them would pass without ever running its body.
	assert.Empty(t, c.InFlight, "a client with no receivers owes the broker nothing")
}
