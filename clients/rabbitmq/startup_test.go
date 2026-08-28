package rabbitmq

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
)

// The regression this phase exists for. A failed first dial used to return from
// Start before the reconnect watcher was ever spawned, so the retry schedule
// the `reconnect` block configures never ran and the client was dead for the
// life of the process — reporting "not ready" accurately and forever.
func TestStartDoesNotFailForAnUnreachableBroker(t *testing.T) {
	c, _ := buildRMQ(t, rmqNoHooks) // brokers = ["amqp://127.0.0.1:1/"]
	t.Cleanup(func() { _ = c.Stop() })

	done := make(chan error, 1)
	go func() { done <- c.Start() }()

	select {
	case err := <-done:
		require.NoError(t, err, "an unreachable broker is not a boot failure")
	case <-time.After(2 * time.Second):
		t.Fatal("Start blocked on a broker that is not listening")
	}
}

// Having declined to fail, the client still has to say so honestly. This is the
// half that used to be wrong even as a report: the wrapper only published its
// client handle after a successful Start, so a broker that was down at boot
// read as "not started" — describing a client that was not trying, when the
// point of the change is that it is.
func TestReadyReportsNotConnectedWhileConnecting(t *testing.T) {
	c, _ := buildRMQ(t, rmqNoHooks)
	t.Cleanup(func() { _ = c.Stop() })

	require.NoError(t, c.Start())

	err := c.Ready(context.Background())
	require.Error(t, err)
	assert.Equal(t, "not connected", err.Error())
}

// Stop has to end the connect loop as well as tear down. The initial connect
// retries without limit, so a client that never reached a broker would
// otherwise keep dialing for the life of the process.
func TestStopEndsAConnectThatNeverSucceeded(t *testing.T) {
	c, _ := buildRMQ(t, rmqNoHooks)
	require.NoError(t, c.Start())

	// Let it get into the retry loop rather than catching it before it starts.
	time.Sleep(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- c.Stop() }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung on a client that never connected")
	}
}

// A send while the broker is away reports the state rather than panicking on a
// sender with no channel. This is the cost of not failing at boot, and it is
// the right cost: an error naming the state, which resolves itself when the
// broker appears.
func TestSendBeforeConnectFails(t *testing.T) {
	const withSender = `
client "rabbitmq" "broker" {
  brokers = ["amqp://127.0.0.1:1/"]

  sender "out" {
    exchange = "ex"
  }
}
`
	c, _ := buildRMQ(t, withSender)
	t.Cleanup(func() { _ = c.Stop() })
	require.NoError(t, c.Start())

	assert.Error(t, c.OnEvent(context.Background(), "t/1", "hello", nil))
}

// Not terminal for an unreachable broker: retrying is exactly what fixes it,
// and the connect loop is already doing so.
func TestStartIsNotTerminalForAnUnreachableBroker(t *testing.T) {
	c, _ := buildRMQ(t, rmqNoHooks)
	t.Cleanup(func() { _ = c.Stop() })

	assert.False(t, cfg.IsTerminal(c.Start()))
}
