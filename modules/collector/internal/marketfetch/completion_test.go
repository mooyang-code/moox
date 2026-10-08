package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func TestStartCompletionConsumerWithDoneWaitsForCancellation(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	done, err := StartCompletionConsumerWithDone(ctx, "crypto", db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil)
	require.NoError(t, err)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completion consumer did not stop after cancellation")
	}
}

func TestCompletionEventBusConfigPrefersPackagedCAFile(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_NATS_TLS_CA_FILE", "/var/task/certs/eventbus-ca.pem")
	t.Setenv("MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64", "c3RhbGUtY2E=")

	cfg, err := completionEventBusConfig()
	require.NoError(t, err)
	assert.Equal(t, "/var/task/certs/eventbus-ca.pem", cfg.TLSCAFile)
	assert.Empty(t, cfg.TLSCAPEMBase64)
}

func TestRetryCollectionItemUsesExactInstanceIDBeforeSubjectFallback(t *testing.T) {
	request := Request{DatasetID: "dataset_stockcn_equity_kline_1m", Items: []domain.CollectionItem{
		{InstanceID: "snapshot-shard-0", SubjectID: "stockcn", DatasetID: "dataset_stockcn_equity_kline_1m", DataType: "instrument", SnapshotAt: "2026-08-30T00:00:00Z", SnapshotShardIndex: 0, SnapshotShardCount: 2},
		{InstanceID: "snapshot-shard-1", SubjectID: "stockcn", DatasetID: "dataset_stockcn_equity_kline_1m", DataType: "instrument", SnapshotAt: "2026-08-30T00:00:00Z", SnapshotShardIndex: 1, SnapshotShardCount: 2},
	}}
	result := &marketfetchpb.MarketFetchItemResult{InstanceId: "snapshot-shard-1", SubjectId: "stockcn", Outcome: string(domain.ItemOutcomeProviderError)}

	item := retryCollectionItem(request, result, "retry-shard-1")

	assert.Equal(t, "snapshot-shard-1", item.InstanceID)
	assert.Equal(t, 1, item.SnapshotShardIndex)
	assert.Equal(t, 2, item.SnapshotShardCount)
	assert.Equal(t, "2026-08-30T00:00:00Z", item.SnapshotAt)
	assert.Equal(t, klineProviderAttemptBudget, item.CandidateIndex)
}

func TestHandleCompletionMarksPermanentFailureOnTaskInstance(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	completedAt := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	batch := completionTestBatch("invalid")
	var request Request
	require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
	request.Items[0].TargetDataTime = "2026-08-02T07:59:00Z"
	requestJSON, err := json.Marshal(request)
	require.NoError(t, err)
	batch.RequestJSON = string(requestJSON)
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, batch.BatchID)

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: "2026-08-02T07:59:00Z",
		Outcome: string(domain.ItemOutcomeInvalid), ErrorType: "invalid_symbol", ErrorSummary: "symbol is delisted",
	}, completedAt)
	payload.BatchId = batch.BatchID
	payload.ScheduleId = batch.ScheduleID
	wakeCalls := 0
	wake := func() {
		wakeCalls++
		key := retryKey(batch.BatchID, "BTC-USDT", "2026-08-02T07:59:00Z")
		row, getErr := db.FetchRetries().Get(ctx, "crypto", key)
		require.NoError(t, getErr, "completion must wake only after its permanent-failure outbox row is durable")
		require.Equal(t, "permanent_failed", row.Status)
	}
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload), wake))
	require.Equal(t, 1, wakeCalls)

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

func TestHandleCompletionDoesNotWakeWhenPermanentFailurePersistenceFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	triggerDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := triggerDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	completedAt := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	batch := completionTestBatch("invalid-persist-failure")
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, batch.BatchID)
	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: "2026-08-02T07:59:00Z",
		Outcome: string(domain.ItemOutcomeInvalid), ErrorType: "invalid_symbol", ErrorSummary: "symbol is delisted",
	}, completedAt)
	payload.BatchId = batch.BatchID
	payload.ScheduleId = batch.ScheduleID
	require.NoError(t, triggerDB.Exec("CREATE TRIGGER fail_failure_retry BEFORE INSERT ON t_collector_fetch_retry_items BEGIN SELECT RAISE(ABORT, 'retry insert failure'); END").Error)
	wakeCalls := 0
	err = handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload), func() { wakeCalls++ })
	require.Error(t, err)
	require.Zero(t, wakeCalls, "the reporter must not wake before a failed completion transaction commits")
}

