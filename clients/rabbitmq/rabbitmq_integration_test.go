//go:build integration

// Integration tests for the rabbitmq client against a REAL RabbitMQ broker.
//
// These are gated by the `integration` build tag AND by environment variables
// (see loadEnv). With no env set, every test skips, so the default
//
//	go test ./...
//
// is unaffected. To run against a broker, source the local credentials file
// (never read/commit it) and pass the tag:
//
//	set -a; . ./rabbitmq.env; set +a; go test -tags=integration -run TestRMQ -v ./clients/rabbitmq/...
//
// Broker provisioning requirements (exchanges must pre-exist; vinculum
// self-declares its own queues/bindings) are documented in the test plan.
//
// Env vars consumed:
//
//	RABBITMQ_HOST            broker host (required; absence => skip)
//	RABBITMQ_PORT            amqp port (default 5672)
//	RABBITMQ_TLS_PORT        amqps port (TLS tests skip if unset)
//	RABBITMQ_VHOST           vhost name, leading slash optional ("" => default "/")
//	RABBITMQ_USER/PASS       full-permission credentials
//	RABBITMQ_RO_USER/PASS    read-only credentials (permission-failure tests)
//	RABBITMQ_MGMT_URL        management API base, e.g. http://host:15672 (reconnect tests)
//	RABBITMQ_MGMT_USER/PASS  management API credentials
//	RABBITMQ_CA_CERT         path to CA PEM that signed the server cert (TLS trust test)
//	RABBITMQ_TLS_SERVERNAME  hostname in the server cert SAN (TLS trust test)
package rabbitmq_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bus "github.com/tsarna/vinculum-bus"
	rabbitmq "github.com/tsarna/vinculum/clients/rabbitmq"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap/zaptest"
)

// Pre-created exchanges the broker-config agent must provision (durable). The
// client never declares exchanges.
const (
	exTopic   = "vinculum.test.topic"
	exInbound = "vinculum.test.inbound"
	exDirect  = "vinculum.test.direct"
	exFanout  = "vinculum.test.fanout"
	exHeaders = "vinculum.test.headers"
)

// ─── Environment ─────────────────────────────────────────────────────────────

type brokerEnv struct {
	host          string
	port          string
	tlsPort       string
	vhost         string
	user, pass    string
	roUser, roPw  string
	mgmtURL       string
	mgmtUser      string
	mgmtPass      string
	caCert        string // path to a CA PEM file, ONLY when it names a real file
	caCertIsFile  bool   // true => private CA file provided; false => system trust
	tlsServerName string
}

