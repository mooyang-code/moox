package jobqueue

import (
	"context"
	"sync"
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
	require.NotContains(t, got.FilterSubject, ">")
	require.NotEqual(t, got.FilterSubject, ExecFilterSubject(cfg.Naming, "crypto", "moox-collector_v202607142250", "collect.symbol"))
}

func TestRouteConsumerKeySeparatesJobTypes(t *testing.T) {
	require.NotEqual(t,
		routeConsumerKey("crypto", "moox-collector_v202607142250", "collect.kline"),
		routeConsumerKey("crypto", "moox-collector_v202607142250", "collect.symbol"),
	)
}

func TestTryAcquireFetchLockSkipsBusyRoute(t *testing.T) {
	lock := &sync.Mutex{}
	lock.Lock()
	require.False(t, tryAcquireFetchLock(lock))
	lock.Unlock()
	require.True(t, tryAcquireFetchLock(lock))
	lock.Unlock()
}

func TestRotateStringsAdvancesAcrossRoutes(t *testing.T) {
	values := []string{"collect.kline", "collect.symbol", "collect.trade"}
	require.Equal(t, []string{"collect.symbol", "collect.trade", "collect.kline"}, rotateStrings(values, 1))
	require.Equal(t, []string{"collect.trade", "collect.kline", "collect.symbol"}, rotateStrings(values, 2))
}

func TestOrderedJobTypesIsolatesIndependentRouteSets(t *testing.T) {
	q := NewJetStreamQueue(nil, QueueConfig{})
	multi := []string{"collect.kline", "collect.symbol"}
	require.Equal(t, multi, q.orderedJobTypes("crypto", "pkg", multi))
	require.Equal(t, []string{"collect.kline"}, q.orderedJobTypes("crypto", "pkg", []string{"collect.kline"}))
	require.Equal(t, []string{"collect.symbol", "collect.kline"}, q.orderedJobTypes("crypto", "pkg", []string{"collect.symbol", "collect.kline"}))
	require.Equal(t, multi, q.orderedJobTypes("stocks", "pkg", multi))
}
