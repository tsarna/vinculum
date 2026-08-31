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

// receiverWithBody is a minimal receiver parameterized on its own attributes.
func receiverWithBody(body string) string {
	return `
bus "main" {}

client "kafka" "k" {
  brokers = ["localhost:9092"]

  receiver "r" {
    group_id    = "g"
    subscriber  = bus.main
    ` + body + `

    subscription "kafka.topic" {
      vinculum_topic = "in/topic"
    }
  }
}
`
}

// receiverWithAck is a minimal receiver parameterized on ack.
func receiverWithAck(mode string) string {
	return receiverWithBody(`ack = "` + mode + `"`)
}

func TestReceiverAckManualIsRejected(t *testing.T) {
	// Acknowledging one record is not committing an offset: completing record 7
	// while 5 is outstanding needs a low-water-mark tracker this receiver does
	// not have. Its predecessor, commit_mode = "manual", promised
	// caller-controlled commits that nothing could perform and fell through to
	// the Kafka client's own periodic autocommit — an alias for the weakest
	// mode in the enum while documented as the strongest. See
	// specs/KAFKA-UNFINISHED.md §1 and specs/CONTEXT-ACK.md §6 phase 3.
	_, hasErr, msg := build(t, receiverWithAck("manual"))
	require.True(t, hasErr, `ack = "manual" should be rejected at load`)
	assert.Contains(t, msg, "not implemented yet")
	assert.Contains(t, msg, "low-water-mark")
}

func TestReceiverAckInvalidNamesTheModesThisReceiverHas(t *testing.T) {
	_, hasErr, msg := build(t, receiverWithAck("whenever"))
	require.True(t, hasErr, "an unknown ack should be rejected")
	assert.Contains(t, msg, `"auto"`)
	assert.Contains(t, msg, `"periodic"`)
}

func TestReceiverAckModesThatWorkStillParse(t *testing.T) {
	for _, mode := range []string{"auto", "periodic"} {
		t.Run(mode, func(t *testing.T) {
			_, hasErr, msg := build(t, receiverWithAck(mode))
			require.False(t, hasErr, msg)
		})
	}
}

// A queue makes delivery succeed at the moment the record is queued, so `auto`
// would commit the offset before the handler ran — and an error would no longer
// reach dlq_topic. Refused, written or defaulted.
func TestQueueSizeIsRefusedWithAutoCommit(t *testing.T) {
	for _, body := range []string{
		"queue_size = 16",
		"ack = \"auto\"\n    queue_size = 16",
	} {
		t.Run(body, func(t *testing.T) {
			_, hasErr, msg := build(t, receiverWithBody(body))
			require.True(t, hasErr, "queue_size with an auto commit should be rejected")
			assert.Contains(t, msg, "queue_size cannot be combined")
			// Kafka has neither knob yet, so the diagnostic must not name one.
			assert.Contains(t, msg, "remove queue_size")
			assert.NotContains(t, msg, "concurrency")
		})
	}
}

// `periodic` commits on a timer regardless of the outcome, so the queue takes
// nothing away that the mode had not already given up. Left alone deliberately.
func TestQueueSizeIsAllowedWithPeriodicCommit(t *testing.T) {
	_, hasErr, msg := build(t, receiverWithBody("ack = \"periodic\"\n    queue_size = 16"))
	require.False(t, hasErr, msg)
}

// The old spelling is met with what it became, rather than with gohcl's
// "argument named commit_mode is not expected here" — which is true, useless,
// and gives an upgrading configuration nothing to search for.
func TestRetiredCommitModeSaysWhatItBecame(t *testing.T) {
	src := `
bus "main" {}

client "kafka" "k" {
  brokers = ["localhost:9092"]

  receiver "r" {
    group_id    = "g"
    subscriber  = bus.main
    commit_mode = "after_process"

    subscription "kafka.topic" {
      vinculum_topic = "in/topic"
    }
  }
}
`
	_, hasErr, msg := build(t, src)
	require.True(t, hasErr, "commit_mode should no longer be accepted")
	assert.Contains(t, msg, `"commit_mode" is now "ack"`)
	assert.Contains(t, msg, "0.46.0")
	assert.Contains(t, msg, `ack = "auto"`)
}