func loadEnv(t *testing.T) brokerEnv {
	t.Helper()
	host := os.Getenv("RABBITMQ_HOST")
	if host == "" {
		t.Skip("RABBITMQ_HOST not set; skipping RabbitMQ integration test")
	}
	// RABBITMQ_CA_CERT is only meaningful as a path to a readable PEM file. If it
	// holds anything else (e.g. a description of a public CA like Let's Encrypt),
	// treat it as "use system trust" so the broker exercises the system-trust path.
	caCert := os.Getenv("RABBITMQ_CA_CERT")
	caCertIsFile := false
	if caCert != "" {
		if fi, err := os.Stat(caCert); err == nil && !fi.IsDir() {
			caCertIsFile = true
		} else {
			caCert = ""
		}
	}
	return brokerEnv{
		host:          host,
		port:          getenv("RABBITMQ_PORT", "5672"),
		tlsPort:       os.Getenv("RABBITMQ_TLS_PORT"),
		vhost:         os.Getenv("RABBITMQ_VHOST"),
		user:          getenv("RABBITMQ_USER", "guest"),
		pass:          getenv("RABBITMQ_PASS", "guest"),
		roUser:        os.Getenv("RABBITMQ_RO_USER"),
		roPw:          os.Getenv("RABBITMQ_RO_PASS"),
		mgmtURL:       os.Getenv("RABBITMQ_MGMT_URL"),
		mgmtUser:      getenv("RABBITMQ_MGMT_USER", os.Getenv("RABBITMQ_USER")),
		mgmtPass:      getenv("RABBITMQ_MGMT_PASS", os.Getenv("RABBITMQ_PASS")),
		caCert:        caCert,
		caCertIsFile:  caCertIsFile,
		tlsServerName: os.Getenv("RABBITMQ_TLS_SERVERNAME"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// vhostSeg returns the URL path segment for the vhost. A leading slash is
// tolerated; "/" or "" both mean the default vhost (empty path segment).
func (e brokerEnv) vhostSeg() string { return strings.TrimPrefix(e.vhost, "/") }

// brokerURL is the credential-free amqp:// URL placed in VCL `brokers`
// (the client supplies credentials via its auth block).
func (e brokerEnv) brokerURL() string {
	return fmt.Sprintf("amqp://%s:%s/%s", e.host, e.port, e.vhostSeg())
}

// adminURL is the credentialed amqp:// URL used by the amqp091 admin
// connection that drives/observes the broker side of each test.
func (e brokerEnv) adminURL() string {
	return fmt.Sprintf("amqp://%s:%s@%s:%s/%s",
		url.QueryEscape(e.user), url.QueryEscape(e.pass), e.host, e.port, e.vhostSeg())
}

// ─── Admin (amqp091) side: drive/observe the broker directly ─────────────────

// dialAdmin opens an independent amqp091 connection + channel for asserting on
// the broker side, with cleanup registered.
func dialAdmin(t *testing.T, e brokerEnv) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(e.adminURL())
	require.NoError(t, err, "admin dial")
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	require.NoError(t, err, "admin channel")
	t.Cleanup(func() { _ = ch.Close() })
	return ch
}

// declareSink declares an exclusive server-named queue bound to exchange with
// bindingKey, for observing what a sender published. The queue is removed when
// the admin connection closes.
func declareSink(t *testing.T, ch *amqp.Channel, exchange, bindingKey string) string {
	t.Helper()
	q, err := ch.QueueDeclare("", false, false, true, false, nil)
	require.NoError(t, err, "declare sink queue")
	require.NoError(t, ch.QueueBind(q.Name, bindingKey, exchange, false, nil),
		"bind sink %s -> %s (%s)", q.Name, exchange, bindingKey)
	return q.Name
}

// getWithin polls queue (auto-ack) until a message arrives or timeout elapses.
func getWithin(t *testing.T, ch *amqp.Channel, queue string, timeout time.Duration) (amqp.Delivery, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		d, ok, err := ch.Get(queue, true)
		require.NoError(t, err, "basic.get %s", queue)
		if ok {
			return d, true
		}
		if time.Now().After(deadline) {
			return amqp.Delivery{}, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// queueDepth returns the ready-message count via a passive declare on a fresh
// channel (a failed passive declare closes the channel, so never reuse the
// caller's channel for it).
func queueDepth(t *testing.T, e brokerEnv, queue string) int {
	t.Helper()
	ch := dialAdmin(t, e)
	q, err := ch.QueueDeclarePassive(queue, false, false, false, false, nil)
	require.NoError(t, err, "passive declare %s", queue)
	return q.Messages
}

// publishRaw publishes directly to the broker (drives the receiver side).
func publishRaw(t *testing.T, ch *amqp.Channel, exchange, routingKey string, body string, headers amqp.Table) {
	t.Helper()
	require.NoError(t, ch.PublishWithContext(context.Background(), exchange, routingKey, false, false,
		amqp.Publishing{ContentType: "text/plain", Body: []byte(body), Headers: headers}),
		"publish to %s (%s)", exchange, routingKey)
}

// ─── Vinculum side: build / start configs, observe the bus ───────────────────

// buildCfg parses VCL and returns the built Config. The event bus is started
// during Build; client wrappers are NOT yet started (call startCfg).
func buildCfg(t *testing.T, vcl string) *cfg.Config {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources([]byte(vcl)).WithLogger(zaptest.NewLogger(t)).Build()
	require.False(t, diags.HasErrors(), "config build: %s", diags.Error())
	return c
}

// startCfg starts all Startables (connecting the rabbitmq client to the broker)
// and registers reverse-order Stop cleanup.
func startCfg(t *testing.T, c *cfg.Config) {
	t.Helper()
	for _, s := range c.Startables {
		require.NoError(t, s.Start(), "start component")
	}
	t.Cleanup(func() {
		for i := len(c.Stoppables) - 1; i >= 0; i-- {
			_ = c.Stoppables[i].Stop()
		}
	})
}

// buildAndStart is the common path for happy-path tests.
func buildAndStart(t *testing.T, vcl string) *cfg.Config {
	t.Helper()
	c := buildCfg(t, vcl)
	startCfg(t, c)
	return c
}

// capture event observed on a vinculum bus.
type capture struct {
	topic  string
	msg    any
	fields map[string]string
}

type captureSub struct {
	bus.BaseSubscriber
	ch chan capture
}

func (s *captureSub) OnEvent(_ context.Context, topic string, msg any, fields map[string]string) error {
	select {
	case s.ch <- capture{topic, msg, fields}:
	default:
	}
	return nil
}

// observeBus subscribes a capture subscriber to bus.<name> for pattern.
func observeBus(t *testing.T, c *cfg.Config, busName, pattern string) *captureSub {
	t.Helper()
	b, ok := c.Buses[busName]
	require.True(t, ok, "bus %q not found", busName)
	s := &captureSub{ch: make(chan capture, 64)}
	require.NoError(t, b.Subscribe(context.Background(), pattern, s), "subscribe %s", pattern)
	return s
}

func (s *captureSub) await(t *testing.T, timeout time.Duration) capture {
	t.Helper()
	select {
	case c := <-s.ch:
		return c
	case <-time.After(timeout):
		t.Fatal("timed out waiting for bus event")
		return capture{}
	}
}

func (s *captureSub) expectNone(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case c := <-s.ch:
		t.Fatalf("expected no bus event, got topic %q", c.topic)
	case <-time.After(within):
	}
}

// publishBus is a convenience for the bus-side injection used by sender tests.
// PublishSync drives the subscriber chain (and the sender's publisher confirm)
// synchronously and returns the first subscriber error.
func publishBus(c *cfg.Config, topic string, payload any) error {
	return c.Buses["main"].PublishSync(context.Background(), topic, payload)
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s.%d", prefix, time.Now().UnixNano())
}

// msgString renders a bus message payload as a string. The rabbitmq receiver
// deserializes via the client's cty-aware wire format, so messages it injects
// onto the bus arrive as cty.Value; messages published from Go arrive as the
// raw Go value. This handles both.
func msgString(m any) string {
	if v, ok := m.(cty.Value); ok {
		if v.Type() == cty.String {
			return v.AsString()
		}
		return v.GoString()
	}
	return fmt.Sprintf("%v", m)
}

// clientHeader returns the standard `bus "main" {}` + client header with auth,
// into which a sender/receiver body is interpolated. topLevel is appended after
// the client block (for `subscription` blocks).
func vclConfig(e brokerEnv, brokerURL, clientBody, topLevel string) string {
	return fmt.Sprintf(`bus "main" {}

client "rabbitmq" "events" {
  brokers = [%q]
  auth {
    username = %q
    password = %q
  }
%s
}

%s
`, brokerURL, e.user, e.pass, clientBody, topLevel)
}

// ─── Phase 0 — Connectivity & lifecycle ──────────────────────────────────────

func TestRMQ_Phase0_Connect(t *testing.T) {
	e := loadEnv(t)
	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange = "`+exTopic+`"
  }`, "")
	c := buildCfg(t, vcl)
	// Start the wrapper directly so we can assert the connect succeeded.
	w := c.Clients["rabbitmq"]["events"].(*rabbitmq.RMQClientWrapper)
	require.NoError(t, w.Start(), "initial connect to broker")
	t.Cleanup(func() { _ = w.Stop() })
}

func TestRMQ_Phase0_OnConnectHook(t *testing.T) {
	e := loadEnv(t)
	vcl := vclConfig(e, e.brokerURL(), `
  on_connect = send(ctx, bus.main, "lifecycle/connected", "up")

  sender "out" {
    exchange = "`+exTopic+`"
  }`, "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "lifecycle/#")
	startCfg(t, c) // on_connect fires synchronously during Start
	got := sub.await(t, 5*time.Second)
	assert.Equal(t, "lifecycle/connected", got.topic)
}

func TestRMQ_Phase0_BadPassword(t *testing.T) {
	e := loadEnv(t)
	vcl := fmt.Sprintf(`bus "main" {}
client "rabbitmq" "events" {
  brokers = [%q]
  connection_timeout = "5s"
  auth {
    username = %q
    password = "definitely-the-wrong-password"
  }
  sender "out" { exchange = %q }
}
`, e.brokerURL(), e.user, exTopic)
	c := buildCfg(t, vcl)
	w := c.Clients["rabbitmq"]["events"].(*rabbitmq.RMQClientWrapper)
	// Initial connect failures surface synchronously; they do NOT retry.
	err := w.Start()
	t.Cleanup(func() { _ = w.Stop() })
	require.Error(t, err, "bad password should fail Start, not hang")
}

// ─── Phase 1 — Sender (bus -> RabbitMQ) ──────────────────────────────────────

func TestRMQ_Phase1_DefaultSlashToDot(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	sink := declareSink(t, admin, exTopic, "#")

	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" { exchange = "`+exTopic+`" }`,
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "sensor/a/reading", "25"))

	d, ok := getWithin(t, admin, sink, 5*time.Second)
	require.True(t, ok, "sender message not observed on broker")
	assert.Equal(t, "sensor.a.reading", d.RoutingKey)
	assert.Equal(t, "25", string(d.Body))
}