func TestHandleCompletionMarksTaskInstanceFailedWhenRetriesAreExhausted(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	completedAt := time.Date(2026, time.August, 2, 8, 5, 0, 0, time.UTC)
	batch := completionTestBatch("retry-exhausted")
	var request Request
	require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
	request.Items[0].TargetDataTime = "2026-08-02T07:59:00Z"
	request.Items[0].SourceEventID = "retry-key"
	request.Items[0].SeriesIndex = 1
	request.Items[0].SeriesHash = "bars-series-hash"
	request.Items[0].ExpectedCount = 2
	request.Items[0].MarketType = "spot"
	request.Items[0].Provider = "binance"
	request.Targets = []domain.WriteTarget{{
		ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars",
		SeriesIndex: 1, SeriesHash: "bars-series-hash", ExpectedCount: 2,
	}}
	requestJSON, err := json.Marshal(request)
	require.NoError(t, err)
	batch.RequestJSON = string(requestJSON)
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, batch.BatchID)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: "retry-key", Attempt: 3, Status: "pending", CreateTime: completedAt.Add(-time.Minute)}))

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: "2026-08-02T07:59:00Z", SourceEventId: "retry-key",
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
	assert.Equal(t, "rate_limit", retry.LastErrorType)
	assert.Equal(t, "too many requests", retry.LastErrorSummary)
	var failureTargets []domain.WriteTarget
	require.NoError(t, json.Unmarshal([]byte(retry.FailureTargetsJSON), &failureTargets))
	require.Len(t, failureTargets, 1)
	assert.Equal(t, "bars", failureTargets[0].DatasetID)
	assert.Equal(t, uint32(1), failureTargets[0].SeriesIndex)
	assert.Equal(t, uint32(2), failureTargets[0].ExpectedCount)
	assert.Equal(t, "bars-series-hash", failureTargets[0].SeriesHash)
	assert.Equal(t, time.Date(2026, time.August, 2, 7, 59, 0, 0, time.UTC), retry.TargetDataTime.UTC())
	target, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-btc")
	require.NoError(t, err)
	assert.Equal(t, "failed", target.Status)
}

func TestHandleCompletionExhaustsWriteTargetRetriesAfterThreeRetries(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	completedAt := time.Date(2026, time.August, 2, 8, 5, 0, 0, time.UTC)
	const targetDataTime = "2026-08-02T07:59:00Z"
	retryKey := writeTargetRetryKey("task-btc", "target-btc", targetDataTime)

	batch := completionTestBatch("write-target-initial")
	periodTime, err := time.Parse(time.RFC3339Nano, targetDataTime)
	require.NoError(t, err)
	periodDeadline := periodTime.Add(15 * time.Minute)
	batch.PeriodTime = &periodTime
	batch.PeriodDeadlineAt = &periodDeadline
	var request Request
	require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
	request.Items[0].TargetDataTime = targetDataTime
	request.Targets = []domain.WriteTarget{{
		ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars",
		SeriesIndex: 0, SeriesHash: "bars-hash", ExpectedCount: 1,
	}}
	requestJSON, err := json.Marshal(request)
	require.NoError(t, err)
	batch.RequestJSON = string(requestJSON)
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, batch.BatchID)

	completeTargetWriteFailure := func(batch domain.BatchInvocation, completedAt time.Time) {
		payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
			InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: targetDataTime,
			SourceEventId: func() string {
				if batch.RetryScope == "write_target" {
					return retryKey
				}
				return ""
			}(),
			Outcome: string(domain.ItemOutcomeSuccess),
			Targets: []*marketfetchpb.MarketFetchTargetResult{{
				WriteTargetId: "target-btc", DatasetId: "bars", Status: "failed", ErrorSummary: "storage unavailable",
			}},
		}, completedAt)
		payload.BatchId = batch.BatchID
		payload.ScheduleId = batch.ScheduleID
		require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)))
	}
	completeTargetWriteFailure(batch, completedAt)

	retry, err := db.FetchRetries().Get(ctx, "crypto", retryKey)
	require.NoError(t, err)
	require.Equal(t, 1, retry.Attempt, "the initial write failure schedules retry 1")
	require.NotNil(t, retry.PeriodTime)
	require.Equal(t, periodTime, *retry.PeriodTime)
	require.NotNil(t, retry.PeriodDeadlineAt)
	require.Equal(t, periodDeadline, *retry.PeriodDeadlineAt)
	for retryNumber := 1; retryNumber <= 3; retryNumber++ {
		require.NoError(t, db.FetchRetries().MarkStatus(ctx, "crypto", retryKey, "dispatched"))
		batch = completionTestBatch(fmt.Sprintf("write-target-retry-%d", retryNumber))
		batch.RetryScope = "write_target"
		batch.WriteTargetID = "target-btc"
		var retryRequest Request
		require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &retryRequest))
		retryRequest.Items[0].TargetDataTime = targetDataTime
		retryRequest.Items[0].SourceEventID = retryKey
		retryRequest.Targets = request.Targets
		retryJSON, marshalErr := json.Marshal(retryRequest)
		require.NoError(t, marshalErr)
		batch.RequestJSON = string(retryJSON)
		created, err = db.FetchBatches().CreatePlanned(ctx, &batch)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, db.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"task-btc"}))
		completeTargetWriteFailure(batch, completedAt.Add(time.Duration(retryNumber)*time.Minute))
		retry, err = db.FetchRetries().Get(ctx, "crypto", retryKey)
		require.NoError(t, err)
		if retryNumber < 3 {
			assert.Equal(t, retryNumber+1, retry.Attempt)
			assert.Equal(t, "pending", retry.Status)
		} else {
			assert.Equal(t, 3, retry.Attempt)
			assert.Equal(t, "permanent_failed", retry.Status)
			assert.Equal(t, "storage", retry.LastErrorType)
			assert.Equal(t, "storage unavailable", retry.LastErrorSummary)
		}
	}
	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus, "the provider fetch succeeded even though its destination exhausted retries")
	target, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-btc")
	require.NoError(t, err)
	assert.Equal(t, "failed", target.Status)
	assert.Equal(t, 3, target.Attempt)
	var failureTargets []domain.WriteTarget
	require.NoError(t, json.Unmarshal([]byte(retry.FailureTargetsJSON), &failureTargets))
	require.Len(t, failureTargets, 1)
	assert.Equal(t, "bars", failureTargets[0].DatasetID)
}

