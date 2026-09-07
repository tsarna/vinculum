package sqs_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
)

// receiverWithQueue is a receiver that feeds an async queue, which is the shape
// that registers two holders for one block.
const receiverWithQueue = `
bus "main" {}

client "sqs_receiver" "r" {
  region     = "us-east-1"
  queue_url  = "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue"
  queue_size = 100
  action     = ctx.topic
}
`

// The phase can only wait for what registered, so registration is the part that
// silently rots. Both holders the receiver produces are named here: the queue
// in front of the action, and the receiver behind it — which is the hop past
// the queue, because the deletion is not owed until the work is done.
//
// Behaviour lives in vinculum-sqs, which has a mock SQS client to exercise it
// against. There is no in-process fake an actual VCL config can be booted
// against here, so what this file covers is the wiring: a drain nothing calls
// and a holder nothing waits on both fail silently and identically.
func TestAReceiverRegistersItselfAsAHolderAndADrainable(t *testing.T) {
	c, hasErr, msg := build(t, receiverWithQueue)
	require.False(t, hasErr, msg)

	names := make([]string, 0, len(c.InFlight))
	for _, holder := range c.InFlight {
		names = append(names, holder.Name)
		assert.NotNil(t, holder.Pending, "%s registered nothing to wait on", holder.Name)
	}
	assert.Equal(t, []string{"bus.main", "sqs_receiver/r", "sqs_receiver/r unsettled"}, names,
		"a receiver should register its queue and its own unsettled deliveries, distinguishably")

	assert.Len(t, c.Drainables, 1,
		"a receiver that never drains keeps consuming until the transports close")
}

// The invariant behind the assertion above, stated over every holder a
// configuration can produce. One block registering two is exactly the case that
// makes a duplicate name easy to write and impossible to read in a log line.
func TestEveryInFlightHolderIsNamedDistinctly(t *testing.T) {
	c, hasErr, msg := build(t, receiverWithQueue)
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

// A receiver reports its unsettled deliveries whatever the ack mode. Under
// manual the wait covers whatever the configuration has not settled yet, which
// is the case the pipeline's own holders cannot see: the queue can be empty
// while the process still owes SQS an answer.
func TestTheHolderIsRegisteredUnderEveryAckMode(t *testing.T) {
	for _, mode := range []string{`ack = "auto"`, "ack = \"manual\"\n  settle_timeout = \"30s\""} {
		t.Run(mode, func(t *testing.T) {
			c, hasErr, msg := build(t, receiverWith(mode))
			require.False(t, hasErr, msg)

			var found *cfg.InFlightHolder
			for i, holder := range c.InFlight {
				if holder.Name == "sqs_receiver/r unsettled" {
					found = &c.InFlight[i]
				}
			}
			require.NotNil(t, found, "the receiver registered no holder of its own")
			assert.Zero(t, found.Pending(), "a receiver that has not started owes nothing")
			assert.Nil(t, found.Close,
				"a receiver holds deliveries belonging to a broker; only a settle finishes one")
		})
	}
}
