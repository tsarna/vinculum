package mqtt

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// The schema says a receiver's vinculum_topic can read ctx.mqtt_topic; the
// runtime is what decides whether it can. Nothing checks one against the other
// — AttrMeta.ContextFields is curated prose and the eval context is built
// imperatively — so a misspelling on either side would only surface as a failed
// evaluation on the first message to arrive.
//
// This evaluates the real expression through the real builder, which is the
// only place the two meet.
func TestInboundContextFieldsAreWhatTheSchemaPromises(t *testing.T) {
	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	expr, pdiags := hclsyntax.ParseExpression(
		[]byte(`"in/${ctx.mqtt_topic}/${ctx.fields.deviceId}/${ctx.msg}"`),
		"<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	topic, err := makeMQTTVinculumTopicFunc(config, expr)(
		"sensors/dev1/reading", map[string]string{"deviceId": "dev1"}, "hello")
	require.NoError(t, err)
	require.Equal(t, "in/sensors/dev1/reading/dev1/hello", topic)
}

// And ctx.topic is gone, rather than lingering as an alias.
func TestInboundContextHasNoTopic(t *testing.T) {
	config, diags := cfg.NewConfig().
		WithSources([]byte(`bus "main" {}`)).
		WithLogger(zap.NewNop()).
		Build()
	require.False(t, diags.HasErrors(), diags.Error())

	expr, pdiags := hclsyntax.ParseExpression([]byte(`ctx.topic`), "<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	_, err := makeMQTTVinculumTopicFunc(config, expr)("sensors/dev1/reading", nil, "hello")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not have an attribute named \"topic\"")
}
