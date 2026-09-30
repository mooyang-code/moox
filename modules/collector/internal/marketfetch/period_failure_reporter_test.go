package marketfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type periodFailureRecorderStub struct {
	calls       int
	ensureCalls int
	ensured     *storagepb.DatasetPeriodExpectation
	last        *storagepb.DatasetPeriodExpectation
	lastIndexes []uint32
	allIndexes  []uint32
	err         error
}

func (*periodFailureRecorderStub) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}

func (s *periodFailureRecorderStub) EnsureDatasetPeriod(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	s.ensureCalls++
	s.ensured = expectation
	return testPeriodStorageStateFromExpectation(expectation), nil
}

func (s *periodFailureRecorderStub) GetDatasetPeriodStatus(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	return testPeriodStorageStateFromExpectation(expectation), nil
}

func (*periodFailureRecorderStub) CommitTimeSeriesBatch(context.Context, *storagepb.DatasetPeriodExpectation, []*storagepb.TimeSeriesBatchRow, string) error {
	return nil
}

func TestEnsureDatasetPeriodDeadlineCoversThreeRetriesAndFailureReport(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	period := now.Truncate(time.Minute)
	recorder := &periodFailureRecorderStub{}
	scheduler := &Scheduler{
		StorageTarget: "storage",
		Storage:       func(string, string, string) (Storage, error) { return recorder, nil },
	}
	err := scheduler.ensureDatasetPeriod(context.Background(), domain.CollectionTask{SpaceID: "crypto", TaskID: "bars-task"}, []domain.CollectionItem{{
		DatasetID: "bars", MarketType: "spot", SubjectID: "ETH-USDT", SeriesIndex: 0, SeriesHash: "bars-hash", ExpectedCount: 1,
	}}, "1m", period, now)
	require.NoError(t, err)
	require.Equal(t, 1, recorder.ensureCalls)
	requiredWindow := 5*batchCompletionDeadline(domain.BatchKindRealtime) + retryDelay(1) + retryDelay(2) + retryDelay(3)
	require.GreaterOrEqual(t, time.Duration(recorder.ensured.GetDeadlineAt()-now.Unix())*time.Second, requiredWindow,
		"period must remain waiting through four bounded SCF invocations, all retry delays, and a failure report window")
}

func (s *periodFailureRecorderStub) RecordDatasetPeriodFailures(_ context.Context, expectation *storagepb.DatasetPeriodExpectation, indexes []uint32) error {
	s.calls++
	s.last = expectation
	s.lastIndexes = append([]uint32(nil), indexes...)
	s.allIndexes = append(s.allIndexes, indexes...)
	return s.err
}

func testPeriodStorageStateFromExpectation(expectation *storagepb.DatasetPeriodExpectation) domain.PeriodStorageState {
	return domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: expectation.GetSpaceId(), DatasetID: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(), PeriodTime: time.Unix(expectation.GetPeriodTime(), 0).UTC()},
		SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(), DeadlineAt: time.Unix(expectation.GetDeadlineAt(), 0).UTC(),
		Status: domain.PeriodStatusWaiting, ConfirmedAt: time.Now().UTC(),
	}
}

func TestReportPendingPeriodFailuresDrainsLargePeriodBeforeFinalization(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	const failureCount = 125
	for index := 0; index < failureCount; index++ {
		instanceID := fmt.Sprintf("instance-%03d", index)
		itemJSON, err := json.Marshal(domain.CollectionItem{
			InstanceID: instanceID, SubjectID: fmt.Sprintf("SUBJECT-%03d", index), Frequency: "1m", MarketType: "spot",
			TargetDataTime: period.Format(time.RFC3339Nano),
		})
		require.NoError(t, err)
		targetsJSON, err := json.Marshal([]domain.WriteTarget{{
			ID: "target-" + instanceID, SpaceID: "crypto", InstanceID: instanceID, DatasetID: "bars",
			SeriesIndex: uint32(index), SeriesHash: "bars-hash", ExpectedCount: failureCount,
		}})
		require.NoError(t, err)
		require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
			SpaceID: "crypto", RetryKey: "retry-" + instanceID, InstanceID: instanceID, SubjectID: fmt.Sprintf("SUBJECT-%03d", index),
			Frequency: "1m", TargetDataTime: period, TaskJSON: string(itemJSON), FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
		}))
	}

	recorder := &periodFailureRecorderStub{}
	scheduler := &Scheduler{
		Retries: db.FetchRetries(), StorageTarget: "storage",
		Storage: func(string, string, string) (Storage, error) { return recorder, nil },
	}
	require.NoError(t, scheduler.reportPendingPeriodFailures(ctx, "crypto"))
	require.Len(t, recorder.allIndexes, failureCount)
	require.Equal(t, failureCount, len(uniqueUint32(recorder.allIndexes)), "every failed series index must be reported once")
	require.Equal(t, 1, recorder.calls, "failures sharing one period expectation must be batched into one RPC")

	unreported, err := db.FetchRetries().ListUnreportedPeriodFailures(ctx, "crypto", failureCount)
	require.NoError(t, err)
	require.Empty(t, unreported)
}

