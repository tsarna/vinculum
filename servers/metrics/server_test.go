package metricsserver_test

import (
	_ "embed"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	httpserver "github.com/tsarna/vinculum/servers/http"
	metricsserver "github.com/tsarna/vinculum/servers/metrics"
	"go.uber.org/zap"
)

//go:embed testdata/standalone.vcl
var standaloneVCL []byte

//go:embed testdata/mounted.vcl
var mountedVCL []byte

func TestStandaloneMetricsServerConfig(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(standaloneVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Server registered under the "metrics" type
	assert.Contains(t, c.Servers, "metrics")
	assert.Contains(t, c.Servers["metrics"], "main")

	// Also accessible via MetricsServers and CtyServerMap
	assert.Contains(t, c.MetricsServers, "main")
	assert.Contains(t, c.CtyServerMap, "main")

	// Standalone server (has listen) is in Startables
	found := false
	for _, s := range c.Startables {
		if _, ok := s.(*metricsserver.MetricsServer); ok {
			found = true
			break
		}
	}
	assert.True(t, found, "standalone MetricsServer should be in Startables")
}

func TestStandaloneMetricsServerScrape(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(standaloneVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	ms, ok := c.MetricsServers["main"].(*metricsserver.MetricsServer)
	require.True(t, ok, "MetricsServers[\"main\"] should be *MetricsServer")

	// Scrape the handler directly — no network required
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	ms.GetHandler().ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	text := string(body)

	// OTel runtime instrumentation produces go.goroutine.count → go_goroutine_count in Prometheus format
	assert.True(t, strings.Contains(text, "go_goroutine_count"), "go runtime metrics should be present")
}

func TestMountedMetricsServerConfig(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(mountedVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Mounted server (no listen) is NOT in Startables
	for _, s := range c.Startables {
		_, ok := s.(*metricsserver.MetricsServer)
		assert.False(t, ok, "mounted MetricsServer should not be in Startables")
	}
}

func TestMountedMetricsServerScrape(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(mountedVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	// Scrape through the HTTP server's mux
	httpSrv := c.Servers["http"]["main"].(*httpserver.HttpServer)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	httpSrv.Server.Handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	// OTel runtime instrumentation produces go.goroutine.count → go_goroutine_count in Prometheus format
	assert.True(t, strings.Contains(string(body), "go_goroutine_count"), "go runtime metrics should appear via mounted handler")
}
