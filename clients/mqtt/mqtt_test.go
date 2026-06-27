package mqtt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

func build(t *testing.T, src string) (*cfg.Config, bool, string) {
	t.Helper()
	c, diags := cfg.NewConfig().WithSources([]byte(src)).WithLogger(zap.NewNop()).Build()
	return c, diags.HasErrors(), diags.Error()
}

func TestReceiverBaggageBlockParses(t *testing.T) {
	const src = `
bus "main" {}

client "mqtt" "m" {
  brokers = ["mqtt://localhost:1883"]

  receiver "r" {
    subscriber = bus.main

    baggage {
      allow = ["tenant_id"]
    }

    subscription "sensors/#" {
      vinculum_topic = "in"
    }
  }
}
`
	c, hasErr, msg := build(t, src)
	require.False(t, hasErr, msg)
	require.Contains(t, c.Clients, "mqtt")
	require.Contains(t, c.Clients["mqtt"], "m")
}

func TestReceiverBaggageAllowDenyConflict(t *testing.T) {
	const src = `
bus "main" {}

client "mqtt" "m" {
  brokers = ["mqtt://localhost:1883"]

  receiver "r" {
    subscriber = bus.main

    baggage {
      allow = ["a"]
      deny  = ["b"]
    }

    subscription "sensors/#" {
      vinculum_topic = "in"
    }
  }
}
`
	_, hasErr, msg := build(t, src)
	require.True(t, hasErr, "allow + deny on a baggage block should fail to parse")
	assert.Contains(t, msg, "either allow or deny")
}