func TestRMQ_Phase1_TopicBlockRoutingKeyOverride(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	sink := declareSink(t, admin, exTopic, "#")

	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange = "`+exTopic+`"
    topic "alerts/#" {
      routing_key = "alerts"
    }
  }`,
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "alerts/disk/full", "boom"))

	d, ok := getWithin(t, admin, sink, 5*time.Second)
	require.True(t, ok)
	assert.Equal(t, "alerts", d.RoutingKey)
}

func TestRMQ_Phase1_DefaultTopicTransforms(t *testing.T) {
	e := loadEnv(t)

	t.Run("verbatim", func(t *testing.T) {
		admin := dialAdmin(t, e)
		sink := declareSink(t, admin, exTopic, "#")
		vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange                = "`+exTopic+`"
    default_topic_transform = "verbatim"
  }`,
			`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
		c := buildAndStart(t, vcl)
		require.NoError(t, publishBus(c, "a/b/c", "x"))
		d, ok := getWithin(t, admin, sink, 5*time.Second)
		require.True(t, ok)
		assert.Equal(t, "a/b/c", d.RoutingKey)
	})

	t.Run("error", func(t *testing.T) {
		vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange                = "`+exTopic+`"
    default_topic_transform = "error"
  }`,
			`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
		c := buildAndStart(t, vcl)
		err := publishBus(c, "no/mapping", "x")
		require.Error(t, err, "transform=error should reject unmapped topics")
	})

	t.Run("ignore", func(t *testing.T) {
		admin := dialAdmin(t, e)
		sink := declareSink(t, admin, exTopic, "#")
		vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange                = "`+exTopic+`"
    default_topic_transform = "ignore"
  }`,
			`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
		c := buildAndStart(t, vcl)
		require.NoError(t, publishBus(c, "no/mapping", "x"))
		_, ok := getWithin(t, admin, sink, 1500*time.Millisecond)
		assert.False(t, ok, "transform=ignore should drop the message")
	})
}

func TestRMQ_Phase1_ConfirmModeFalseStillDelivers(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	sink := declareSink(t, admin, exTopic, "#")

	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange     = "`+exTopic+`"
    confirm_mode = false
  }`,
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "fire/forget", "x"))
	d, ok := getWithin(t, admin, sink, 5*time.Second)
	require.True(t, ok)
	assert.Equal(t, "fire.forget", d.RoutingKey)
}