func TestHandleCompletionScopesWriteTargetRetriesToPeriod(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	firstPeriod := "2026-08-02T07:59:00Z"
	secondPeriod := "2026-08-02T08:00:00Z"
	completedAt := time.Date(2026, time.August, 2, 8, 5, 0, 0, time.UTC)
	target := domain.WriteTarget{
		ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars",
		SeriesIndex: 0, SeriesHash: "bars-hash", ExpectedCount: 1,
	}
	initialized := false

	completeFailedPeriod := func(batchID, period string, at time.Time) {
		batch := completionTestBatch(batchID)
		var request Request
		require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
		request.Items[0].TargetDataTime = period
		request.Targets = []domain.WriteTarget{target}
		requestJSON, err := json.Marshal(request)
		require.NoError(t, err)
		batch.RequestJSON = string(requestJSON)
		created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
		require.NoError(t, err)
		require.True(t, created)
		if !initialized {
			persistCompletionTestInstance(t, db, ctx, batch.BatchID)
			initialized = true
		} else {
			require.NoError(t, db.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"task-btc"}))
		}
		payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
			InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: period,
			Outcome: string(domain.ItemOutcomeSuccess),
			Targets: []*marketfetchpb.MarketFetchTargetResult{{
				WriteTargetId: target.ID, DatasetId: target.DatasetID, Status: "failed", ErrorSummary: "storage unavailable",
			}},
		}, at)
		payload.BatchId = batch.BatchID
		payload.ScheduleId = batch.ScheduleID
		require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)))
	}

	firstKey := writeTargetRetryKey("task-btc", target.ID, firstPeriod)
	secondKey := writeTargetRetryKey("task-btc", target.ID, secondPeriod)
	assert.Equal(t, firstKey, writeTargetRetryKey("task-btc", target.ID, "2026-08-02T09:59:00+02:00"), "equivalent timestamp offsets must share a retry identity")
	assert.NotEqual(t, firstKey, secondKey, "different target periods must get distinct retry identities")
	completeFailedPeriod("period-one", firstPeriod, completedAt)
	firstRetry, err := db.FetchRetries().Get(ctx, "crypto", firstKey)
	require.NoError(t, err)
	require.Equal(t, "pending", firstRetry.Status)
	require.NoError(t, db.FetchRetries().MarkStatus(ctx, "crypto", firstKey, "succeeded"))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: target.ID, SpaceID: target.SpaceID, InstanceID: target.InstanceID, TaskID: target.TaskID,
		DatasetID: target.DatasetID, SeriesIndex: target.SeriesIndex, SeriesHash: target.SeriesHash,
		ExpectedCount: target.ExpectedCount, Status: "succeeded",
	}}))

	completeFailedPeriod("period-two", secondPeriod, completedAt.Add(time.Minute))
	secondRetry, err := db.FetchRetries().Get(ctx, "crypto", secondKey)
	require.NoError(t, err, "a successful retry for an older bar must not suppress a later period's retry")
	assert.Equal(t, "pending", secondRetry.Status)
	assert.Equal(t, secondPeriod, secondRetry.TargetDataTime.UTC().Format(time.RFC3339))
	firstRetry, err = db.FetchRetries().Get(ctx, "crypto", firstKey)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", firstRetry.Status)
}

func TestConfiguredRetryBudgetCannotExceedThreeRetries(t *testing.T) {
	t.Setenv("MOOX_FETCH_MAX_RETRY_ATTEMPTS", "9")
	assert.Equal(t, 3, maxRetryAttempts())
	t.Setenv("MOOX_FETCH_MAX_RETRY_ATTEMPTS", "2")
	assert.Equal(t, 2, maxRetryAttempts())
}

