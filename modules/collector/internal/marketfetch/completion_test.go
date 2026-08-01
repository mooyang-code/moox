package marketfetch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHandleCompletionMarksPermanentFailureOnTaskInstance(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	completedAt := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	batch := completionTestBatch("invalid")
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{completionTestInstance()}))

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		TaskId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: "2026-08-02T07:59:00Z",
		Outcome: string(domain.ItemOutcomeInvalid), ErrorType: "invalid_symbol", ErrorSummary: "symbol is delisted",
	}, completedAt)
	payload.BatchId = batch.BatchID
	payload.ScheduleId = batch.ScheduleID
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)))

	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusFailed, instance.LastExecStatus)
	require.NotNil(t, instance.LastExecTime)
	assert.Equal(t, completedAt, instance.LastExecTime.UTC())
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(instance.Result), &result))
	assert.Equal(t, "invalid_symbol", result["error_type"])
	assert.Equal(t, "symbol is delisted", result["error_summary"])
}

func TestHandleCompletionMarksTaskInstanceFailedWhenRetriesAreExhausted(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	completedAt := time.Date(2026, time.August, 2, 8, 5, 0, 0, time.UTC)
	batch := completionTestBatch("retry-exhausted")
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{completionTestInstance()}))
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: "retry-key", Attempt: 3, Status: "pending", CreateTime: completedAt.Add(-time.Minute)}))

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		TaskId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: "2026-08-02T07:59:00Z", SourceEventId: "retry-key",
		Outcome: string(domain.ItemOutcomeHTTP429), ErrorType: "rate_limit", ErrorSummary: "too many requests",
	}, completedAt)
	payload.BatchId = batch.BatchID
	payload.ScheduleId = batch.ScheduleID
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)))

	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusFailed, instance.LastExecStatus)
	retry, err := db.FetchRetries().Get(ctx, "crypto", "retry-key")
	require.NoError(t, err)
	assert.Equal(t, "permanent_failed", retry.Status)
}

func TestHandleCompletionKeepsSuccessfulTaskInstanceSuccessful(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	batch := completionTestBatch("success")
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{completionTestInstance()}))
	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{TaskId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", Outcome: string(domain.ItemOutcomeSuccess)}, time.Date(2026, time.August, 2, 8, 10, 0, 0, time.UTC))
	payload.BatchId = batch.BatchID
	payload.ScheduleId = batch.ScheduleID
	payload.Status = string(domain.BatchStatusSucceeded)
	payload.SuccessCount = 1
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)))

	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
}

func TestHandleCompletionDoesNotRegressNewSuccessWithSupersededRetryFailure(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{completionTestInstance()}))
	olderTarget := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	newerTarget := olderTarget.Add(time.Minute)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "old-retry", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: olderTarget,
		Attempt: 3, Status: "dispatched", CreateTime: olderTarget,
	}))
	olderBatch := completionTestBatch("old-retry")
	newerBatch := completionTestBatch("new-success")
	created, err := db.FetchBatches().CreatePlanned(ctx, &olderBatch)
	require.NoError(t, err)
	require.True(t, created)
	created, err = db.FetchBatches().CreatePlanned(ctx, &newerBatch)
	require.NoError(t, err)
	require.True(t, created)

	newerCompletedAt := newerTarget.Add(time.Minute)
	newerPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		TaskId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: newerTarget.Format(time.RFC3339Nano), Outcome: string(domain.ItemOutcomeSuccess),
	}, newerCompletedAt)
	newerPayload.BatchId = newerBatch.BatchID
	newerPayload.ScheduleId = newerBatch.ScheduleID
	newerPayload.Status = string(domain.BatchStatusSucceeded)
	newerPayload.SuccessCount = 1
	newerPayload.PermanentFailedCount = 0
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(newerPayload)))
	retry, err := db.FetchRetries().Get(ctx, "crypto", "old-retry")
	require.NoError(t, err)
	assert.Equal(t, "superseded", retry.Status)

	olderPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		TaskId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: olderTarget.Format(time.RFC3339Nano), SourceEventId: "old-retry",
		Outcome: string(domain.ItemOutcomeInvalid), ErrorType: "invalid_symbol", ErrorSummary: "symbol is delisted",
	}, newerCompletedAt.Add(time.Minute))
	olderPayload.BatchId = olderBatch.BatchID
	olderPayload.ScheduleId = olderBatch.ScheduleID
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(olderPayload)))

	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
	assert.Equal(t, newerCompletedAt, instance.LastExecTime.UTC())
}

func TestHandleCompletionDoesNotRegressNewSuccessWithOlderRealtimeFailure(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{completionTestInstance()}))
	olderTarget := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	newerTarget := olderTarget.Add(time.Minute)
	olderBatch := completionTestBatch("old-realtime")
	newerBatch := completionTestBatch("new-realtime")
	created, err := db.FetchBatches().CreatePlanned(ctx, &olderBatch)
	require.NoError(t, err)
	require.True(t, created)
	created, err = db.FetchBatches().CreatePlanned(ctx, &newerBatch)
	require.NoError(t, err)
	require.True(t, created)

	newerCompletedAt := newerTarget.Add(time.Minute)
	newerPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		TaskId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: newerTarget.Format(time.RFC3339Nano), Outcome: string(domain.ItemOutcomeSuccess),
	}, newerCompletedAt)
	newerPayload.BatchId = newerBatch.BatchID
	newerPayload.ScheduleId = newerBatch.ScheduleID
	newerPayload.Status = string(domain.BatchStatusSucceeded)
	newerPayload.SuccessCount = 1
	newerPayload.PermanentFailedCount = 0
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(newerPayload)))

	olderPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		TaskId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: olderTarget.Format(time.RFC3339Nano),
		Outcome: string(domain.ItemOutcomeInvalid), ErrorType: "invalid_symbol", ErrorSummary: "symbol is delisted",
	}, newerCompletedAt.Add(time.Second))
	olderPayload.BatchId = olderBatch.BatchID
	olderPayload.ScheduleId = olderBatch.ScheduleID
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(olderPayload)))

	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
	assert.Equal(t, newerCompletedAt, instance.LastExecTime.UTC())
}

func newCompletionTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func completionTestBatch(suffix string) domain.BatchInvocation {
	return domain.BatchInvocation{SpaceID: "crypto", BatchID: "batch-" + suffix, ScheduleID: "schedule-" + suffix, BatchKind: domain.BatchKindRealtime, DatasetID: "bars", Frequency: "1m", NodeID: "node-1", Status: domain.BatchStatusPlanned, PlannedCount: 1}
}

func completionTestInstance() domain.TaskInstance {
	return domain.TaskInstance{SpaceID: "crypto", TaskID: "task-btc", RuleID: "rule", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
}

func completionTestPayload(item *marketfetchpb.MarketFetchItemResult, completedAt time.Time) *marketfetchpb.MarketFetchBatchCompleted {
	return &marketfetchpb.MarketFetchBatchCompleted{ScheduleId: "schedule", BatchKind: string(domain.BatchKindRealtime), DatasetId: "bars", Frequency: "1m", NodeId: "node-1", PlannedCount: 1, Status: string(domain.BatchStatusFailed), PermanentFailedCount: 1, CompletedAt: timestamppb.New(completedAt), Items: []*marketfetchpb.MarketFetchItemResult{item}}
}

func completionTestDelivery(payload *marketfetchpb.MarketFetchBatchCompleted) *events.EventDelivery {
	return &events.EventDelivery{Message: &eventpb.EventMessage{SpaceId: "crypto"}, Payload: payload}
}
