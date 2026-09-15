package otlp_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

// A config that is built and never started — `vinculum check`, man::check —
// must not export to the endpoint it names: not on the metric interval, not
// when a span is flushed, and not on the final flush at teardown.
func TestOtlpClientExportsNothingBeforeStart(t *testing.T) {
	rec := &pathRecorder{}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)

	src := fmt.Sprintf(`
client "otlp" "default" {
    service_name       = "test-service"
    endpoint           = %q
    metric_interval    = "10ms"
    include_go_metrics = false
}
`, srv.URL)

	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), diags.Error())

	tp := c.OtlpClients["default"].GetTracerProvider().(*sdktrace.TracerProvider)
	_, span := tp.Tracer("test").Start(context.Background(), "test-span")
	span.End()
	require.NoError(t, tp.ForceFlush(context.Background()))

	time.Sleep(200 * time.Millisecond) // twenty metric intervals
	c.Discard()

	assert.Empty(t, rec.seen())
}
