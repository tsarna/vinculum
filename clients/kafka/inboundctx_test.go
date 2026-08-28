package kafka

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
	config := inboundTestConfig(t)

	expr, pdiags := hclsyntax.ParseExpression(
		[]byte(`"in/${ctx.kafka_topic}/${ctx.key}/${ctx.fields.deviceId}/${ctx.msg}"`),
		"<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	key := "k1"
	topic, err := makeVinculumTopicFunc(config, expr)(
		"sensor.readings", &key, map[string]string{"deviceId": "dev1"}, "hello")
	require.NoError(t, err)
	require.Equal(t, "in/sensor.readings/k1/dev1/hello", topic)
}

// A record produced without a key yields a null, which is the one field the
// schema marks optional.
func TestInboundContextKeyIsNullWithoutOne(t *testing.T) {
	config := inboundTestConfig(t)

	expr, pdiags := hclsyntax.ParseExpression(
		[]byte(`ctx.key == null ? "unkeyed" : "keyed"`), "<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	topic, err := makeVinculumTopicFunc(config, expr)("sensor.readings", nil, nil, "hello")
	require.NoError(t, err)
	require.Equal(t, "unkeyed", topic)
}

func inboundTestConfig(t *testing.T) *cfg.Config {
	t.Helper()
	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())
	return config
}