func TestHandleCompletionPreservesLogicalSyncPointAcrossRetryGenerations(t *testing.T) {
	t.Setenv("MOOX_FETCH_MAX_RETRY_ATTEMPTS", "5")
	db := newCompletionTestStore(t)
	ctx := context.Background()
	completedAt := time.Date(2026, time.August, 2, 8, 6, 0, 0, time.UTC)
	const retryID = "batch-b0|BTC-USDT|2026-08-02T07:59:00Z"
	persistCompletionTestInstance(t, db, ctx)
	for _, batchID := range []string{"b0", "b1"} {
		batch := completionTestBatch(batchID)
		batch.BatchKind = domain.BatchKindCatchup
		var request Request
		require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
		request.Items[0].TargetDataTime = "2026-08-02T07:59:00Z"
		if batchID == "b1" {
			request.Items[0].SourceEventID = retryID
		}
		requestJSON, marshalErr := json.Marshal(request)
		require.NoError(t, marshalErr)
		batch.RequestJSON = string(requestJSON)
		created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, db.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{"task-btc"}))
		payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
			InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: "2026-08-02T07:59:00Z",
			SourceEventId: func() string {
				if batchID == "b0" {
					return ""
				}
				return retryID
			}(),
			Outcome: string(domain.ItemOutcomeHTTP429), ErrorType: "rate_limit", ErrorSummary: "too many requests",
		}, completedAt)
		payload.BatchId = batch.BatchID
		payload.ScheduleId = batch.ScheduleID
		payload.BatchKind = string(domain.BatchKindCatchup)
		require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)))
		completedAt = completedAt.Add(time.Minute)
	}
	retry, err := db.FetchRetries().Get(ctx, "crypto", retryID)
	require.NoError(t, err)
	assert.Equal(t, "batch-b0", retry.SourceBatchID)
}

func TestHandleCompletionKeepsSuccessfulTaskInstanceSuccessful(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	batch := completionTestBatch("success")
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, batch.BatchID)
	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", Outcome: string(domain.ItemOutcomeSuccess)}, time.Date(2026, time.August, 2, 8, 10, 0, 0, time.UTC))
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
	olderTarget := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	newerTarget := olderTarget.Add(time.Minute)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "old-retry", InstanceID: "task-btc", RetryScope: "fetch", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: olderTarget,
		Attempt: 3, Status: "dispatched", CreateTime: olderTarget,
	}))
	olderBatch := completionTestBatch("old-retry")
	newerBatch := completionTestBatch("new-success")
	for _, batch := range []*domain.BatchInvocation{&olderBatch, &newerBatch} {
		var request Request
		require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
		if batch == &olderBatch {
			request.Items[0].TargetDataTime = olderTarget.Format(time.RFC3339Nano)
			request.Items[0].SourceEventID = "old-retry"
		} else {
			request.Items[0].TargetDataTime = newerTarget.Format(time.RFC3339Nano)
		}
		request.Targets = []domain.WriteTarget{{
			ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars",
		}}
		requestJSON, marshalErr := json.Marshal(request)
		require.NoError(t, marshalErr)
		batch.RequestJSON = string(requestJSON)
	}
	created, err := db.FetchBatches().CreatePlanned(ctx, &olderBatch)
	require.NoError(t, err)
	require.True(t, created)
	created, err = db.FetchBatches().CreatePlanned(ctx, &newerBatch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, olderBatch.BatchID, newerBatch.BatchID)

	newerCompletedAt := newerTarget.Add(time.Minute)
	newerPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: newerTarget.Format(time.RFC3339Nano), Outcome: string(domain.ItemOutcomeSuccess),
		Targets: []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-btc", DatasetId: "bars", Status: "succeeded"}},
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
	target, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-btc")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", target.Status)

	olderPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: olderTarget.Format(time.RFC3339Nano), SourceEventId: "old-retry",
		Outcome: string(domain.ItemOutcomeInvalid), ErrorType: "invalid_symbol", ErrorSummary: "symbol is delisted",
		Targets: []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-btc", DatasetId: "bars", Status: "failed", ErrorSummary: "legacy retry failed"}},
	}, newerCompletedAt.Add(time.Minute))
	olderPayload.BatchId = olderBatch.BatchID
	olderPayload.ScheduleId = olderBatch.ScheduleID
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(olderPayload)))

	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
	assert.Equal(t, newerCompletedAt, instance.LastExecTime.UTC())
	target, err = db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-btc")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", target.Status, "a superseded retry completion must not regress the newer successful write target")
}

