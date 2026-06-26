package rabbitmq_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

func buildSrc(t *testing.T, src string) (*cfg.Config, bool, string) {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	return c, diags.HasErrors(), diags.Error()
}

func TestReceiverBaggageBlockParses(t *testing.T) {
	const src = `
bus "main" {}

client "rabbitmq" "events" {
  brokers = ["amqp://localhost:5672/"]

  receiver "in" {
    queue      = "q"
    subscriber = bus.main

    baggage {
      allow = ["tenant_id"]
    }
  }
}
`
	c, hasErr, msg := buildSrc(t, src)
	require.False(t, hasErr, msg)
	require.Contains(t, c.Clients, "rabbitmq")
	require.Contains(t, c.Clients["rabbitmq"], "events")
}

func TestReceiverBaggageAllowDenyConflict(t *testing.T) {
	const src = `
bus "main" {}

client "rabbitmq" "events" {
  brokers = ["amqp://localhost:5672/"]

  receiver "in" {
    queue      = "q"
    subscriber = bus.main

    baggage {
      allow = ["a"]
      deny  = ["b"]
    }
  }
}
`
	_, hasErr, msg := buildSrc(t, src)
	require.True(t, hasErr, "allow + deny on a baggage block should fail to parse")
	assert.Contains(t, msg, "either allow or deny")
}
