package rabbitmq

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rmqsender "github.com/tsarna/vinculum-rabbitmq/sender"
	cfg "github.com/tsarna/vinculum/config"
)

// Named sender addressing. The cty shape (`client.X.senders` and
// `client.X.sender.<name>`) is verified in the black-box tests; these
// white-box tests focus on the actual dispatch behavior.

// newUnstartedWrapper builds an RMQClientWrapper with the given sender names
// and unwired RMQSenders. The wrapper is in the post-Start state (senders
// populated) but each sender is disconnected, so OnEvent will return errors
// — useful for verifying dispatch fan-out without standing up a broker.
func newUnstartedWrapper(t *testing.T, names ...string) *RMQClientWrapper {
	t.Helper()

	specs := make([]builtSenderSpec, 0, len(names))
	proxies := make(map[string]*RMQSenderProxy, len(names))
	senders := make([]*rmqsender.RMQSender, 0, len(names))

	for _, name := range names {
		specs = append(specs, builtSenderSpec{name: name, exchange: "test-exchange"})
		proxies[name] = &RMQSenderProxy{clientName: "test", senderName: name}
		s, err := rmqsender.NewSender().WithExchange("test-exchange").Build()
		require.NoError(t, err)
		senders = append(senders, s)
	}

	return &RMQClientWrapper{
		BaseClient:    cfg.BaseClient{Name: "test"},
		senderSpecs:   specs,
		senderProxies: proxies,
		senders:       senders,
	}
}

func TestProxy_NotStarted_ReturnsError(t *testing.T) {
	p := &RMQSenderProxy{clientName: "c", senderName: "s"}
	err := p.OnEvent(context.Background(), "topic", "msg", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet started")
	assert.Contains(t, err.Error(), "sender \"s\"")
}

func TestProxy_WireSender_ForwardsOnEvent(t *testing.T) {
	// An unwired sender's OnEvent returns "not yet connected" — proves the
	// proxy actually forwarded to the underlying sender rather than holding
	// onto its own "not yet started" path.
	s, err := rmqsender.NewSender().WithExchange("e").Build()
	require.NoError(t, err)

	p := &RMQSenderProxy{clientName: "c", senderName: "s"}
	p.wireSender(s)

	err = p.OnEvent(context.Background(), "topic", "msg", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet connected", "should be the sender's error, not the proxy's")
}

func TestProxy_ClientAndSenderNamesInError(t *testing.T) {
	p := &RMQSenderProxy{clientName: "events", senderName: "main"}
	err := p.OnEvent(context.Background(), "t", "m", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client \"events\"")
	assert.Contains(t, err.Error(), "sender \"main\"")
}

func TestWrapperOnEvent_NoSendersConfigured(t *testing.T) {
	// senderSpecs empty → config-time error; not the "not yet started" one.
	w := &RMQClientWrapper{BaseClient: cfg.BaseClient{Name: "test"}}
	err := w.OnEvent(context.Background(), "t", "m", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no senders configured")
}

func TestWrapperOnEvent_NotYetStarted(t *testing.T) {
	// senderSpecs populated (config-time state) but senders slice is empty
	// (pre-Start state).
	w := &RMQClientWrapper{
		BaseClient:    cfg.BaseClient{Name: "test"},
		senderSpecs:   []builtSenderSpec{{name: "x", exchange: "e"}},
		senderProxies: map[string]*RMQSenderProxy{"x": {clientName: "test", senderName: "x"}},
	}
	err := w.OnEvent(context.Background(), "t", "m", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet started")
}

func TestWrapperOnEvent_FanOut_CallsAllSenders(t *testing.T) {
	// Three unwired senders → each will fail with "not yet connected".
	// We verify the wrapper called all three by checking the combined error.
	w := newUnstartedWrapper(t, "a", "b", "c")

	err := w.OnEvent(context.Background(), "topic", "msg", nil)
	require.Error(t, err)
	// 3 errors → "multiple publish errors" branch
	assert.Contains(t, err.Error(), "multiple publish errors")
	// And each underlying sender error appears
	assert.Equal(t, 3, strings.Count(err.Error(), "not yet connected"),
		"expected the per-sender error to appear three times; got: %s", err.Error())
}

func TestWrapperOnEvent_SingleSender_ReturnsUnwrappedError(t *testing.T) {
	// One unwired sender → single error → wrapper returns it directly
	// (not the "multiple publish errors" wrapper).
	w := newUnstartedWrapper(t, "only")

	err := w.OnEvent(context.Background(), "t", "m", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet connected")
	assert.NotContains(t, err.Error(), "multiple publish errors")
}

func TestCtyValue_NoSenders_ReturnsPlainCapsule(t *testing.T) {
	// Mirrors the black-box test, but directly inspects CtyValue() without
	// going through full config parsing.
	w := &RMQClientWrapper{BaseClient: cfg.BaseClient{Name: "test"}}
	val := w.CtyValue()
	assert.Equal(t, "client", val.Type().FriendlyName(),
		"with no senders the value is the plain client capsule")
}

func TestCtyValue_WithSenders_ExposesSendersAndSender(t *testing.T) {
	w := &RMQClientWrapper{
		BaseClient: cfg.BaseClient{Name: "test"},
		senderProxies: map[string]*RMQSenderProxy{
			"main":  {clientName: "test", senderName: "main"},
			"audit": {clientName: "test", senderName: "audit"},
		},
	}
	val := w.CtyValue()
	require.True(t, val.Type().IsObjectType())
	m := val.AsValueMap()
	require.Contains(t, m, "senders")
	require.Contains(t, m, "sender")
	require.True(t, m["sender"].Type().IsObjectType())

	senderMap := m["sender"].AsValueMap()
	require.Contains(t, senderMap, "main")
	require.Contains(t, senderMap, "audit")
	assert.Equal(t, "subscriber", senderMap["main"].Type().FriendlyName())
	assert.Equal(t, "subscriber", senderMap["audit"].Type().FriendlyName())
	assert.Equal(t, "subscriber", m["senders"].Type().FriendlyName())
}
