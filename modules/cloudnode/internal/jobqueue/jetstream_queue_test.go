package jobqueue

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJetStreamQueue_PublishValidatesInput(t *testing.T) {
	q := NewJetStreamQueue(&Runtime{}, QueueConfig{})

	_, err := q.Publish(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	_, err = q.Publish(context.Background(), &pb.JobItem{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "space_id")
}

func TestJetStreamQueue_AckRequiresInflightToken(t *testing.T) {
	q := NewJetStreamQueue(nil, QueueConfig{})
	err := q.Ack(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	err = q.Nak(context.Background(), "missing", 0)
	require.Error(t, err)

	err = q.Term(context.Background(), "missing")
	require.Error(t, err)

	err = q.InProgress(context.Background(), "missing")
	require.Error(t, err)
}

func TestJetStreamQueue_CloseWithoutConsumer(t *testing.T) {
	q := NewJetStreamQueue(nil, QueueConfig{})
	assert.NoError(t, q.Close())
}
