package rabbitmq_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// receiverWithAck is a minimal receiver parameterized on ack.
func receiverWithAck(body string) string {
	return fmt.Sprintf(`
bus "main" {}

client "rabbitmq" "events" {
  brokers = ["amqp://localhost:5672/"]

  receiver "in" {
    queue      = "q"
    subscriber = bus.main
    %s
  }
}
`, body)
}

func TestReceiverAckInvalidNamesTheModesThisReceiverHas(t *testing.T) {
	_, hasErr, msg := buildSrc(t, receiverWithAck(`ack = "whenever"`))
	require.True(t, hasErr, "an unknown ack should be rejected")
	assert.Contains(t, msg, `"auto"`)
	assert.Contains(t, msg, `"manual"`)
	assert.Contains(t, msg, `"none"`)
}

// The three modes RabbitMQ has, and the default. `auto` is what
// `auto_ack = false` did — vinculum acknowledges after handling — and `none` is
// what `auto_ack = true` did, AMQP's own no-ack mode.
func TestReceiverAckModesThisReceiverHasStillParse(t *testing.T) {
	for _, body := range []string{
		`ack = "auto"`,
		`ack = "none"`,
		"ack = \"manual\"\n    settle_timeout = \"30s\"",
		``,
	} {
		t.Run(body, func(t *testing.T) {
			_, hasErr, msg := buildSrc(t, receiverWithAck(body))
			require.False(t, hasErr, msg)
		})
	}
}

// Nothing settles a manual delivery until the configuration does, and an
// unsettled AMQP delivery holds a prefetch slot indefinitely — ten of them stall
// the receiver. There is no default bound worth choosing, so the configuration
// states the one it can live with.
func TestManualRequiresSettleTimeout(t *testing.T) {
	_, hasErr, msg := buildSrc(t, receiverWithAck(`ack = "manual"`))
	require.True(t, hasErr, `ack = "manual" without settle_timeout should be rejected`)
	assert.Contains(t, msg, "requires settle_timeout")
}

func TestSettleTimeoutWithoutManualIsRejected(t *testing.T) {
	_, hasErr, msg := buildSrc(t, receiverWithAck(`settle_timeout = "30s"`))
	require.True(t, hasErr, "settle_timeout under an automatic ack should be rejected")
	assert.Contains(t, msg, `settle_timeout applies only to ack = "manual"`)
}

func TestSettleTimeoutMustBePositive(t *testing.T) {
	_, hasErr, msg := buildSrc(t, receiverWithAck("ack = \"manual\"\n    settle_timeout = \"0s\""))
	require.True(t, hasErr, "a zero bound should be rejected")
	assert.Contains(t, msg, "settle_timeout must be positive")
}

// A queue makes delivery return at the moment the message is queued, so `auto`
// would ack before the handler ran — and a handler error would no longer nack
// the message to the dead-letter exchange. Refused, written or defaulted.
func TestQueueSizeIsRefusedWithAutoAck(t *testing.T) {
	for _, body := range []string{
		"queue_size = 16",
		"ack = \"auto\"\n    queue_size = 16",
	} {
		t.Run(body, func(t *testing.T) {
			_, hasErr, msg := buildSrc(t, receiverWithAck(body))
			require.True(t, hasErr, "queue_size with an auto ack should be rejected")
			assert.Contains(t, msg, "queue_size cannot be combined")
			// Manual settle is the way to keep the queue, so the diagnostic
			// names it. `concurrency` is the other answer and this receiver
			// does not have one — a serial delivery loop is not parallelized by
			// prefetch — so it must not be named.
			assert.Contains(t, msg, `ack = "manual"`)
			assert.NotContains(t, msg, "concurrency")
		})
	}
}

// AMQP's no-ack mode never acknowledges anything, so there is nothing for the
// queue to acknowledge early. Left alone deliberately.
func TestQueueSizeIsAllowedWithNoAck(t *testing.T) {
	_, hasErr, msg := buildSrc(t, receiverWithAck("ack = \"none\"\n    queue_size = 16"))
	require.False(t, hasErr, msg)
}

// The pairing manual settle exists for: the queue decouples the delivery loop
// from the work, and the acknowledgement follows the work rather than the
// enqueue, because the delivery travels on ctx.
func TestQueueSizeIsAllowedWithManualAck(t *testing.T) {
	_, hasErr, msg := buildSrc(t, receiverWithAck(
		"ack = \"manual\"\n    settle_timeout = \"30s\"\n    queue_size = 16"))
	require.False(t, hasErr, msg)
}

// The old spelling is met with what it became. It is worth spelling out here
// because the boolean inverted on the way: `auto_ack = false` was the default
// and is now the default `ack = "auto"`.
func TestRetiredAutoAckSaysWhatItBecame(t *testing.T) {
	_, hasErr, msg := buildSrc(t, receiverWithAck(`auto_ack = true`))
	require.True(t, hasErr, "auto_ack should no longer be accepted")
	assert.Contains(t, msg, `"auto_ack" is now "ack"`)
	assert.Contains(t, msg, "0.46.0")
	assert.Contains(t, msg, `ack = "none"`)
}

// The rename is scoped to the receiver body. A `declare` block's own
// `auto_delete` means AMQP's delete-the-queue-when-unused and must be left
// alone — a rename table that reached it would report a working attribute.
func TestDeclareAutoDeleteIsUntouchedByTheRename(t *testing.T) {
	const src = `
bus "main" {}

client "rabbitmq" "events" {
  brokers = ["amqp://localhost:5672/"]

  receiver "in" {
    queue      = "q"
    subscriber = bus.main

    declare {
      durable     = false
      auto_delete = true
    }
  }
}
`
	_, hasErr, msg := buildSrc(t, src)
	require.False(t, hasErr, msg)
}
