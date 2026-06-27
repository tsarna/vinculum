package sqs_test

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

client "sqs_receiver" "r" {
  region     = "us-east-1"
  queue_url  = "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue"
  subscriber = bus.main

  baggage {
    allow = ["tenant_id"]
  }
}
`
	c, hasErr, msg := build(t, src)
	require.False(t, hasErr, msg)
	require.Contains(t, c.Clients, "sqs_receiver")
	require.Contains(t, c.Clients["sqs_receiver"], "r")
}

func TestReceiverBaggageAllowDenyConflict(t *testing.T) {
	const src = `
bus "main" {}

client "sqs_receiver" "r" {
  region     = "us-east-1"
  queue_url  = "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue"
  subscriber = bus.main

  baggage {
    allow = ["a"]
    deny  = ["b"]
  }
}
`
	_, hasErr, msg := build(t, src)
	require.True(t, hasErr, "allow + deny on a baggage block should fail to parse")
	assert.Contains(t, msg, "either allow or deny")
}
