package sqs

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
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
		[]byte(`"in/${ctx.queue}/${ctx.message_id}/${ctx.fields.type}/${ctx.msg}"`),
		"<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	topic := makeVinculumTopicFunc(config, expr, "tasks")(
		sqstypes.Message{MessageId: aws.String("m1"), Body: aws.String("hello")},
		map[string]string{"type": "job"})
	require.Equal(t, "in/tasks/m1/job/hello", topic)
}

// A message with no body leaves ctx.msg null rather than absent, so an
// expression can test for it instead of failing to evaluate.
func TestInboundContextMsgIsNullWithoutABody(t *testing.T) {
	config := inboundTestConfig(t)

	expr, pdiags := hclsyntax.ParseExpression(
		[]byte(`ctx.msg == null ? "empty" : "has-body"`), "<test>", hcl.InitialPos)
	require.False(t, pdiags.HasErrors(), pdiags.Error())

	topic := makeVinculumTopicFunc(config, expr, "tasks")(
		sqstypes.Message{MessageId: aws.String("m1")}, nil)
	require.Equal(t, "empty", topic)
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
