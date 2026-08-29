package cmd

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// The `subscription` block's `tracing` attribute, checked where an OTLP client
// type actually exists — package config registers none, since clients/otlp is a
// leaf package that imports it.
//
// What these cover is the wiring: that the block's `tracing` expression reaches
// ResolveTracerProvider at all. That the provider then reaches the queue, and
// that the queue emits a span with it, is
// TestSubscriberSource_QueueTracesTheAsyncHop in package config, which asserts
// on a recorded span rather than on the plumbing.

const otlpClient = `
client "otlp" "t" {
  endpoint     = "http://127.0.0.1:4318"
  service_name = "test"
}
`

func TestSubscriptionTracingAcceptsAnOtlpClient(t *testing.T) {
	assert.Empty(t, buildRefCheck(t, `
bus "main" {}
`+otlpClient+`
subscription "s" {
    target     = bus.main
    topics     = ["a"]
    action     = "noop"
    queue_size = 4
    tracing    = client.t
}
`))
}

// The wiring proof. A `tracing` expression that names something which is not an
// OTLP client has to be rejected — if the attribute were decoded and then
// dropped on the floor, this would build clean.
func TestSubscriptionTracingRejectsSomethingThatIsNotAnOtlpClient(t *testing.T) {
	got := buildRefCheck(t, `
bus "main" {}
subscription "s" {
    target     = bus.main
    topics     = ["a"]
    action     = "noop"
    queue_size = 4
    tracing    = bus.main
}
`)
	assert.NotEmpty(t, got, "tracing = bus.main should be rejected")
}

// Auto-wire: a single otlp client and no `tracing` attribute is how most
// configurations will get this, and it must not need saying twice.
func TestSubscriptionTracingAutoWiresToTheOnlyOtlpClient(t *testing.T) {
	assert.Empty(t, buildRefCheck(t, `
bus "main" {}
`+otlpClient+`
subscription "s" {
    target     = bus.main
    topics     = ["a"]
    action     = "noop"
    queue_size = 4
}
`))
}

// tracing without queue_size is the one way to write this attribute and get
// nothing for it, so it says so rather than being quietly inert. A warning, not
// an error — the configuration is valid and works, the attribute just does not
// do what writing it suggests.
func TestSubscriptionTracingWithoutQueueSizeWarns(t *testing.T) {
	cfg, diags := config.NewConfig().WithSources([]byte(`
bus "main" {}
` + otlpClient + `
subscription "s" {
    target  = bus.main
    topics  = ["a"]
    action  = "noop"
    tracing = client.t
}
`)).WithLogger(zap.NewNop()).Build()
	if cfg != nil {
		for _, b := range cfg.Buses {
			b.Stop() //nolint:errcheck
		}
	}

	require.False(t, diags.HasErrors(), "should warn, not fail: %v", diags)
	require.Len(t, diags, 1)
	assert.Equal(t, hcl.DiagWarning, diags[0].Severity)
	assert.Contains(t, diags[0].Summary, "tracing without queue_size")
}

// And the common case: no OTLP client anywhere. ResolveTracerProvider answers
// (nil, nil) there, which must stay silent rather than become a diagnostic on
// every queued subscription in every untraced configuration.
func TestSubscriptionWithoutAnyOtlpClientIsQuiet(t *testing.T) {
	assert.Empty(t, buildRefCheck(t, `
bus "main" {}
subscription "s" {
    target     = bus.main
    topics     = ["a"]
    action     = "noop"
    queue_size = 4
}
`))
}
