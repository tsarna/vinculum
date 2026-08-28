package mqtt

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
)

// The regression this phase exists for. Start used to wait for the first
// connection, and the boot loop is serial, so one unreachable broker held up
// every component after it — HTTP listeners included, with nothing to do with
// MQTT. Bounded rather than merely observed: "returns eventually" is what the
// old code did too, given a broker that came up.
func TestStartDoesNotWaitForTheBroker(t *testing.T) {
	c, _ := buildMQTT(t, mqttNoHooks) // brokers = ["mqtt://127.0.0.1:1"]
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

// Having declined to wait, the client still has to say so honestly. "not
// started" would claim nothing is being attempted; "not connected" is the same
// state an outage produces, and recovers the same way.
func TestReadyReportsNotConnectedWhileConnecting(t *testing.T) {
	c, _ := buildMQTT(t, mqttNoHooks)
	t.Cleanup(func() { _ = c.Stop() })

	require.NoError(t, c.Start())

	err := c.Ready(context.Background())
	require.Error(t, err)
	assert.Equal(t, "not connected", err.Error())
}

// Stop has to unblock the connect goroutine as well as tear down, or every
// client that never reached its broker leaks one for the life of the process.
// The library's own Stop cannot do it — its connection manager handle is only
// set once a connection comes up — so cancelling the context it was built from
// is what actually reaches the retry loop.
func TestStopUnblocksAConnectThatNeverSucceeded(t *testing.T) {
	c, _ := buildMQTT(t, mqttNoHooks)
	require.NoError(t, c.Start())

	done := make(chan error, 1)
	go func() { done <- c.Stop() }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung on a client that never connected")
	}
}

// A publish before the first connection reports the truth rather than panicking
// on a publisher whose publish function has not been injected yet. This is the
// cost of not waiting, and it is the right cost: an error naming the state,
// which resolves itself when the broker appears.
func TestPublishBeforeConnectFails(t *testing.T) {
	const withSender = `
client "mqtt" "broker" {
    brokers = ["mqtt://127.0.0.1:1"]

    sender "out" {
        topic "#" {
            mqtt_topic = "out/${ctx.topic}"
        }
    }
}
`
	c, _ := buildMQTT(t, withSender)
	t.Cleanup(func() { _ = c.Stop() })
	require.NoError(t, c.Start())

	err := c.OnEvent(context.Background(), "t/1", "hello", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet connected")
}

// Start is not terminal for an unreachable broker: retrying is exactly what
// fixes it, and autopaho is already doing so.
func TestStartIsNotTerminalForAnUnreachableBroker(t *testing.T) {
	c, _ := buildMQTT(t, mqttNoHooks)
	t.Cleanup(func() { _ = c.Stop() })

	assert.False(t, cfg.IsTerminal(c.Start()))
}
