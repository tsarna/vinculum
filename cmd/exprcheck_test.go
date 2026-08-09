package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// The cmd package blank-imports every subsystem (cmd/plugins.go), so these
// cover what package config's own tests cannot: the typed block variants whose
// deferred attributes come from the client, server, and trigger registries.

// buildRefCheck builds a config the way `vinculum check` does, returning the
// joined diagnostics text — empty when the config was accepted.
func buildRefCheck(t *testing.T, src string) string {
	t.Helper()
	cfg, diags := config.NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	if cfg != nil {
		for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
			cfg.Stoppables[i].Stop() //nolint:errcheck
		}
		for _, b := range cfg.Buses {
			b.Stop() //nolint:errcheck
		}
	}
	if !diags.HasErrors() {
		return ""
	}
	return diags.Error()
}

// The case that prompted the check. A computed metric's `value` is polled every
// interval, so a name that resolves to nothing used to leave `vinculum check`
// reporting the configuration valid and the poll failing forever.
func TestCheckCatchesComputedMetricValue(t *testing.T) {
	src := `
bus "main" {}
server "metrics" "m" { listen = "127.0.0.1:0" }
metric "gauge" "queue_depth" {
  help              = "Depth of the work queue."
  value             = get(var.depth)
  computed_interval = "15s"
}
`
	err := buildRefCheck(t, src)

	require.NotEmpty(t, err, "a computed metric value naming nothing must fail the load")
	assert.Contains(t, err, `Unknown reference "var"`)
}

func TestCheckCatchesComputedMetricCtxField(t *testing.T) {
	src := `
server "metrics" "m" { listen = "127.0.0.1:0" }
metric "gauge" "queue_depth" {
  help  = "Depth of the work queue."
  value = length(ctx.msg)
}
`
	err := buildRefCheck(t, src)

	require.NotEmpty(t, err)
	assert.Contains(t, err, `Unknown ctx field "msg"`)
	// A poll is not a message: the shape it does get is named in the message.
	assert.Contains(t, err, `"metric-value" context`)
	assert.Contains(t, err, "It provides: metric,")
}

// The deferred attributes of a nested sub-block are reached through the
// schema's own block tree, not only at the top level of a block body.
func TestCheckCatchesNestedSubBlockAction(t *testing.T) {
	src := `
bus "main" {}
server "http" "api" {
  listen = "127.0.0.1:0"
  handle "/hook" {
    action = send(ctx, bus.mian, "in/hook", ctx.request.body)
  }
}
`
	err := buildRefCheck(t, src)

	require.NotEmpty(t, err)
	assert.Contains(t, err, `No bus named "mian"`)
}

// An open context shape is completed per site: the fields a receiver adds to
// `on_decode_error` are its transport's, so mqtt's are legal here and
// rabbitmq's are not.
func TestCheckKnowsSiteAddedCtxFields(t *testing.T) {
	decodeError := func(field string) string {
		return `
bus "main" {}
client "mqtt" "m" {
  brokers = ["mqtt://127.0.0.1:1883"]
  receiver "r" {
    subscription "in/#" {}
    subscriber      = bus.main
    on_decode_error = log::warn("undecodable", { where = ` + field + ` })
  }
}
`
	}

	assert.Empty(t, buildRefCheck(t, decodeError("ctx.mqtt_topic")),
		"the mqtt receiver adds mqtt_topic to the decode-error context")

	err := buildRefCheck(t, decodeError("ctx.routing_key"))
	require.NotEmpty(t, err, "routing_key belongs to the rabbitmq receiver, not this one")
	assert.Contains(t, err, `Unknown ctx field "routing_key"`)
}

// A trigger action gets its own shape, and it is not the message one.
func TestCheckCatchesTriggerCtxField(t *testing.T) {
	src := `
bus "main" {}
trigger "interval" "tick" {
  delay  = "1m"
  action = send(ctx, bus.main, "tick", ctx.topic)
}
`
	err := buildRefCheck(t, src)

	require.NotEmpty(t, err)
	assert.Contains(t, err, `Unknown ctx field "topic"`)
	assert.Contains(t, err, `"trigger-interval" context`)
}