func TestRMQ_Phase1_MandatoryUnroutableErrors(t *testing.T) {
	e := loadEnv(t)
	// mandatory=true + confirm_mode=true (default): the broker acks unroutable
	// returns under publisher confirms, but the sender correlates the Basic.Return
	// (via MessageId) and surfaces it as a publish error from OnEvent — matching
	// RABBITMQ-SPEC.md and the convention of every other vinculum messaging client.
	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange                = "`+exDirect+`"
    mandatory               = true
    default_topic_transform = "verbatim"
  }`,
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl)

	err := publishBus(c, "no-binding-for-this-key", "x")
	require.Error(t, err, "mandatory + unroutable should surface a publish error")
	assert.Contains(t, err.Error(), "returned by broker")
}

func TestRMQ_Phase1_PersistentDeliveryMode(t *testing.T) {
	e := loadEnv(t)

	cases := []struct {
		name       string
		persistent string
		wantMode   uint8
	}{
		{"persistent_true", "true", 2},
		{"persistent_false", "false", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			admin := dialAdmin(t, e)
			sink := declareSink(t, admin, exTopic, "#")
			vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange   = "`+exTopic+`"
    persistent = `+tc.persistent+`
  }`,
				`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
			c := buildAndStart(t, vcl)
			require.NoError(t, publishBus(c, "p/mode", "x"))
			d, ok := getWithin(t, admin, sink, 5*time.Second)
			require.True(t, ok)
			assert.Equal(t, tc.wantMode, d.DeliveryMode)
		})
	}
}

func TestRMQ_Phase1_WireFormats(t *testing.T) {
	e := loadEnv(t)

	t.Run("json", func(t *testing.T) {
		admin := dialAdmin(t, e)
		sink := declareSink(t, admin, exTopic, "#")
		vcl := vclConfig(e, e.brokerURL(), `
  wire_format = "json"
  sender "out" { exchange = "`+exTopic+`" }`,
			`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
		c := buildAndStart(t, vcl)
		require.NoError(t, publishBus(c, "j/son", map[string]any{"a": 1, "b": "two"}))
		d, ok := getWithin(t, admin, sink, 5*time.Second)
		require.True(t, ok)
		assert.JSONEq(t, `{"a":1,"b":"two"}`, string(d.Body))
	})

	t.Run("bytes", func(t *testing.T) {
		admin := dialAdmin(t, e)
		sink := declareSink(t, admin, exTopic, "#")
		vcl := vclConfig(e, e.brokerURL(), `
  wire_format = "bytes"
  sender "out" { exchange = "`+exTopic+`" }`,
			`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
		c := buildAndStart(t, vcl)
		require.NoError(t, publishBus(c, "by/tes", []byte("raw-bytes")))
		d, ok := getWithin(t, admin, sink, 5*time.Second)
		require.True(t, ok)
		assert.Equal(t, "raw-bytes", string(d.Body))
	})
}

func TestRMQ_Phase1_FieldsBecomeHeaders(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	sink := declareSink(t, admin, exTopic, "#")

	// A subscription action calls send() with explicit fields; the sender must
	// encode those vinculum fields as AMQP headers.
	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" { exchange = "`+exTopic+`" }`,
		`subscription "s" {
  target = bus.main
  topics = ["trigger"]
  action = send(ctx, client.events.sender.out, "out/key", ctx.msg, {deviceId = "abc", kind = "temp"})
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "trigger", "payload"))
	d, ok := getWithin(t, admin, sink, 5*time.Second)
	require.True(t, ok)
	require.NotNil(t, d.Headers)
	assert.Equal(t, "abc", fmt.Sprintf("%v", d.Headers["deviceId"]))
	assert.Equal(t, "temp", fmt.Sprintf("%v", d.Headers["kind"]))
}

// ─── Phase 2 — Receiver (RabbitMQ -> bus) ────────────────────────────────────

func TestRMQ_Phase2_BasicConsumeDotToSlash(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.recv")

	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  receiver "in" {
    queue      = %q
    subscriber = bus.main
    declare {
      durable     = false
      auto_delete = true
    }
    binding "sensor.#" { exchange = "`+exInbound+`" }
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "sensor/#")
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "sensor.a.reading", "value", nil)

	got := sub.await(t, 5*time.Second)
	assert.Equal(t, "sensor/a/reading", got.topic)
	assert.Equal(t, "value", msgString(got.msg))
}

func TestRMQ_Phase2_SubscriptionNamedHashCapture(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.recv")

	// #name captures the matched multi-word tail (dot-joined) into ctx.fields.
	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  receiver "in" {
    queue      = %q
    subscriber = bus.main
    declare {
      durable     = false
      auto_delete = true
    }
    binding "alerts.#" { exchange = "`+exInbound+`" }
    subscription "alerts.#suffix" {
      vinculum_topic = "alerts/${ctx.fields.suffix}"
    }
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "alerts/#")
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "alerts.disk.full", "v", nil)

	got := sub.await(t, 5*time.Second)
	assert.Equal(t, "alerts/disk.full", got.topic)
}

func TestRMQ_Phase2_SubscriptionNamedWildcard(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.recv")

	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  receiver "in" {
    queue      = %q
    subscriber = bus.main
    declare {
      durable     = false
      auto_delete = true
    }
    binding "sensor.#" { exchange = "`+exInbound+`" }
    subscription "sensor.*deviceId.reading" {
      vinculum_topic = "sensor/${ctx.fields.deviceId}/reading"
    }
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "sensor/#")
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "sensor.abc.reading", "v", nil)

	got := sub.await(t, 5*time.Second)
	assert.Equal(t, "sensor/abc/reading", got.topic)
}

func TestRMQ_Phase2_HeadersBecomeFields(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.recv")

	// AMQP headers become receiver-side ctx.fields. We verify at the action
	// (where fields are consumed), encoding the header value into the topic —
	// the bus hop does not necessarily carry the fields map downstream.
	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  receiver "in" {
    queue  = %q
    action = send(ctx, bus.main, "hdr/${ctx.fields.source}", ctx.msg)
    declare {
      durable     = false
      auto_delete = true
    }
    binding "hdr.#" { exchange = "`+exInbound+`" }
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "hdr/#")
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "hdr.one", "v", amqp.Table{"source": "unit-test"})

	got := sub.await(t, 5*time.Second)
	assert.Equal(t, "hdr/unit-test", got.topic)
}

func TestRMQ_Phase2_ManualNackNoRequeue(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.recv")

	// With no subscription blocks and default_routing_key_transform="error",
	// every delivery fails routing => the receiver nacks WITHOUT requeue and
	// never delivers. (A deserialize error does NOT nack — the receiver falls
	// back to raw bytes — so we drive the nack via the error transform.)
	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  receiver "in" {
    queue                         = %q
    subscriber                    = bus.main
    default_routing_key_transform = "error"
    declare {
      durable     = false
      auto_delete = true
    }
    binding "bad.#" { exchange = "`+exInbound+`" }
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "#")
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "bad.message", "x", nil)

	sub.expectNone(t, 1500*time.Millisecond)
	assert.Equal(t, 0, queueDepth(t, e, queue), "nacked message must not be requeued")
}

func TestRMQ_Phase2_ActionHandler(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.recv")

	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  receiver "in" {
    queue  = %q
    action = send(ctx, bus.main, "got/${ctx.topic}", ctx.msg)
    declare {
      durable     = false
      auto_delete = true
    }
    binding "act.#" { exchange = "`+exInbound+`" }
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "got/#")
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "act.one", "hello", nil)

	got := sub.await(t, 5*time.Second)
	assert.Equal(t, "got/act/one", got.topic)
	assert.Equal(t, "hello", msgString(got.msg))
}

func TestRMQ_Phase2_PrefetchBurstDelivery(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.recv")

	// Functional check: prefetch=1 must still deliver an entire burst (the
	// setting throttles unacked in-flight count, but all messages eventually
	// flow). True in-flight throttling is observed manually / via mgmt stats —
	// VCL has no blocking handler primitive to hold an ack open here.
	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  receiver "in" {
    queue      = %q
    prefetch   = 1
    subscriber = bus.main
    declare {
      durable     = false
      auto_delete = true
    }
    binding "pf.#" { exchange = "`+exInbound+`" }
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "pf/#")
	startCfg(t, c)

	const burst = 5
	for i := 0; i < burst; i++ {
		publishRaw(t, admin, exInbound, "pf.x", fmt.Sprintf("m%d", i), nil)
	}
	for i := 0; i < burst; i++ {
		got := sub.await(t, 5*time.Second)
		assert.Equal(t, "pf/x", got.topic)
	}
}

// ─── Phase 3 — Round-trip (sender + receiver + topology together) ────────────

func TestRMQ_Phase3_RoundTrip(t *testing.T) {
	e := loadEnv(t)
	queue := uniqueName("vinculum.test.rt")

	// bus "out/#" -> sender -> exTopic -> (self-declared queue bound "out.#")
	// -> receiver action -> bus "in/<topic>".
	vcl := vclConfig(e, e.brokerURL(), fmt.Sprintf(`
  sender "out" { exchange = "`+exTopic+`" }

  receiver "in" {
    queue  = %q
    action = send(ctx, bus.main, "in/${ctx.topic}", ctx.msg)
    declare {
      durable     = false
      auto_delete = true
    }
    binding "out.#" { exchange = "`+exTopic+`" }
  }`, queue),
		`subscription "s" {
  target     = bus.main
  topics     = ["out/#"]
  subscriber = client.events.sender.out
}`)
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "in/#")
	startCfg(t, c)

	require.NoError(t, publishBus(c, "out/a/b", "hello"))

	got := sub.await(t, 8*time.Second)
	assert.Equal(t, "in/out/a/b", got.topic)
	assert.Equal(t, "hello", msgString(got.msg))
}
