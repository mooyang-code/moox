package binance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryMetadataStorageRetriesInsideSingleAttemptBatch(t *testing.T) {
	var calls atomic.Int32
	err := retryMetadataStorage(SingleAttempt(context.Background()), func() error {
		if calls.Add(1) < 3 {
			return errors.New("temporary metadata transport failure")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}

func TestRetryStorageRemainsSingleAttemptForAggregateWrites(t *testing.T) {
	var calls atomic.Int32
	err := retryStorage(SingleAttempt(context.Background()), func() error {
		calls.Add(1)
		return errors.New("temporary write failure")
	})
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load())
}