func TestHandleCompletionDoesNotRegressRetrySuccessWithLatePermanentFailure(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 2, 8, 10, 0, 0, time.UTC)
	const retryKey = "retry-key"

	oldBatch := completionTestBatch("timed-out-old-attempt")
	oldBatch.Attempt = 1
	newerBatch := completionTestBatch("newer-retry-attempt")
	newerBatch.Attempt = 2
	for _, batch := range []*domain.BatchInvocation{&oldBatch, &newerBatch} {
		var request Request
		require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
		request.Items[0].SourceEventID = retryKey
		request.Items[0].TargetDataTime = now.Add(-time.Minute).Format(time.RFC3339Nano)
		request.Targets = []domain.WriteTarget{{ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars"}}
		requestJSON, marshalErr := json.Marshal(request)
		require.NoError(t, marshalErr)
		batch.RequestJSON = string(requestJSON)
	}
	created, err := db.FetchBatches().CreatePlanned(ctx, &oldBatch)
	require.NoError(t, err)
	require.True(t, created)
	created, err = db.FetchBatches().CreatePlanned(ctx, &newerBatch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, oldBatch.BatchID, newerBatch.BatchID)

	completedAt := now.Add(-time.Minute)
	timedOut := oldBatch
	timedOut.Status = domain.BatchStatusTimedOut
	timedOut.CompletedAt = &completedAt
	updated, err := db.FetchBatches().Complete(ctx, &timedOut)
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: retryKey, InstanceID: "task-btc", Status: "dispatched", Attempt: 1, CreateTime: now,
	}))
	newerPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano),
		SourceEventId: retryKey, Outcome: string(domain.ItemOutcomeSuccess),
		Targets: []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-btc", DatasetId: "bars", Status: "succeeded"}},
	}, now)
	newerPayload.BatchId = newerBatch.BatchID
	newerPayload.ScheduleId = newerBatch.ScheduleID
	newerPayload.Status = string(domain.BatchStatusSucceeded)
	newerPayload.SuccessCount = 1
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(newerPayload)))
	storedRetry, err := db.FetchRetries().Get(ctx, "crypto", retryKey)
	require.NoError(t, err)
	require.Equal(t, "succeeded", storedRetry.Status)

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano),
		SourceEventId: retryKey, Outcome: string(domain.ItemOutcomeInvalid), ErrorType: "invalid_symbol", ErrorSummary: "stale old attempt",
		Targets: []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-btc", DatasetId: "bars", Status: "failed", ErrorSummary: "stale old attempt"}},
	}, now.Add(time.Second))
	payload.BatchId = oldBatch.BatchID
	payload.ScheduleId = oldBatch.ScheduleID
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)))

	retry, err := db.FetchRetries().Get(ctx, "crypto", retryKey)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", retry.Status)
	target, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-btc")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", target.Status)
	instance, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
}

func TestHandleCompletionDoesNotRegressNewSuccessWithOlderRealtimeFailure(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	olderTarget := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	newerTarget := olderTarget.Add(time.Minute)
	olderBatch := completionTestBatch("old-realtime")
	newerBatch := completionTestBatch("new-realtime")
	for _, item := range []struct {
		batch *domain.BatchInvocation
		at    time.Time
	}{{&olderBatch, olderTarget}, {&newerBatch, newerTarget}} {
		var request Request
		require.NoError(t, json.Unmarshal([]byte(item.batch.RequestJSON), &request))
		request.Items[0].TargetDataTime = item.at.Format(time.RFC3339Nano)
		requestJSON, marshalErr := json.Marshal(request)
		require.NoError(t, marshalErr)
		item.batch.RequestJSON = string(requestJSON)
	}
	created, err := db.FetchBatches().CreatePlanned(ctx, &olderBatch)
	require.NoError(t, err)
	require.True(t, created)
	created, err = db.FetchBatches().CreatePlanned(ctx, &newerBatch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, olderBatch.BatchID, newerBatch.BatchID)

	newerCompletedAt := newerTarget.Add(time.Minute)
	newerPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: newerTarget.Format(time.RFC3339Nano), Outcome: string(domain.ItemOutcomeSuccess),
	}, newerCompletedAt)
	newerPayload.BatchId = newerBatch.BatchID
	newerPayload.ScheduleId = newerBatch.ScheduleID
	newerPayload.Status = string(domain.BatchStatusSucceeded)
	newerPayload.SuccessCount = 1
	newerPayload.PermanentFailedCount = 0
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(newerPayload)))

	olderPayload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: olderTarget.Format(time.RFC3339Nano),
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

func TestCompletionAcceptsFailoverNodeForSameBatch(t *testing.T) {
	batch := completionTestBatch("failover")
	payload := completionTestPayload(nil, time.Now().UTC())
	payload.BatchId = batch.BatchID
	payload.ScheduleId = batch.ScheduleID
	payload.BatchKind = string(batch.BatchKind)
	payload.DatasetId = "bars"
	payload.Frequency = batch.Frequency
	payload.NodeId = "node-failover"
	payload.PlannedCount = int32(batch.PlannedCount)

	assert.Empty(t, completionIdentityMismatch(&batch, payload))
}

