package kafka_test

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

const baggageReceiver = `
bus "main" {}

client "kafka" "k" {
  brokers = ["localhost:9092"]

  receiver "r" {
    group_id   = "g"
    subscriber = bus.main

    baggage {
      allow = ["tenant_id"]
    }

    subscription "kafka.topic" {
      vinculum_topic = "in/topic"
    }
  }
}
`

func TestReceiverBaggageBlockParses(t *testing.T) {
	c, hasErr, msg := build(t, baggageReceiver)
	require.False(t, hasErr, msg)
	require.Contains(t, c.Clients, "kafka")
	require.Contains(t, c.Clients["kafka"], "k")
}

func TestReceiverBaggageAllowDenyConflict(t *testing.T) {
	const src = `
client "kafka" "k" {
  brokers = ["localhost:9092"]

  receiver "r" {
    group_id = "g"
    action   = "noop"

    baggage {
      allow = ["a"]
      deny  = ["b"]
    }

    subscription "kafka.topic" {
      vinculum_topic = "in/topic"
    }
  }
}
`
	_, hasErr, msg := build(t, src)
	require.True(t, hasErr, "allow + deny on a baggage block should fail to parse")
	assert.Contains(t, msg, "either allow or deny")
}
