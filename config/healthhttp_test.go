package config

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// probeConfig returns a Config past its boot gate with the given contributors.
func probeConfig(t *testing.T, contributors ...func(*Health)) *Config {
	t.Helper()
	c := &Config{Health: NewHealth(zap.NewNop())}
	for _, add := range contributors {
		add(c.Health)
	}
	c.Health.SetBooted()
	return c
}

func ready(kind, typ, name string) func(*Health) {
	return func(h *Health) { h.RegisterReady(kind, typ, name, &fakeReadyable{}) }
}

func broken(kind, typ, name, reason string) func(*Health) {
	return func(h *Health) {
		h.RegisterReady(kind, typ, name, &fakeReadyable{err: errors.New(reason)})
	}
}

func probe(t *testing.T, c *Config, path string, allowVerbose bool, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	c.HealthHandler(path, allowVerbose).ServeHTTP(rec, req)
	return rec.Result()
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestProbeStatusCodes(t *testing.T) {
	healthy := probeConfig(t, ready("server", "http", "api"))
	resp := probe(t, healthy, ReadyzPath, false, "/readyz")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok\n", bodyOf(t, resp))

	// 503, not a 4xx: nothing is wrong with the request, the server is
	// temporarily unable to handle it.
	sick := probeConfig(t, broken("client", "mqtt", "broker", "not connected"))
	resp = probe(t, sick, ReadyzPath, false, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "not ready\n", bodyOf(t, resp))
}

func TestProbeSetsNoStore(t *testing.T) {
	c := probeConfig(t)
	resp := probe(t, c, ReadyzPath, false, "/readyz")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
}

func TestProbeHeadHasStatusButNoBody(t *testing.T) {
	c := probeConfig(t, broken("client", "mqtt", "broker", "not connected"))

	rec := httptest.NewRecorder()
	c.HealthHandler(ReadyzPath, false).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/readyz", nil))
	resp := rec.Result()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Empty(t, bodyOf(t, resp))
}

func TestProbeVerboseBody(t *testing.T) {
	c := probeConfig(t,
		ready("server", "http", "api"),
		broken("client", "mqtt", "broker", "not connected: dial tcp 10.0.0.5:1883: connection refused"),
	)

	resp := probe(t, c, ReadyzPath, true, "/readyz?verbose")
	body := bodyOf(t, resp)

	// Mirrors kube-apiserver's /readyz?verbose so operator habits transfer,
	// plus how long each component has held its verdict. Nothing here has ever
	// changed state, so every age is the moment it was first observed.
	assert.Equal(t, strings.Join([]string{
		"[+]process ok (for 0s)",
		"[+]server.api ok (for 0s)",
		"[-]client.broker failed: not connected: dial tcp 10.0.0.5:1883: connection refused (for 0s)",
		"readyz check failed",
		"",
	}, "\n"), body)

	// Registration order is boot order, so infrastructure reads before the
	// things that depend on it.
	assert.Less(t, strings.Index(body, "server.api"), strings.Index(body, "client.broker"))
}

func TestProbeVerboseIsRefusedWhereTheEndpointSaysSo(t *testing.T) {
	c := probeConfig(t, broken("client", "mqtt", "broker", "not connected to broker.internal"))

	// The endpoint's setting, not the caller's: an unauthenticated endpoint
	// must not name components and quote connection errors on request.
	terse := bodyOf(t, probe(t, c, ReadyzPath, false, "/readyz?verbose"))
	assert.Equal(t, "not ready\n", terse)
	assert.NotContains(t, terse, "broker.internal")
}

func TestProbeJSON(t *testing.T) {
	c := probeConfig(t, broken("client", "mqtt", "broker", "not connected"))

	resp := probe(t, c, ReadyzPath, true, "/readyz?verbose&format=json")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var doc struct {
		Ready  bool `json:"ready"`
		Checks []struct {
			Component string    `json:"component"`
			Type      string    `json:"type"`
			Ready     bool      `json:"ready"`
			Reason    string    `json:"reason"`
			Since     time.Time `json:"since"`
		} `json:"checks"`
	}
	body := bodyOf(t, resp)
	require.NoError(t, json.Unmarshal([]byte(body), &doc))

	assert.False(t, doc.Ready)
	require.Len(t, doc.Checks, 2)
	assert.Equal(t, "process", doc.Checks[0].Component)
	assert.Equal(t, "client.broker", doc.Checks[1].Component)
	assert.Equal(t, "mqtt", doc.Checks[1].Type)
	assert.Equal(t, "not connected", doc.Checks[1].Reason)

	// Every entry is dated, and RFC 3339 is what a cty time capsule encodes to,
	// so `since` reads the same here as through jsonencode(health::status(ctx)).
	assert.WithinDuration(t, time.Now(), doc.Checks[1].Since, time.Minute)
	assert.Contains(t, body, `"since":"`+doc.Checks[1].Since.Format(time.RFC3339Nano)+`"`)
}

func TestProbeJSONWithoutDetailCarriesOnlyTheVerdict(t *testing.T) {
	c := probeConfig(t, broken("client", "mqtt", "broker", "not connected to broker.internal"))

	// Negotiating your way past a terse endpoint would defeat it.
	body := bodyOf(t, probe(t, c, ReadyzPath, false, "/readyz?format=json"))
	assert.JSONEq(t, `{"ready": false}`, body)
	assert.NotContains(t, body, "broker.internal")
}

func TestProbeAcceptHeaderSelectsJSON(t *testing.T) {
	c := probeConfig(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	c.HealthHandler(ReadyzPath, false).ServeHTTP(rec, req)

	assert.Equal(t, "application/json", rec.Result().Header.Get("Content-Type"))
}

func TestLivezIgnoresReadinessFailures(t *testing.T) {
	c := probeConfig(t, broken("client", "mqtt", "broker", "not connected"))

	// A dependency outage restarting every replica is a well-known cascading
	// failure, and nothing but the process and an explicit live check feeds
	// liveness.
	assert.Equal(t, http.StatusServiceUnavailable, probe(t, c, ReadyzPath, false, "/readyz").StatusCode)
	assert.Equal(t, http.StatusOK, probe(t, c, LivezPath, false, "/livez").StatusCode)
	assert.Equal(t, http.StatusOK, probe(t, c, HealthzPath, false, "/healthz").StatusCode)
}

func TestLivezVerbosePrintsItsOwnName(t *testing.T) {
	c := probeConfig(t)
	assert.Contains(t, bodyOf(t, probe(t, c, LivezPath, true, "/livez?verbose")), "livez check passed")
	assert.Contains(t, bodyOf(t, probe(t, c, HealthzPath, true, "/healthz?verbose")), "healthz check passed")

	// /healthz is an alias for /livez, so its verdict key is liveness's.
	body := bodyOf(t, probe(t, c, HealthzPath, false, "/healthz?format=json"))
	assert.JSONEq(t, `{"live": true}`, body)
}

func TestProbeDuringBootReportsStarting(t *testing.T) {
	c := &Config{Health: NewHealth(zap.NewNop())}
	c.Health.RegisterReady("client", "mqtt", "broker", &fakeReadyable{})

	// A startupProbe pointed at /readyz works because of this, without a
	// separate endpoint.
	resp := probe(t, c, ReadyzPath, true, "/readyz?verbose")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "[-]process failed: starting (for 0s)\nreadyz check failed\n", bodyOf(t, resp))
}

func TestProbeDuringDrainReportsShuttingDown(t *testing.T) {
	c := probeConfig(t, ready("server", "http", "api"))
	require.Equal(t, http.StatusOK, probe(t, c, ReadyzPath, false, "/readyz").StatusCode)

	c.Health.BeginDrain()

	// Readiness goes false immediately, bypassing the cache, so a load
	// balancer stops sending work while in-flight requests finish.
	resp := probe(t, c, ReadyzPath, true, "/readyz?verbose")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, bodyOf(t, resp), "[-]process failed: shutting down")

	// Draining is not wedged: killing the process mid-drain is the opposite of
	// what shutdown is trying to achieve.
	assert.Equal(t, http.StatusOK, probe(t, c, LivezPath, false, "/livez").StatusCode)
}

func TestQueryFlagReadsValuelessAndExplicitForms(t *testing.T) {
	on := []string{"/x?verbose", "/x?verbose=", "/x?verbose=true", "/x?verbose=1", "/x?verbose=on"}
	for _, target := range on {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		assert.True(t, NegotiateHealthRender(req, true).Verbose, target)
	}
	off := []string{"/x", "/x?verbose=false", "/x?verbose=0", "/x?verbose=no"}
	for _, target := range off {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		assert.False(t, NegotiateHealthRender(req, true).Verbose, target)
	}
}

func TestHealthMuxServesAllThree(t *testing.T) {
	c := probeConfig(t)
	mux := c.HealthMux(false)

	for _, path := range HealthEndpointPaths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, rec.Code, path)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "the listener owns nothing but the three probes")
}