func TestHandleCompletionRejectsForgedTimerClaimIdentityWithoutSideEffects(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	period := now
	task := timerPlannerTask()
	require.NoError(t, db.Tasks().Create(ctx, task))
	plan := timerPlannerPlan(task, "timer-run", period)
	planner := TimerPeriodPlanner{
		Snapshots: db.PeriodSeriesSnapshot(), States: db.PeriodStorageStates(), Batches: db.TimerPeriodBatches(),
		Now: func() time.Time { return now },
		EnsureStorage: func(_ context.Context, snapshot domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			return timerPlannerStorageState(snapshot, now.Add(time.Hour)), nil
		},
	}
	created, err := planner.Plan(ctx, plan)
	require.NoError(t, err)
	require.True(t, created)
	manifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, plan.Snapshot.Key, 0)
	require.NoError(t, err)
	claimID := "timer-claim-id"
	bindingHash := timerPeriodBindingHash(plan.Assignment)
	claim, err := db.TimerPeriodBatches().Claim(ctx, store.TimerPeriodBatchClaimInput{
		SpaceID: "crypto", FunctionName: "market-fetch", RequestID: claimID, GroupID: 0, GroupCount: 1,
		BindingHash: bindingHash, TickTime: now.Add(time.Second), NodeID: "timer-node", Region: "ap-singapore", CompletionTimeout: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claim.Claimed)

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: plan.Request.Items[0].InstanceID, SubjectId: "BTC-USDT", Outcome: string(domain.ItemOutcomeSuccess),
	}, now.Add(2*time.Second))
	payload.BatchId = manifest.BatchID
	payload.ScheduleId = "timer:" + stableID("crypto", "bars", "1m", period.Format(time.RFC3339Nano), "0")
	payload.BatchKind = string(domain.BatchKindRealtime)
	payload.Frequency = "1m"
	payload.NodeId = "forged-node"
	payload.RequestId = "forged-request"

	err = handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload))
	require.ErrorIs(t, err, errCompletionIdentityMismatch)
	// A valid request and node must not allow a completion from another region
	// to apply to this durably claimed Timer batch.
	payload.RequestId = claimID
	payload.NodeId = manifest.NodeID
	payload.Region = "ap-tokyo"
	err = handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload))
	require.ErrorIs(t, err, errCompletionIdentityMismatch)
	batch, err := db.FetchBatches().Get(ctx, "crypto", manifest.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusDispatched, batch.Status)
	target, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-old")
	require.NoError(t, err)
	require.Equal(t, "pending", target.Status)
}

func TestRetryCollectionItemAdvancesProviderCandidateWindow(t *testing.T) {
	request := Request{DatasetID: "bars", Frequency: "1m", Provider: "binance", MarketType: "spot", Items: []domain.CollectionItem{{
		InstanceID: "task-btc", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DatasetID: "bars", CandidateIndex: 1,
	}}}
	item := retryCollectionItem(request, &marketfetchpb.MarketFetchItemResult{InstanceId: "task-btc", SubjectId: "BTC-USDT"}, "retry-key")
	require.Equal(t, 1+klineProviderAttemptBudget, item.CandidateIndex)
	require.Equal(t, "retry-key", item.SourceEventID)
}

func TestRetryCollectionItemKeepsProviderCandidateOnStorageFailure(t *testing.T) {
	request := Request{DatasetID: "bars", Frequency: "1m", Provider: "binance", MarketType: "spot", Items: []domain.CollectionItem{{
		InstanceID: "task-btc", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DatasetID: "bars", CandidateIndex: 1,
	}}}
	item := retryCollectionItem(request, &marketfetchpb.MarketFetchItemResult{InstanceId: "task-btc", SubjectId: "BTC-USDT", Outcome: string(domain.ItemOutcomeStorageError)}, "storage-retry-key")
	require.Equal(t, 1, item.CandidateIndex)
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
	request, _ := json.Marshal(Request{
		BatchID: "batch-" + suffix, ScheduleID: "schedule-" + suffix, BatchKind: domain.BatchKindRealtime,
		SpaceID: "crypto", DatasetID: "bars", Frequency: "1m",
		Items:   []domain.CollectionItem{{InstanceID: "task-btc", SubjectID: "BTC-USDT", DatasetID: "bars", Frequency: "1m"}},
		Targets: []domain.WriteTarget{{ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars"}},
	})
	return domain.BatchInvocation{SpaceID: "crypto", BatchID: "batch-" + suffix, ScheduleID: "schedule-" + suffix, BatchKind: domain.BatchKindRealtime, DatasetID: "bars", Frequency: "1m", NodeID: "node-1", Status: domain.BatchStatusPlanned, PlannedCount: 1, RequestJSON: string(request)}
}

func completionTestInstance() domain.TaskInstance {
	return domain.TaskInstance{SpaceID: "crypto", InstanceID: "task-btc", Provider: "binance", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
}

func persistCompletionTestInstance(t *testing.T, db *store.Store, ctx context.Context, batchIDs ...string) {
	t.Helper()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule", TaskName: "Rule", DataType: "kline", Enabled: true}))
	instance := completionTestInstance()
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: "target-btc", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "rule", DatasetID: "bars", Status: "pending",
	}}))
	for _, batchID := range batchIDs {
		require.NoError(t, db.FetchBatches().UpsertItems(ctx, "crypto", batchID, []string{instance.InstanceID}))
	}
}

