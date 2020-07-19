package requeue_test

import (
	"testing"

	requeue "github.com/nickpoorman/nats-requeue"
	"github.com/nickpoorman/nats-requeue/flatbuf"
	"github.com/stretchr/testify/assert"
)

func TestRequeueMessage_FlatbufMutate(t *testing.T) {
	msg := requeue.DefaultRequeueMessage()
	msg.Retries = 5
	msg.TTL = 10000
	msg.Delay = 20000
	msg.BackoffStrategy = requeue.BackoffStrategy_Exponential
	msg.OriginalSubject = "foo.bar"
	msg.OriginalPayload = []byte("my awesome message")

	msgBytes := msg.Bytes()

	fb := flatbuf.GetRootAsRequeueMessage(msgBytes, 0)

	assert.Equal(t, fb.Retries(), uint64(5))
	assert.True(t, fb.MutateRetries(4))
	assert.Equal(t, fb.Retries(), uint64(4))

	// Assert that fb was modifying the underlying bytes.
	fb2 := flatbuf.GetRootAsRequeueMessage(msgBytes, 0)
	assert.Equal(t, fb2.Retries(), uint64(4))
}
