package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchBatchCompletionLateSuccessOnlyCancelsPendingRetry(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, item := range []*domain.RetryItem{
		{SpaceID: "crypto", RetryKey: "pending", Status: "pending", Attempt: 1, CreateTime: now},
		{SpaceID: "crypto", RetryKey: "dispatched", Status: "dispatched", Attempt: 1, CreateTime: now},
	} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, item))
	}
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "late", Status: domain.BatchStatusPlanned, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	batch.Status = domain.BatchStatusTimedOut
	updated, err := s.FetchBatches().Complete(ctx, batch)
	require.NoError(t, err)
	require.True(t, updated)
	batch.LateCompletion = true
	updated, err = s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{CancelPendingRetryKeys: []string{"pending", "dispatched"}})
	require.NoError(t, err)
	require.True(t, updated)

	pending, err := s.FetchRetries().Get(ctx, "crypto", "pending")
	require.NoError(t, err)
	dispatched, err := s.FetchRetries().Get(ctx, "crypto", "dispatched")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", pending.Status)
	assert.Equal(t, "dispatched", dispatched.Status)
}

func TestDueMarketFetchRecordsAreIsolatedBySpace(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, spaceID := range []string{"crypto", "research"} {
		batch := &domain.BatchInvocation{SpaceID: spaceID, BatchID: "batch-" + spaceID, Status: domain.BatchStatusPlanned, DeadlineAt: &now}
		created, err := s.FetchBatches().CreatePlanned(ctx, batch)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: spaceID, RetryKey: "retry-" + spaceID, Status: "pending", NextRetryAt: &now, CreateTime: now}))
	}
	batches, err := s.FetchBatches().ListDue(ctx, "crypto", now, 10)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	assert.Equal(t, "crypto", batches[0].SpaceID)
	retries, err := s.FetchRetries().ListDue(ctx, "crypto", now, 10)
	require.NoError(t, err)
	require.Len(t, retries, 1)
	assert.Equal(t, "crypto", retries[0].SpaceID)
}

func TestFetchBatchCompletionMarksDispatchedRetryPermanent(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	retry := &domain.RetryItem{SpaceID: "crypto", RetryKey: "delisted", Status: "dispatched", Attempt: 2, CreateTime: now}
	require.NoError(t, s.FetchRetries().Upsert(ctx, retry))
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "retry-child", Status: domain.BatchStatusPlanned, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	batch.Status = domain.BatchStatusFailed
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{PermanentRetryKeys: []string{"delisted"}})
	require.NoError(t, err)
	require.True(t, updated)
	stored, err := s.FetchRetries().Get(ctx, "crypto", "delisted")
	require.NoError(t, err)
	assert.Equal(t, "permanent_failed", stored.Status)
}

func TestFetchBatchCompletionSupersedesOlderPendingRetry(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, item := range []*domain.RetryItem{
		{SpaceID: "crypto", RetryKey: "older", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: now.Add(-time.Minute), Status: "pending", CreateTime: now},
		{SpaceID: "crypto", RetryKey: "newer", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: now.Add(time.Minute), Status: "pending", CreateTime: now},
	} {
		require.NoError(t, s.FetchRetries().Upsert(ctx, item))
	}
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: "realtime", Status: domain.BatchStatusPlanned, CompletedAt: &now}
	created, err := s.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	batch.Status = domain.BatchStatusSucceeded
	updated, err := s.FetchBatches().CompleteWithEffects(ctx, batch, FetchCompletionEffects{SupersedePendingRetries: []MarketFetchRetrySupersede{{SpaceID: "crypto", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: now}}})
	require.NoError(t, err)
	require.True(t, updated)
	older, err := s.FetchRetries().Get(ctx, "crypto", "older")
	require.NoError(t, err)
	newer, err := s.FetchRetries().Get(ctx, "crypto", "newer")
	require.NoError(t, err)
	assert.Equal(t, "superseded", older.Status)
	assert.Equal(t, "pending", newer.Status)
}
