//go:build integration

// Phases 4-7 of the rabbitmq integration suite: TLS, reconnect/failover,
// auth/permission failures, and non-topic exchange types. Shares the harness
// defined in rabbitmq_integration_test.go. See that file's header for env vars
// and how to run.
package rabbitmq_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rabbitmq "github.com/tsarna/vinculum/clients/rabbitmq"
)

// vclWithAuth is vclConfig with explicit (non-default) credentials, used by the
// read-only permission tests.
func vclWithAuth(e brokerEnv, brokerURL, user, pass, clientBody, topLevel string) string {
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
`, brokerURL, user, pass, clientBody, topLevel)
}

// amqpsURL builds the TLS broker URL, preferring the cert's SAN hostname so
// verification can succeed.
func (e brokerEnv) amqpsURL() string {
	host := e.host
	if e.tlsServerName != "" {
		host = e.tlsServerName
	}
	return fmt.Sprintf("amqps://%s:%s/%s", host, e.tlsPort, e.vhostSeg())
}

func requireRO(t *testing.T, e brokerEnv) {
	t.Helper()
	if e.roUser == "" {
		t.Skip("RABBITMQ_RO_USER not set; skipping permission test")
	}
}

// closeVhostConnections closes every broker connection on the client's vhost via
// the management API, to trigger the client's reconnect path.
func closeVhostConnections(t *testing.T, e brokerEnv) {
	t.Helper()
	if e.mgmtURL == "" {
		t.Skip("RABBITMQ_MGMT_URL not set; skipping reconnect test")
	}
	wantVhost := e.vhostSeg()
	if wantVhost == "" {
		wantVhost = "/"
	}
	base := strings.TrimRight(e.mgmtURL, "/")
	httpc := &http.Client{Timeout: 10 * time.Second}

	// RabbitMQ's management stats are eventually consistent, so a just-opened
	// connection may not be listed for a couple of seconds. Poll until we find
	// and close at least one matching connection.
	deadline := time.Now().Add(20 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodGet, base+"/api/connections", nil)
		require.NoError(t, err)
		req.SetBasicAuth(e.mgmtUser, e.mgmtPass)
		resp, err := httpc.Do(req)
		require.NoError(t, err, "list connections")
		require.Equal(t, http.StatusOK, resp.StatusCode, "list connections status")
		var conns []struct {
			Name  string `json:"name"`
			Vhost string `json:"vhost"`
			User  string `json:"user"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&conns))
		resp.Body.Close()

		vhostsSeen := map[string]int{}
		closed := 0
		for _, cn := range conns {
			vhostsSeen[cn.Vhost]++
			// Match on vhost, or our client user on any vhost (covers brokers that
			// report the vhost differently than the URL path).
			if cn.Vhost != wantVhost && cn.User != e.user {
				continue
			}
			dreq, err := http.NewRequest(http.MethodDelete, base+"/api/connections/"+url.PathEscape(cn.Name), nil)
			require.NoError(t, err)
			dreq.SetBasicAuth(e.mgmtUser, e.mgmtPass)
			dresp, err := httpc.Do(dreq)
			require.NoError(t, err, "delete connection %s", cn.Name)
			dresp.Body.Close()
			closed++
		}
		if closed > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no connection to close (wanted vhost %q or user %q); total=%d vhosts seen=%v",
				wantVhost, e.user, len(conns), vhostsSeen)
		}
		time.Sleep(1 * time.Second)
	}
}

// ─── Phase 4 — TLS (server-trust only; no mTLS) ──────────────────────────────

func TestRMQ_Phase4_TLSWithCATrust(t *testing.T) {
	e := loadEnv(t)
	if e.tlsPort == "" || !e.caCertIsFile {
		t.Skip("RABBITMQ_TLS_PORT unset or RABBITMQ_CA_CERT is not a PEM file; skipping private-CA trust test")
	}
	admin := dialAdmin(t, e) // plaintext admin connection to observe
	sink := declareSink(t, admin, exTopic, "#")

	vcl := vclConfig(e, e.amqpsURL(), fmt.Sprintf(`
  tls {
    ca_cert = %q
  }
  sender "out" { exchange = "`+exTopic+`" }`, e.caCert),
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "tls/ok", "secure"))
	d, ok := getWithin(t, admin, sink, 5*time.Second)
	require.True(t, ok, "message did not flow over the TLS connection")
	assert.Equal(t, "secure", string(d.Body))
}

func TestRMQ_Phase4_TLSSystemTrust(t *testing.T) {
	e := loadEnv(t)
	if e.tlsPort == "" {
		t.Skip("RABBITMQ_TLS_PORT not set; skipping TLS test")
	}
	if e.caCertIsFile {
		t.Skip("broker uses a private CA (RABBITMQ_CA_CERT is a PEM file); system-trust path is N/A")
	}
	vcl := vclConfig(e, e.amqpsURL(), `
  sender "out" { exchange = "`+exTopic+`" }`, "")
	c := buildCfg(t, vcl)
	w := c.Clients["rabbitmq"]["events"].(*rabbitmq.RMQClientWrapper)
	require.NoError(t, w.Start(), "amqps with system trust should connect")
	t.Cleanup(func() { _ = w.Stop() })
}

