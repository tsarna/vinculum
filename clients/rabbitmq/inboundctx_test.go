package rabbitmq

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// See clients/mqtt/inboundctx_test.go for why this is worth testing: the
// schema's promise about ctx and the eval context that has to keep it are
// written in different places and nothing compares them.
func TestInboundContextFieldsAreWhatTheSchemaPromises(t *testing.T) {
	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	expr, pdiags := hclsyntax.ParseExpression(
		[]byte(`"in/${ctx.exchange}/${ctx.routing_key}/${ctx.fields.deviceId}/${ctx.msg}"`),
		"<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	topic, err := makeVinculumTopicFunc(config, expr)(
		"sensor.dev1.reading", "events", map[string]string{"deviceId": "dev1"}, "hello")
	require.NoError(t, err)
	require.Equal(t, "in/events/sensor.dev1.reading/dev1/hello", topic)
}

// `topic` used to alias `routing_key` here. It does not any more, and the
// fixtures that reached for the alias were already using the real name.
func TestInboundContextHasNoTopicAlias(t *testing.T) {
	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	expr, pdiags := hclsyntax.ParseExpression([]byte(`ctx.topic`), "<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	_, err := makeVinculumTopicFunc(config, expr)("sensor.dev1.reading", "events", nil, "hello")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not have an attribute named \"topic\"")
}
