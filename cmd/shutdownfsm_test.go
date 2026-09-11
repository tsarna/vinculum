package cmd

import (
	"context"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	fsm "github.com/tsarna/vinculum-fsm"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// hookCost is how long each transition takes in the tests below, and it is a
// real wait rather than a busy machine on purpose: it makes a backlog outlast
// any quiesce that is not waiting for it, on a fast machine and a loaded one
// alike. Ten events is 200ms of work, and a quiesce that finds nothing pending
// is done after two readings one 25ms sleep apart.
const hookCost = 20 * time.Millisecond

// slowHookServer is what a transition calls to take its time. An httptest
// server rather than anything in-process because a hook can only do what the
// configuration language can say, and an HTTP request is the plainest slow
// thing a `.vcl` has.
func slowHookServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		time.Sleep(hookCost)
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fsmConfig boots a machine driven from a bus, whose every event spends
// hookCost in its transition and then counts itself.
//
// shutdown_event is what makes this discriminate: Stop then ends the loop with
// the backlog unrun. Without it, Stop would run the backlog in the last phase,
// and a test built on this would pass with the mailbox holder removed.
func fsmConfig(t *testing.T, url string) *config.Config {
	t.Helper()
	cfg, diags := config.NewConfig().
		WithSources([]byte(fmt.Sprintf(`
bus "main" {}

var "handled" { value = 0 }

fsm "door" {
    initial        = "idle"
    queue_size     = 100
    shutdown_event = "halt"

    state "idle" {}
    state "stopped" {}

    event "work" {
        transition "idle" "idle" {
            action = [
                http::get(ctx, null, %q),
                increment(ctx, var.handled),
            ]
        }
    }

    event "halt" {
        transition "*" "stopped" {}
    }
}

subscription "drive" {
    target     = bus.main
    topics     = ["work"]
    subscriber = fsm.door
}
`, url))).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	return cfg
}

// In the language a user writes: a machine's mailbox is a queue like any other
// — `queue_size` events deep, drained by one goroutine — and the phase that
// waits for the pipeline to empty can only see it as a registered holder.
// Without one, that phase samples zero twice with the backlog still queued and
// moves on to a Stop that, with a shutdown_event, abandons whatever the machine
// has not reached.
//
// The barrier is var.handled, which only rises. The precondition
// after it is what makes the rest a test rather than a coincidence: the
// shutdown has to land with a backlog still queued, and a test goroutine that
// arrived late enough to find the machine finished would otherwise pass having
// observed nothing.
func TestShutdownRunsTheBacklogAnFsmIsHolding(t *testing.T) {
	cfg := fsmConfig(t, slowHookServer(t))
	startAllOrFail(t, cfg)

	const events = 10
	for i := 0; i < events; i++ {
		require.NoError(t, cfg.Buses["main"].Publish(context.Background(), "work", i))
	}
	require.Eventually(t, func() bool { return handledCount(cfg) > 0 },
		3*time.Second, time.Millisecond, "the machine never ran anything")
	require.Less(t, handledCount(cfg), int64(events),
		"the machine had finished before the shutdown began, so nothing was carried through it")

	began := time.Now()
	shutdown(cfg, zap.NewNop())
	took := time.Since(began)

	assert.Equal(t, int64(events), handledCount(cfg),
		"shutdown exited while the machine still had events queued")

	// And the wait has to end. A mailbox that never reads empty holds the phase
	// for its whole budget, and this is where that shows: the backlog is a
	// fraction of a second of work.
	assert.Less(t, took, config.DefaultShutdownTimeout/2,
		"shutdown ran out its quiesce budget, so something never read empty")

	// And the shutdown event still runs.
	door, err := fsm.GetInstanceFromCapsule(cfg.CtyFsmMap["door"])
	require.NoError(t, err)
	assert.Equal(t, "stopped", door.CurrentState(),
		"the shutdown event should still have been delivered")
}

// The phase can only wait for what registered, so registration is the part that
// rots silently: each machine registers one holder, named for it, with nothing
// to Close.
func TestAnFsmRegistersItsMailboxAsAHolder(t *testing.T) {
	cfg, diags := config.NewConfig().
		WithSources([]byte(`
fsm "door" {
    initial = "idle"
    state "idle" {}
    state "busy" {}
    event "go" {
        transition "idle" "busy" {}
    }
}

fsm "gate" {
    initial = "idle"
    state "idle" {}
    state "busy" {}
    event "go" {
        transition "idle" "busy" {}
    }
}
`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	t.Cleanup(func() { shutdown(cfg, zap.NewNop()) })

	var names []string
	for _, holder := range cfg.InFlight {
		if !strings.HasPrefix(holder.Name, "fsm.") {
			continue
		}
		names = append(names, holder.Name)
		require.NotNil(t, holder.Pending, "%s registered nothing to wait on", holder.Name)
		assert.Nil(t, holder.Close,
			"%s: nothing empties a mailbox short of stopping the machine", holder.Name)
	}
	assert.Equal(t, []string{"fsm.door", "fsm.gate"}, names,
		"each machine should name its own mailbox")
}

// With a broker: an entry whose XACK is owed by a transition is still in the
// machine — queued or running — when the signal arrives, so the acknowledgement
// needs the mailbox emptied while the connection is still open. The assertion
// is the broker's: everything the receiver took is acknowledged at exit. It
// says nothing about how much the receiver took, since an entry it never
// reached stays on the stream for the next boot.
//
// On this path the receiver's unsettled count holds the phase open by itself,
// so this test pins the ordering, not the mailbox holder;
// TestShutdownRunsTheBacklogAnFsmIsHolding pins the holder.
func TestShutdownSettlesWhatAMachinesMailboxWasHolding(t *testing.T) {
	mr := miniredis.RunT(t)
	url := slowHookServer(t)

	cfg, diags := config.NewConfig().
		WithSources([]byte(fmt.Sprintf(`
client "redis" "base" { address = %q }

client "redis_stream" "rs" {
    connection = client.base

    consumer "in" {
        stream         = "events"
        group          = "g"
        consumer_name  = "c"
        block_timeout  = "50ms"
        ack            = "auto"
        vinculum_topic = "work"
        subscriber     = fsm.door
    }
}

fsm "door" {
    initial        = "idle"
    queue_size     = 100
    shutdown_event = "halt"

    state "idle" {}
    state "stopped" {}

    event "work" {
        transition "idle" "idle" {
            action = http::get(ctx, null, %q)
        }
    }

    event "halt" {
        transition "*" "stopped" {}
    }
}
`, mr.Addr(), url))).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	startAllOrFail(t, cfg)

	const entries = 10
	produce(t, mr.Addr(), entries)

	// -1 means nothing registered the count; see the helpers.
	require.NotEqual(t, -1, unsettledCount(cfg),
		"the receiver registered no unsettled count, so nothing here can see what this test is about")
	require.NotEqual(t, -1, mailboxDepth(cfg),
		"the machine registered no mailbox holder, so nothing here can see what it is carrying")

	// Both at once: the receiver raises its count several steps before the
	// event reaches the mailbox, so sampling one and then the other can land
	// between them.
	require.Eventually(t,
		func() bool { return unsettledCount(cfg) > 0 && mailboxDepth(cfg) > 0 },
		3*time.Second, time.Millisecond,
		"the shutdown never landed on a machine carrying entries it owed an acknowledgement for")

	shutdown(cfg, zap.NewNop())

	assert.Zero(t, pending(t, mr.Addr()),
		"entries the machine's transitions were owed for were left unacknowledged at exit")
}

// mailboxDepth totals what the machines are carrying — the holders
// config/fsm.go registers, read the way the shutdown phase reads them. Answers
// -1 when no machine registered one: an idle machine and an unregistered one
// are both zero to a caller, and only one of them is a missing registration.
func mailboxDepth(cfg *config.Config) int {
	found := false
	total := 0
	for _, holder := range cfg.InFlight {
		if strings.HasPrefix(holder.Name, "fsm.") {
			found = true
			total += holder.Pending()
		}
	}
	if !found {
		return -1
	}
	return total
}

// unsettledCount reports what the receivers still owe their brokers, and -1 if
// no receiver registered a count at all: nothing owed and nothing counting are
// both zero to a caller, and only one of them is a receiver being broken.
func unsettledCount(cfg *config.Config) int {
	found := false
	total := 0
	for _, holder := range cfg.InFlight {
		if strings.HasSuffix(holder.Name, " unsettled") {
			found = true
			total += holder.Pending()
		}
	}
	if !found {
		return -1
	}
	return total
}