func TestRMQ_Phase4_TLSInsecureSkipVerify(t *testing.T) {
	e := loadEnv(t)
	if e.tlsPort == "" {
		t.Skip("RABBITMQ_TLS_PORT not set; skipping TLS test")
	}
	// Connect by raw host (cert SAN may not match) to make verification matter.
	rawURL := fmt.Sprintf("amqps://%s:%s/%s", e.host, e.tlsPort, e.vhostSeg())

	t.Run("skip_verify_connects", func(t *testing.T) {
		vcl := vclConfig(e, rawURL, `
  tls { insecure_skip_verify = true }
  sender "out" { exchange = "`+exTopic+`" }`, "")
		c := buildCfg(t, vcl)
		w := c.Clients["rabbitmq"]["events"].(*rabbitmq.RMQClientWrapper)
		require.NoError(t, w.Start(), "insecure_skip_verify should connect")
		t.Cleanup(func() { _ = w.Stop() })
	})

	t.Run("no_trust_fails", func(t *testing.T) {
		if !e.caCertIsFile {
			t.Skip("broker cert chains to a public CA; cannot guarantee a verification failure")
		}
		vcl := vclConfig(e, rawURL, `
  tls { insecure_skip_verify = false }
  sender "out" { exchange = "`+exTopic+`" }`, "")
		c := buildCfg(t, vcl)
		w := c.Clients["rabbitmq"]["events"].(*rabbitmq.RMQClientWrapper)
		err := w.Start()
		t.Cleanup(func() { _ = w.Stop() })
		require.Error(t, err, "amqps to a private-CA broker without trust must fail verification")
	})
}

// ─── Phase 5 — Reconnect & failover ──────────────────────────────────────────

func TestRMQ_Phase5_ReconnectAfterConnectionClose(t *testing.T) {
	e := loadEnv(t)
	if e.mgmtURL == "" {
		t.Skip("RABBITMQ_MGMT_URL not set; skipping reconnect test")
	}
	vcl := vclConfig(e, e.brokerURL(), `
  on_connect    = send(ctx, bus.main, "lifecycle/connected", "up")
  on_disconnect = send(ctx, bus.main, "lifecycle/disconnected", "down")
  sender "out" { exchange = "`+exTopic+`" }`, "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "lifecycle/#")
	startCfg(t, c)

	first := sub.await(t, 5*time.Second)
	require.Equal(t, "lifecycle/connected", first.topic)

	closeVhostConnections(t, e)

	// on_disconnect fires before the reconnect; on_connect fires after. Collect
	// until we've seen both (order-tolerant to avoid races on event delivery).
	sawDisc, sawConn := false, false
	deadline := time.After(25 * time.Second)
	for !(sawDisc && sawConn) {
		select {
		case ev := <-sub.ch:
			switch ev.topic {
			case "lifecycle/disconnected":
				sawDisc = true
			case "lifecycle/connected":
				sawConn = true
			}
		case <-deadline:
			t.Fatalf("reconnect lifecycle incomplete: disconnected=%v connected=%v", sawDisc, sawConn)
		}
	}
}

func TestRMQ_Phase5_FailoverToSecondBroker(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	sink := declareSink(t, admin, exTopic, "#")

	dead := fmt.Sprintf("amqp://127.0.0.1:1/%s", e.vhostSeg()) // refused fast
	vcl := fmt.Sprintf(`bus "main" {}

client "rabbitmq" "events" {
  brokers = [%q, %q]
  connection_timeout = "3s"
  auth {
    username = %q
    password = %q
  }
  sender "out" { exchange = %q }
}

subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}
`, dead, e.brokerURL(), e.user, e.pass, exTopic)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "failover/test", "x"))
	d, ok := getWithin(t, admin, sink, 5*time.Second)
	require.True(t, ok, "client did not fail over to the reachable broker")
	assert.Equal(t, "failover.test", d.RoutingKey)
}

// ─── Phase 6 — Auth & permission failures (read-only user) ───────────────────

func TestRMQ_Phase6_ReadOnlyPublishDenied(t *testing.T) {
	e := loadEnv(t)
	requireRO(t, e)
	vcl := vclWithAuth(e, e.brokerURL(), e.roUser, e.roPw, `
  sender "out" { exchange = "`+exTopic+`" }`,
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl) // a read-only user can still connect

	err := publishBus(c, "denied/write", "x")
	require.Error(t, err, "read-only user must not be able to publish")
}

