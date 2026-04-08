package metricsserver_test

import (
	"context"
	_ "embed"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/types"
	httpserver "github.com/tsarna/vinculum/servers/http"
	metricsserver "github.com/tsarna/vinculum/servers/metrics"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/standalone.vcl
var standaloneVCL []byte

//go:embed testdata/mounted.vcl
var mountedVCL []byte

//go:embed testdata/dotnames.vcl
var dotnamesVCL []byte

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

// --- Integration: metric block → OTel instrument → Prometheus scrape ---

func scrape(t *testing.T, ms *metricsserver.MetricsServer) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	ms.GetHandler().ServeHTTP(w, req)
	body, err := io.ReadAll(w.Result().Body)
	require.NoError(t, err)
	return string(body)
}

func TestMetricBlockGauge_SetAndScrape(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(standaloneVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	ms := c.MetricsServers["main"].(*metricsserver.MetricsServer)

	// Get the gauge metric capsule from the config namespace
	metricObj := c.Constants["metric"]
	require.True(t, metricObj.Type().HasAttribute("temperature"))
	capsule := metricObj.GetAttr("temperature")
	mv, err := types.GetMetricFromCapsule(capsule)
	require.NoError(t, err)

	// Set a value via the Settable interface
	settable := mv.(interface {
		Set(ctx context.Context, args []cty.Value) (cty.Value, error)
	})
	_, err = settable.Set(context.Background(), []cty.Value{cty.NumberFloatVal(42.5)})
	require.NoError(t, err)

	// Scrape and verify the value appears in Prometheus format
	text := scrape(t, ms)
	assert.Contains(t, text, "temperature")
	assert.Contains(t, text, "42.5")
}

func TestMetricBlockCounter_IncrementAndScrape(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(standaloneVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	ms := c.MetricsServers["main"].(*metricsserver.MetricsServer)

	metricObj := c.Constants["metric"]
	capsule := metricObj.GetAttr("requests")
	mv, err := types.GetMetricFromCapsule(capsule)
	require.NoError(t, err)

	incrementable := mv.(interface {
		Increment(ctx context.Context, args []cty.Value) (cty.Value, error)
	})
	_, err = incrementable.Increment(context.Background(), []cty.Value{cty.NumberIntVal(7)})
	require.NoError(t, err)

	text := scrape(t, ms)
	// OTel→Prometheus bridge adds _total suffix to counters
	assert.Contains(t, text, "requests_total")
	assert.Contains(t, text, "7")
}

func TestMetricBlockHistogram_ObserveAndScrape(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(standaloneVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	ms := c.MetricsServers["main"].(*metricsserver.MetricsServer)

	metricObj := c.Constants["metric"]
	capsule := metricObj.GetAttr("latency")
	mv, err := types.GetMetricFromCapsule(capsule)
	require.NoError(t, err)

	observable := mv.(interface {
		Observe(ctx context.Context, args []cty.Value) (cty.Value, error)
	})
	_, err = observable.Observe(context.Background(), []cty.Value{cty.NumberFloatVal(0.042)})
	require.NoError(t, err)

	text := scrape(t, ms)
	assert.Contains(t, text, "latency")
	// Histogram should have bucket boundaries and count
	assert.Contains(t, text, "latency_bucket")
	assert.Contains(t, text, "latency_count")
}

func TestMetricBlockMeterProvider(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(standaloneVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	ms := c.MetricsServers["main"].(*metricsserver.MetricsServer)
	assert.NotNil(t, ms.GetMeterProvider(), "MetricsServer should expose a MeterProvider")

	// Auto-wire: single metrics server is selected as default by GetDefaultInstrumentMetrics
	im, imDiags := c.GetDefaultInstrumentMetrics()
	require.False(t, imDiags.HasErrors())
	assert.NotNil(t, im, "single metrics server should be auto-wired as default")
	assert.Equal(t, ms.GetMeterProvider(), im.GetMeterProvider())
}

// --- Dot-to-underscore VCL naming ---

func TestDotNameMetrics_VCLKeyTranslation(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(dotnamesVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	metricObj := c.Constants["metric"]

	// "http.server.active_requests" → VCL key "http_server_active_requests"
	assert.True(t, metricObj.Type().HasAttribute("http_server_active_requests"),
		"dotted metric name should be accessible as underscore VCL key")

	// "app.requests_total" with namespace "my.app" → VCL key "app_requests_total"
	// (namespace only affects the OTel instrument name, not the VCL key)
	assert.True(t, metricObj.Type().HasAttribute("app_requests_total"),
		"dotted metric name should be accessible as underscore VCL key")
}

func TestDotNameMetrics_PrometheusExposition(t *testing.T) {
	logger := zap.NewNop()
	c, diags := cfg.NewConfig().WithSources(dotnamesVCL).WithLogger(logger).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	ms := c.MetricsServers["main"].(*metricsserver.MetricsServer)

	// Set a value on the dotted-name gauge
	capsule := c.Constants["metric"].GetAttr("http_server_active_requests")
	mv, err := types.GetMetricFromCapsule(capsule)
	require.NoError(t, err)

	settable := mv.(interface {
		Set(ctx context.Context, args []cty.Value) (cty.Value, error)
	})
	_, err = settable.Set(context.Background(), []cty.Value{cty.NumberIntVal(5)})
	require.NoError(t, err)

	text := scrape(t, ms)
	// OTel→Prometheus bridge converts dots to underscores in metric names
	assert.Contains(t, text, "http_server_active_requests")
}

func TestDotNameCollision_Detected(t *testing.T) {
	// Two metrics that map to the same VCL key should produce an error
	vcl := []byte(`
server "metrics" "main" {
    listen = "127.0.0.1:19092"
}

metric "gauge" "foo.bar" {
    help = "first"
}

metric "gauge" "foo_bar" {
    help = "second"
}
`)
	logger := zap.NewNop()
	_, diags := cfg.NewConfig().WithSources(vcl).WithLogger(logger).Build()
	assert.True(t, diags.HasErrors(), "should detect VCL name collision")

	found := false
	for _, d := range diags {
		if strings.Contains(d.Detail, "VCL key") {
			found = true
			break
		}
	}
	assert.True(t, found, "error should mention VCL key collision")
}