func completionTestPayload(item *marketfetchpb.MarketFetchItemResult, completedAt time.Time) *marketfetchpb.MarketFetchBatchCompleted {
	payload := &marketfetchpb.MarketFetchBatchCompleted{ScheduleId: "schedule", BatchKind: string(domain.BatchKindRealtime), DatasetId: "bars", Frequency: "1m", NodeId: "node-1", PlannedCount: 1, CompletedAt: timestamppb.New(completedAt), Items: []*marketfetchpb.MarketFetchItemResult{item}}
	switch domain.ItemOutcome(item.GetOutcome()) {
	case domain.ItemOutcomeSuccess:
		payload.Status = string(domain.BatchStatusSucceeded)
		payload.SuccessCount = 1
		if len(item.GetTargets()) == 0 {
			item.Targets = []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-btc", DatasetId: "bars", Status: "succeeded"}}
		}
	case domain.ItemOutcomeHTTP429, domain.ItemOutcomeHTTP5xx, domain.ItemOutcomeNetworkError, domain.ItemOutcomeStorageError, domain.ItemOutcomeProviderError:
		payload.Status = string(domain.BatchStatusFailed)
		payload.RetryCount = 1
	default:
		payload.Status = string(domain.BatchStatusFailed)
		payload.PermanentFailedCount = 1
	}
	return payload
}

func completionTestDelivery(payload *marketfetchpb.MarketFetchBatchCompleted) *events.EventDelivery {
	return &events.EventDelivery{Message: &eventpb.EventMessage{SpaceId: "crypto"}, Payload: payload}
}

func TestHandleCompletionRejectsUnknownBatchWithoutSideEffects(t *testing.T) {
	db := newCompletionTestStore(t)
	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Outcome: string(domain.ItemOutcomeSuccess),
	}, time.Now().UTC())
	payload.BatchId = "missing-batch"
	payload.ScheduleId = "missing-schedule"

	err := handleCompletion(context.Background(), db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload))
	require.ErrorIs(t, err, errUnknownCompletionBatch)
	_, retryErr := db.FetchRetries().Get(context.Background(), "crypto", retryKey(payload.GetBatchId(), "BTC-USDT", payload.GetItems()[0].GetTargetDataTime()))
	require.Error(t, retryErr)
}

func TestHandleCompletionRejectsEnvelopeIdentityMismatchWithoutSideEffects(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	batch := completionTestBatch("identity-mismatch")
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "task-btc", SubjectId: "BTC-USDT", Outcome: string(domain.ItemOutcomeSuccess),
	}, time.Now().UTC())
	payload.BatchId = batch.BatchID
	payload.ScheduleId = "forged-schedule"
	payload.Frequency = batch.Frequency

	err = handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload))
	require.ErrorIs(t, err, errCompletionIdentityMismatch)
	stored, getErr := db.FetchBatches().Get(ctx, "crypto", batch.BatchID)
	require.NoError(t, getErr)
	require.Equal(t, domain.BatchStatusPlanned, stored.Status)
}

