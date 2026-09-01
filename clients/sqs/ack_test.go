package sqs_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// receiverWith is a minimal receiver parameterized on its settle attributes.
func receiverWith(body string) string {
	return fmt.Sprintf(`
bus "main" {}

client "sqs_receiver" "r" {
  region     = "us-east-1"
  queue_url  = "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue"
  subscriber = bus.main
  %s
}
`, body)
}

func TestReceiverAckModesParse(t *testing.T) {
	for _, body := range []string{
		``,
		`ack = "auto"`,
		"ack = \"manual\"\n  settle_timeout = \"30s\"",
	} {
		t.Run(body, func(t *testing.T) {
			_, hasErr, msg := build(t, receiverWith(body))
			require.False(t, hasErr, msg)
		})
	}
}

func TestReceiverAckInvalidNamesTheModesThisReceiverHas(t *testing.T) {
	_, hasErr, msg := build(t, receiverWith(`ack = "periodic"`))
	require.True(t, hasErr, "SQS has no periodic mode")
	assert.Contains(t, msg, `"auto"`)
	assert.Contains(t, msg, `"manual"`)
	assert.NotContains(t, msg, `"periodic" or`)
}

// Nothing settles a message under manual, so a bound on how long it may go
// unsettled is required rather than defaulted: no one value suits both a 50ms
// enrichment and a five-minute batch.
func TestManualRequiresSettleTimeout(t *testing.T) {
	_, hasErr, msg := build(t, receiverWith(`ack = "manual"`))
	require.True(t, hasErr)
	assert.Contains(t, msg, "requires settle_timeout")
}

// And it is refused where it could not apply: under auto the message is settled
// for you when delivery returns, so there is no unsettled message to bound.
// Permitted under `auto`, because the deletion now follows the work rather than
// delivery's return — so there is a genuinely unsettled message for a bound to
// apply to. Not required: the framework settles at a known point.
func TestSettleTimeoutIsAllowedWithAutoDelete(t *testing.T) {
	_, hasErr, msg := build(t, receiverWith(`settle_timeout = "30s"`))
	assert.False(t, hasErr, "settle_timeout should bound an automatic delete too: %s", msg)
}

func TestSettleTimeoutMustBePositive(t *testing.T) {
	_, hasErr, msg := build(t, receiverWith("ack = \"manual\"\n  settle_timeout = \"0s\""))
	require.True(t, hasErr)
	assert.Contains(t, msg, "must be positive")
}

// A queue makes delivery return at the moment the message is queued, which used
// to mean `auto` deleted the message before the handler ran. It no longer does:
// the delivery carries a settler that travels with the message, so the deletion
// arrives when the work finishes, however far down the chain that is. The
// combination is correct rather than refused.
func TestQueueSizeIsAllowedWithAutoDelete(t *testing.T) {
	for _, body := range []string{
		"queue_size = 16",
		"ack = \"auto\"\n  queue_size = 16",
	} {
		t.Run(body, func(t *testing.T) {
			_, hasErr, msg := build(t, receiverWith(body))
			assert.False(t, hasErr, "queue_size with an auto delete should be accepted: %s", msg)
		})
	}
}

// Manual settle is the other way to keep the queue: nothing is deleted until
// the configuration says so, and the delivery rides ctx through the queue, so
// the settle follows the real outcome rather than the enqueue.
func TestQueueSizeIsAllowedWithManualSettle(t *testing.T) {
	_, hasErr, msg := build(t, receiverWith(
		"ack = \"manual\"\n  settle_timeout = \"30s\"\n  queue_size = 16"))
	require.False(t, hasErr, msg)
}

// The old spelling is met with what it became, rather than with gohcl's
// "argument named auto_delete is not expected here".
func TestRetiredAutoDeleteSaysWhatItBecame(t *testing.T) {
	_, hasErr, msg := build(t, receiverWith(`auto_delete = false`))
	require.True(t, hasErr, "auto_delete should no longer be accepted")
	assert.Contains(t, msg, `"auto_delete" is now "ack"`)
	assert.Contains(t, msg, "0.46.0")
	assert.Contains(t, msg, `ack = "manual"`)
}
