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

func TestReceiverAckManualRequiresSettleTimeout(t *testing.T) {
	// The only thing standing between a config and manual settle now. Nothing
	// settles a record until the configuration does, so a bound is not optional
	// — an unsettled record holds its partition's committable offset.
	_, hasErr, msg := build(t, receiverWithAck("manual"))
	require.True(t, hasErr, `ack = "manual" without settle_timeout should be rejected`)
	assert.Contains(t, msg, "requires settle_timeout")
}

func TestReceiverAckInvalidNamesTheModesThisReceiverHas(t *testing.T) {
	_, hasErr, msg := build(t, receiverWithAck("whenever"))
	require.True(t, hasErr, "an unknown ack should be rejected")
	assert.Contains(t, msg, `"auto"`)
	assert.Contains(t, msg, `"manual"`)
	assert.Contains(t, msg, `"periodic"`)
}

func TestReceiverAckModesThatWorkStillParse(t *testing.T) {
	for _, body := range []string{
		`ack = "auto"`,
		`ack = "periodic"`,
		"ack = \"manual\"\n    settle_timeout = \"30s\"",
	} {
		t.Run(body, func(t *testing.T) {
			_, hasErr, msg := build(t, receiverWithBody(body))
			require.False(t, hasErr, msg)
		})
	}
}

// Refused until this receiver had a per-record settler, because `auto` could
// only settle when delivery returned — which, behind a queue, is the enqueue.
// The settler travels with the record now, so the acknowledgement follows the
// work through the queue and the combination is correct rather than a trap.
func TestQueueSizeIsAcceptedWithAuto(t *testing.T) {
	for _, body := range []string{
		"queue_size = 16",
		"ack = \"auto\"\n    queue_size = 16",
		"ack = \"auto\"\n    queue_size = 16\n    partitions = 4\n    partition_key = ctx.topic",
	} {
		t.Run(body, func(t *testing.T) {
			_, hasErr, msg := build(t, receiverWithBody(body))
			require.False(t, hasErr, msg)
		})
	}
}

// Optional under auto, where the framework settles at a known point but the
// chain reaching it may be one the configuration does not fully trust. It was
// refused here while there was no single delivery for a bound to apply to.
func TestSettleTimeoutIsAcceptedWithAuto(t *testing.T) {
	_, hasErr, msg := build(t, receiverWithBody(
		"ack = \"auto\"\n    settle_timeout = \"30s\""))
	require.False(t, hasErr, msg)
}

// Neither mode has a per-record acknowledgement for a bound to apply to:
// periodic advances offsets on a timer whatever happens.
func TestSettleTimeoutIsRefusedWithPeriodic(t *testing.T) {
	_, hasErr, msg := build(t, receiverWithBody(
		"ack = \"periodic\"\n    settle_timeout = \"30s\""))
	require.True(t, hasErr, "settle_timeout under periodic should be rejected")
	assert.Contains(t, msg, "does not apply")
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
