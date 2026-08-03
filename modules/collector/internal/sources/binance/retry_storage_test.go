package binance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryMetadataStorageRetriesInsideSingleAttemptBatch(t *testing.T) {
	var calls atomic.Int32
	err := retryMetadataStorage(context.Background(), func() error {
		if calls.Add(1) < 3 {
			return errors.New("temporary metadata transport failure")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}

func TestRetryStorageUsesConfiguredAttempts(t *testing.T) {
	t.Setenv("MOOX_FETCH_STORAGE_MAX_ATTEMPTS", "2")
	var calls atomic.Int32
	err := retryStorage(context.Background(), func() error {
		calls.Add(1)
		return errors.New("temporary write failure")
	})
	require.Error(t, err)
	assert.Equal(t, int32(2), calls.Load())
}

func TestRetryStorageWithAttemptTimeoutUsesIndependentAttemptContexts(t *testing.T) {
	t.Setenv("MOOX_FETCH_STORAGE_MAX_ATTEMPTS", "3")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	err := retryStorageWithAttemptTimeout(ctx, func(attemptCtx context.Context) error {
		deadline, ok := attemptCtx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), storageAttemptTimeoutCap)
		if calls.Add(1) < 3 {
			return errors.New("temporary write failure")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}

func TestRetryStorageWithAttemptTimeoutRetriesChildDeadline(t *testing.T) {
	t.Setenv("MOOX_FETCH_STORAGE_MAX_ATTEMPTS", "3")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	err := retryStorageWithAttemptTimeout(ctx, func(context.Context) error {
		if calls.Add(1) < 3 {
			return context.DeadlineExceeded
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}
