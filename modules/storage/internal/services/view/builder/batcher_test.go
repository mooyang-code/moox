package builder

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeBatchOptionsAppliesDefaults(t *testing.T) {
	opts := normalizeBatchOptions(BatchOptions{})

	assert.Equal(t, 500, opts.BatchSize)
	assert.Equal(t, 200*time.Millisecond, opts.BatchWait)
}

func TestBatcherFlushOnBatchSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := newBatcher[int](BatchOptions{BatchSize: 2, BatchWait: time.Hour})
	out := make(chan []int, 1)
	go b.run(ctx, out)

	require.NoError(t, b.add(ctx, 1))
	require.NoError(t, b.add(ctx, 2))

	select {
	case batch := <-out:
		assert.Equal(t, []int{1, 2}, batch)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch flush")
	}
}

func TestBatcherFlushesPendingOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	b := newBatcher[int](BatchOptions{BatchSize: 10, BatchWait: time.Hour})
	out := make(chan []int, 1)
	exited := make(chan struct{})
	go func() {
		b.run(ctx, out)
		close(exited)
	}()

	require.NoError(t, b.add(ctx, 42))
	cancel()

	select {
	case batch := <-out:
		assert.Equal(t, []int{42}, batch)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for drain flush")
	}
	<-exited
}

func TestBatcherAddCancelledContextReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := newBatcher[int](BatchOptions{BatchSize: 1, BatchWait: time.Millisecond})
	err := b.add(ctx, 1)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
