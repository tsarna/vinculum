package otlp_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

// pathRecorder is a collector stand-in that records the path of every export
// it is POSTed and answers 200, the way a real collector does on success.
type pathRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *pathRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (r *pathRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// exportOnce builds a client "otlp" against the given endpoints, emits one span
// and one metric, flushes both, and returns the paths the server was POSTed.
//
// It drives the real exporters rather than the path helper alone: the breakage
// this guards against was a change in what the exporters do with a path-less
// URL, which a test of our own string handling would not have noticed.
func exportOnce(t *testing.T, endpointAttrs string) []string {
	t.Helper()

	rec := &pathRecorder{}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)

	src := fmt.Sprintf(`
client "otlp" "default" {
    service_name = "test-service"
    %s
}
`, fmt.Sprintf(endpointAttrs, srv.URL))

	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	oc := c.OtlpClients["default"]
	require.NotNil(t, oc)

	ctx := context.Background()

	tp, ok := oc.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, ok, "expected the SDK tracer provider")
	_, span := tp.Tracer("test").Start(ctx, "test-span")
	span.End()
	require.NoError(t, tp.ForceFlush(ctx))

	mp, ok := oc.GetMeterProvider().(*sdkmetric.MeterProvider)
	require.True(t, ok, "expected the SDK meter provider")
	counter, err := mp.Meter("test").Int64Counter("test.counter")
	require.NoError(t, err)
	counter.Add(ctx, 1)
	require.NoError(t, mp.ForceFlush(ctx))

	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})

	return rec.seen()
}

// A path-less endpoint must reach each signal's default OTLP path. OTel Go
// v1.45.0 changed WithEndpointURL to pin such a URL to "/" instead of letting
// the default signal path be appended, which made every export 404 against a
// stock collector configured as `endpoint = "http://collector:4318"`.
//
// Traces and metrics are asserted together but broke from separate dependency
// bumps, so they could regress separately too.
func TestPathlessEndpointGetsSignalPath(t *testing.T) {
	paths := exportOnce(t, `endpoint = "%s"`)

	assert.Contains(t, paths, "/v1/traces")
	assert.Contains(t, paths, "/v1/metrics")
	assert.NotContains(t, paths, "/", "a path-less endpoint must not export to the root")
}

// An endpoint given with an explicit path is the user's to choose: a collector
// behind a prefix, or a vendor endpoint that is not spec-shaped. Use it verbatim.
func TestExplicitEndpointPathIsUsedVerbatim(t *testing.T) {
	paths := exportOnce(t, `
    endpoint        = "%[1]s/custom/traces"
    metric_endpoint = "%[1]s/custom/metrics"`)

	assert.Contains(t, paths, "/custom/traces")
	assert.Contains(t, paths, "/custom/metrics")
}

// metric_endpoint defaults to endpoint, so one path-less value has to yield the
// right path for each signal — which is why the default cannot simply be
// spelled out in the config.
func TestSharedEndpointSplitsPerSignal(t *testing.T) {
	paths := exportOnce(t, `endpoint = "%s"`)

	assert.ElementsMatch(t, []string{"/v1/traces", "/v1/metrics"}, paths)
}