func TestSchedulerReportsPeriodFailuresBeforeCloudNodeLookup(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Now().UTC().Truncate(time.Minute)
	itemJSON, err := json.Marshal(domain.CollectionItem{
		InstanceID: "instance-eth", SubjectID: "ETH-USDT", Frequency: "1m", MarketType: "spot",
		TargetDataTime: period.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	targetsJSON, err := json.Marshal([]domain.WriteTarget{{
		ID: "target-eth", SpaceID: "crypto", InstanceID: "instance-eth", DatasetID: "bars",
		SeriesIndex: 0, SeriesHash: "bars-hash", ExpectedCount: 1,
	}})
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "retry-eth", InstanceID: "instance-eth", SubjectID: "ETH-USDT", Frequency: "1m",
		TargetDataTime: period, TaskJSON: string(itemJSON), FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
	}))
	recorder := &periodFailureRecorderStub{}
	scheduler := &Scheduler{
		SpaceID: "crypto", Tasks: db.Tasks(), Batches: db.FetchBatches(), Retries: db.FetchRetries(),
		Invoker: &recordingMarketFetchInvoker{err: errors.New("CloudNode unavailable")}, StorageTarget: "storage",
		Storage: func(string, string, string) (Storage, error) { return recorder, nil },
	}
	err = scheduler.Tick(ctx, "crypto")
	require.ErrorContains(t, err, "CloudNode unavailable")
	require.Equal(t, 1, recorder.calls, "period failure reporting must run before CloudNode lookup can return early")
	stored, err := db.FetchRetries().Get(ctx, "crypto", "retry-eth")
	require.NoError(t, err)
	require.True(t, stored.PeriodFailureReported)
}

func uniqueUint32(values []uint32) map[uint32]struct{} {
	unique := make(map[uint32]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}
	return unique
}

func TestReportPendingPeriodFailuresRetriesIdempotentlyAndDeduplicatesSeries(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	itemJSON, err := json.Marshal(domain.CollectionItem{
		InstanceID: "instance-eth", SubjectID: "ETH-USDT", Frequency: "1m", MarketType: "spot",
		TargetDataTime: period.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	targetsJSON, err := json.Marshal([]domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: "instance-eth", DatasetID: "bars", SeriesIndex: 1, SeriesHash: "bars-hash", ExpectedCount: 2},
		{ID: "target-b", SpaceID: "crypto", InstanceID: "instance-eth", DatasetID: "bars", SeriesIndex: 1, SeriesHash: "bars-hash", ExpectedCount: 2},
	})
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "retry-eth", InstanceID: "instance-eth", SubjectID: "ETH-USDT", Frequency: "1m",
		TargetDataTime: period, TaskJSON: string(itemJSON), FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
	}))

	recorder := &periodFailureRecorderStub{err: errors.New("Storage unavailable")}
	scheduler := &Scheduler{
		Retries: db.FetchRetries(), StorageTarget: "storage",
		Storage: func(string, string, string) (Storage, error) { return recorder, nil },
	}
	require.Error(t, scheduler.reportPendingPeriodFailures(ctx, "crypto"))
	stored, err := db.FetchRetries().Get(ctx, "crypto", "retry-eth")
	require.NoError(t, err)
	require.False(t, stored.PeriodFailureReported, "failed Storage calls must remain eligible for retry")
	require.Equal(t, 1, recorder.calls)
	require.Equal(t, []uint32{1}, recorder.lastIndexes, "duplicate write targets must not duplicate a series failure")
	require.Equal(t, "bars", recorder.last.GetDatasetId())
	require.Equal(t, "1m", recorder.last.GetFrequency())
	require.Equal(t, period.Unix(), recorder.last.GetPeriodTime())
	require.Equal(t, "bars-hash", recorder.last.GetSeriesHash())
	require.Equal(t, uint32(2), recorder.last.GetExpectedCount())

	recorder.err = nil
	require.NoError(t, scheduler.reportPendingPeriodFailures(ctx, "crypto"))
	stored, err = db.FetchRetries().Get(ctx, "crypto", "retry-eth")
	require.NoError(t, err)
	require.True(t, stored.PeriodFailureReported)
	require.Equal(t, 2, recorder.calls)
	require.NoError(t, scheduler.reportPendingPeriodFailures(ctx, "crypto"))
	require.Equal(t, 2, recorder.calls, "a reported permanent retry must not be sent again")
}