func TestRMQ_Phase6_ReadOnlyDeclareDenied(t *testing.T) {
	e := loadEnv(t)
	requireRO(t, e)
	queue := uniqueName("vinculum.test.rodecl")
	vcl := vclWithAuth(e, e.brokerURL(), e.roUser, e.roPw, fmt.Sprintf(`
  receiver "in" {
    queue      = %q
    subscriber = bus.main
    declare {
      durable     = false
      auto_delete = true
    }
    binding "x.#" { exchange = "`+exInbound+`" }
  }`, queue), "")
	c := buildCfg(t, vcl)
	w := c.Clients["rabbitmq"]["events"].(*rabbitmq.RMQClientWrapper)
	err := w.Start()
	t.Cleanup(func() { _ = w.Stop() })
	require.Error(t, err, "read-only user must not be able to declare a queue")
}

func TestRMQ_Phase6_ReadOnlyConsumeAllowed(t *testing.T) {
	e := loadEnv(t)
	requireRO(t, e)
	admin := dialAdmin(t, e)
	queue := uniqueName("vinculum.test.roconsume")

	// Admin pre-creates and binds the queue (the read-only user cannot).
	_, err := admin.QueueDeclare(queue, false, false, false, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.QueueDelete(queue, false, false, false) })
	require.NoError(t, admin.QueueBind(queue, "ro.#", exInbound, false, nil))

	// No declare / no binding in VCL — the receiver passively consumes.
	vcl := vclWithAuth(e, e.brokerURL(), e.roUser, e.roPw, fmt.Sprintf(`
  receiver "in" {
    queue      = %q
    subscriber = bus.main
  }`, queue), "")
	c := buildCfg(t, vcl)
	sub := observeBus(t, c, "main", "ro/#")
	startCfg(t, c)

	publishRaw(t, admin, exInbound, "ro.ok", "v", nil)
	got := sub.await(t, 5*time.Second)
	assert.Equal(t, "ro/ok", got.topic)
}

// ─── Phase 7 — Non-topic exchange types ──────────────────────────────────────

func TestRMQ_Phase7_DirectExchange(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	sink := declareSink(t, admin, exDirect, "exact-key")

	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" {
    exchange                = "`+exDirect+`"
    default_topic_transform = "verbatim"
  }`,
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "exact-key", "hit"))
	d, ok := getWithin(t, admin, sink, 5*time.Second)
	require.True(t, ok, "direct exchange should deliver the exact-key message")
	assert.Equal(t, "hit", string(d.Body))

	require.NoError(t, publishBus(c, "wrong-key", "miss"))
	_, ok2 := getWithin(t, admin, sink, 1500*time.Millisecond)
	assert.False(t, ok2, "direct exchange must not deliver a non-matching key")
}

func TestRMQ_Phase7_FanoutExchange(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	sink1 := declareSink(t, admin, exFanout, "")
	sink2 := declareSink(t, admin, exFanout, "")

	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" { exchange = "`+exFanout+`" }`,
		`subscription "s" {
  target     = bus.main
  topics     = ["#"]
  subscriber = client.events.sender.out
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "any/key", "bcast"))
	d1, ok1 := getWithin(t, admin, sink1, 5*time.Second)
	require.True(t, ok1, "fanout should deliver to sink1")
	assert.Equal(t, "bcast", string(d1.Body))
	d2, ok2 := getWithin(t, admin, sink2, 5*time.Second)
	require.True(t, ok2, "fanout should deliver to sink2")
	assert.Equal(t, "bcast", string(d2.Body))
}

func TestRMQ_Phase7_HeadersExchange(t *testing.T) {
	e := loadEnv(t)
	admin := dialAdmin(t, e)
	q, err := admin.QueueDeclare("", false, false, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, admin.QueueBind(q.Name, "", exHeaders, false,
		amqp.Table{"x-match": "all", "kind": "temp"}))

	vcl := vclConfig(e, e.brokerURL(), `
  sender "out" { exchange = "`+exHeaders+`" }`,
		`subscription "match" {
  target = bus.main
  topics = ["m"]
  action = send(ctx, client.events.sender.out, "ignored", ctx.msg, {kind = "temp"})
}

subscription "nomatch" {
  target = bus.main
  topics = ["n"]
  action = send(ctx, client.events.sender.out, "ignored", ctx.msg, {kind = "other"})
}`)
	c := buildAndStart(t, vcl)

	require.NoError(t, publishBus(c, "m", "matched"))
	d, ok := getWithin(t, admin, q.Name, 5*time.Second)
	require.True(t, ok, "headers exchange should route the matching message")
	assert.Equal(t, "matched", string(d.Body))

	require.NoError(t, publishBus(c, "n", "unmatched"))
	_, ok2 := getWithin(t, admin, q.Name, 1500*time.Millisecond)
	assert.False(t, ok2, "headers exchange must not route a non-matching message")
}
