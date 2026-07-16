package jobqueue

import (
	"context"
	"errors"
	"sync"
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/jetstream"
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
	assert.Contains(t, err.Error(), "not found")

	err = q.Term(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	err = q.InProgress(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestJetStreamQueue_CloseWithoutConsumer(t *testing.T) {
	q := NewJetStreamQueue(nil, QueueConfig{})
	assert.NoError(t, q.Close())
}

func TestConsumerConfigForRouteUsesExactRoute(t *testing.T) {
	cfg := QueueConfig{Naming: NamingConfig{SubjectPrefix: "moox.cloudnode"}}

	got := consumerConfigForRoute(cfg, "crypto", "moox-collector_v202607142250", "collect.kline")

	require.Equal(t, ConsumerName("crypto", "moox-collector_v202607142250", "collect.kline"), got.Durable)
	require.Equal(t, ExecFilterSubject(cfg.Naming, "crypto", "moox-collector_v202607142250", "collect.kline"), got.FilterSubject)
}

func TestRouteConsumerKeySeparatesJobTypes(t *testing.T) {
	require.NotEqual(t,
		routeConsumerKey("crypto", "moox-collector_v202607142250", "collect.kline"),
		routeConsumerKey("crypto", "moox-collector_v202607142250", "collect.symbol"),
	)
}

func TestShouldRecreateRouteConsumerOnlyForConfigurationDrift(t *testing.T) {
	require.True(t, shouldRecreateRouteConsumer(errors.Join(errors.New("consumer drift"), jetstream.ErrInvalidConsumer)))
	require.False(t, shouldRecreateRouteConsumer(errors.New("network down")))
}

func TestTryAcquireFetchLockSkipsBusyRoute(t *testing.T) {
	lock := &sync.Mutex{}
	lock.Lock()
	require.False(t, tryAcquireFetchLock(lock))
	lock.Unlock()
	require.True(t, tryAcquireFetchLock(lock))
	lock.Unlock()
}