func TestHandleCompletionRejectsFieldsOutsideFrozenRequest(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	batch := completionTestBatch("frozen-item-mismatch")
	var request Request
	require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
	request.Items[0].TargetDataTime = "2026-08-02T07:59:00Z"
	request.Items[0].SourceEventID = "frozen-source-event"
	request.Targets = []domain.WriteTarget{{ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars"}}
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	batch.RequestJSON = string(encoded)
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, batch.BatchID)
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule-other", TaskName: "Other rule", DataType: "kline", Enabled: true}))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{
		ID: "target-other", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule-other", DatasetID: "other-bars", Status: "pending",
	}}))

	for _, mutate := range []func(*marketfetchpb.MarketFetchItemResult){
		func(item *marketfetchpb.MarketFetchItemResult) { item.TargetDataTime = "2026-08-02T08:00:00Z" },
		func(item *marketfetchpb.MarketFetchItemResult) { item.SourceEventId = "forged-source-event" },
		func(item *marketfetchpb.MarketFetchItemResult) {
			item.Targets = []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-other", DatasetId: "other-bars", Status: "failed", ErrorSummary: "forged"}}
		},
	} {
		payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
			InstanceId: "task-btc", SubjectId: "BTC-USDT", TargetDataTime: request.Items[0].TargetDataTime,
			SourceEventId: request.Items[0].SourceEventID, Outcome: string(domain.ItemOutcomeSuccess),
			Targets: []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-btc", DatasetId: "bars", Status: "succeeded"}},
		}, time.Now().UTC())
		payload.BatchId = batch.BatchID
		payload.ScheduleId = batch.ScheduleID
		mutate(payload.Items[0])
		require.ErrorIs(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload)), errCompletionIdentityMismatch)
	}

	storedBatch, err := db.FetchBatches().Get(ctx, "crypto", batch.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, storedBatch.Status)
	for _, targetID := range []string{"target-btc", "target-other"} {
		target, targetErr := db.TaskInstances().GetWriteTarget(ctx, "crypto", targetID)
		require.NoError(t, targetErr)
		require.Equal(t, "pending", target.Status)
	}
	_, err = db.FetchRetries().Get(ctx, "crypto", "forged-source-event")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestHandleCompletionRejectsInvalidStatusCountsAndMissingTargetReceipt(t *testing.T) {
	mutations := map[string]func(*marketfetchpb.MarketFetchBatchCompleted){
		"nonterminal status": func(payload *marketfetchpb.MarketFetchBatchCompleted) {
			payload.Status = string(domain.BatchStatusDispatched)
		},
		"inconsistent counts": func(payload *marketfetchpb.MarketFetchBatchCompleted) {
			payload.SuccessCount = 0
		},
		"missing frozen target receipt": func(payload *marketfetchpb.MarketFetchBatchCompleted) {
			payload.Items[0].Targets = nil
		},
		"unknown target status": func(payload *marketfetchpb.MarketFetchBatchCompleted) {
			payload.Items[0].Targets[0].Status = "unknown"
		},
		"unknown item outcome": func(payload *marketfetchpb.MarketFetchBatchCompleted) {
			payload.Items[0].Outcome = "unknown"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			db := newCompletionTestStore(t)
			ctx := context.Background()
			batch := completionTestBatch("invalid-contract-" + stableID(name))
			created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
			require.NoError(t, err)
			require.True(t, created)
			persistCompletionTestInstance(t, db, ctx, batch.BatchID)

			payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
				InstanceId: "task-btc", SubjectId: "BTC-USDT", Outcome: string(domain.ItemOutcomeSuccess),
				Targets: []*marketfetchpb.MarketFetchTargetResult{{WriteTargetId: "target-btc", DatasetId: "bars", Status: "succeeded"}},
			}, time.Now().UTC())
			payload.BatchId = batch.BatchID
			payload.ScheduleId = batch.ScheduleID
			payload.Status = string(domain.BatchStatusSucceeded)
			payload.SuccessCount = 1
			payload.RetryCount = 0
			payload.PermanentFailedCount = 0
			mutate(payload)

			err = handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload))
			require.ErrorIs(t, err, errCompletionIdentityMismatch)
			stored, err := db.FetchBatches().Get(ctx, "crypto", batch.BatchID)
			require.NoError(t, err)
			require.Equal(t, domain.BatchStatusPlanned, stored.Status)
			target, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-btc")
			require.NoError(t, err)
			require.Equal(t, "pending", target.Status)
		})
	}
}

func TestHandleCompletionRejectsItemNotInOriginalRequestWithoutSideEffects(t *testing.T) {
	db := newCompletionTestStore(t)
	ctx := context.Background()
	batch := completionTestBatch("item-mismatch")
	created, err := db.FetchBatches().CreatePlanned(ctx, &batch)
	require.NoError(t, err)
	require.True(t, created)
	persistCompletionTestInstance(t, db, ctx, batch.BatchID)

	payload := completionTestPayload(&marketfetchpb.MarketFetchItemResult{
		InstanceId: "forged-instance", SubjectId: "BTC-USDT", Outcome: string(domain.ItemOutcomeSuccess),
	}, time.Now().UTC())
	payload.BatchId = batch.BatchID
	payload.ScheduleId = batch.ScheduleID
	payload.Frequency = batch.Frequency

	err = handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(payload))
	require.ErrorIs(t, err, errCompletionIdentityMismatch)
	stored, getErr := db.FetchBatches().Get(ctx, "crypto", batch.BatchID)
	require.NoError(t, getErr)
	require.Equal(t, domain.BatchStatusPlanned, stored.Status)
	target, targetErr := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-btc")
	require.NoError(t, targetErr)
	require.Equal(t, "pending", target.Status)
}
