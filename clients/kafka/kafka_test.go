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

// receiverWithCommitMode is a minimal receiver parameterized on commit_mode.
func receiverWithCommitMode(mode string) string {
	return `
bus "main" {}

client "kafka" "k" {
  brokers = ["localhost:9092"]

  receiver "r" {
    group_id    = "g"
    subscriber  = bus.main
    commit_mode = "` + mode + `"

    subscription "kafka.topic" {
      vinculum_topic = "in/topic"
    }
  }
}
`
}

func TestReceiverCommitModeManualIsRejected(t *testing.T) {
	// "manual" promised caller-controlled commits that nothing can perform.
	// Accepted, it fell through to the Kafka client's own periodic autocommit,
	// so it was an alias for the weakest mode in the enum while documented as
	// the strongest. See specs/KAFKA-UNFINISHED.md §1.
	_, hasErr, msg := build(t, receiverWithCommitMode("manual"))
	require.True(t, hasErr, `commit_mode = "manual" should be rejected at load`)
	assert.Contains(t, msg, "not implemented")
	assert.Contains(t, msg, "after_process")
	assert.Contains(t, msg, "periodic")
}

func TestReceiverCommitModeInvalidNamesTheRemainingModes(t *testing.T) {
	_, hasErr, msg := build(t, receiverWithCommitMode("whenever"))
	require.True(t, hasErr, "an unknown commit_mode should be rejected")
	assert.Contains(t, msg, "use after_process or periodic")
}

func TestReceiverCommitModesThatRemainStillParse(t *testing.T) {
	for _, mode := range []string{"after_process", "periodic"} {
		t.Run(mode, func(t *testing.T) {
			_, hasErr, msg := build(t, receiverWithCommitMode(mode))
			require.False(t, hasErr, msg)
		})
	}
}
