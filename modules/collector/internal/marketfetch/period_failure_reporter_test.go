package marketfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type periodFailureStorageStub struct {
	calls int
	call  func(context.Context, *storagepb.DatasetPeriodExpectation, []uint32) ([]*storagepb.DatasetPeriodFailureResult, error)
}

func (*periodFailureStorageStub) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}

func (s *periodFailureStorageStub) RecordDatasetPeriodFailures(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
	s.calls++
	if s.call == nil {
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED), nil
	}
	return s.call(ctx, expectation, indexes)
}

func TestEnsureDatasetPeriodDeadlineCoversThreeRetriesAndFailureReport(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	period := now.Truncate(time.Minute)
	recorder := &recordingPeriodFailureStorage{}
	scheduler := &Scheduler{
		StorageTarget: "storage",
		Storage:       func(string, string, string) (Storage, error) { return recorder, nil },
	}
	err := scheduler.ensureDatasetPeriod(context.Background(), domain.CollectionTask{SpaceID: "crypto", TaskID: "bars-task"}, []domain.CollectionItem{{
		DatasetID: "bars", MarketType: "spot", SubjectID: "ETH-USDT", SeriesIndex: 0, SeriesHash: "bars-hash", ExpectedCount: 1,
	}}, "1m", period, now)
	require.NoError(t, err)
	require.Len(t, recorder.ensured, 1)
	requiredWindow := 5*batchCompletionDeadline(domain.BatchKindRealtime) + retryDelay(1) + retryDelay(2) + retryDelay(3) + periodFailureReportingSlack
	require.GreaterOrEqual(t, recorder.ensured[0].GetDeadlineAt()-now.Unix(), int64(requiredWindow/time.Second),
		"period must remain waiting through four bounded SCF invocations, all retry delays, and the independent failure reporter window")
}

func TestPeriodFailureReportEmptyReceiptStaysPending(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	retry := insertPermanentFailure(t, db, "retry-empty", period, []domain.WriteTarget{failureTarget("target-a", "bars", 0, 1)})
	storage := &periodFailureStorageStub{call: func(context.Context, *storagepb.DatasetPeriodExpectation, []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		return nil, nil
	}}
	reporter := newFailureReporter(db, storage)
	reporter.budget = time.Second
	require.Error(t, reporter.RunOnce(ctx, "crypto"), "nil error without an exact receipt is not an acknowledgement")
	stored, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState)
	require.NotEmpty(t, stored.PeriodFailureLastError)
}

func TestPeriodFailureReportLostAckReplaysAndResumesAfterRestart(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Now().UTC().Truncate(time.Minute)
	target := failureTarget("target-a", "bars", 0, 1)
	retry := insertPermanentFailure(t, db, "retry-lost-ack", period, []domain.WriteTarget{target})
	now := period.Add(time.Second)
	require.NoError(t, db.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: period.Add(time.Minute), Status: domain.PeriodStatusWaiting, ConfirmedAt: now,
	}))
	var calls int
	storage := &periodFailureStorageStub{call: func(_ context.Context, _ *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("Storage applied write; response lost")
		}
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED), nil
	}}
	firstReporter := newFailureReporter(db, storage)
	firstReporter.budget = time.Second
	firstReporter.now = func() time.Time { return now }
	require.Error(t, firstReporter.RunOnce(ctx, "crypto"))
	stored, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState)
	require.Nil(t, stored.PeriodFailureDeadlineExceededAt, "before the canonical Storage deadline the unknown transport outcome is retryable without a local terminal cutoff")

	// A process restart creates a fresh reporter and scans the durable outbox.
	restarted := newFailureReporter(db, storage)
	restarted.budget = time.Second
	restarted.now = func() time.Time { return now.Add(time.Second) }
	require.NoError(t, restarted.RunOnce(ctx, "crypto"))
	stored, err = db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportAcknowledged, stored.PeriodFailureReportState)
	require.Equal(t, 2, calls)
}

func TestPeriodFailureReportPastDeadlineRemainsRetryableUntilStorageReceipt(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Minute)
	target := failureTarget("target-a", "bars", 0, 1)
	retry := insertPermanentFailure(t, db, "retry-past-deadline", period, []domain.WriteTarget{target})
	require.NoError(t, db.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: period.Add(time.Minute), ConfirmedAt: period.Add(time.Second), Status: domain.PeriodStatusWaiting,
	}))
	var calls int
	storage := &periodFailureStorageStub{call: func(_ context.Context, _ *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("Storage unavailable after deadline")
		}
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE), nil
	}}
	first := newFailureReporter(db, storage)
	first.budget = time.Second
	first.now = func() time.Time { return time.Now().UTC() }
	require.Error(t, first.RunOnce(ctx, "crypto"))
	pending, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, pending.PeriodFailureReportState)
	require.NotNil(t, pending.PeriodFailureDeadlineExceededAt)
	deadlineExceededAt := *pending.PeriodFailureDeadlineExceededAt

	restarted := newFailureReporter(db, storage)
	restarted.budget = time.Second
	require.NoError(t, restarted.RunOnce(ctx, "crypto"))
	settled, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportMissedDeadline, settled.PeriodFailureReportState)
	require.Equal(t, deadlineExceededAt, *settled.PeriodFailureDeadlineExceededAt, "the first deadline observation is durable and immutable")
}

func TestPeriodFailureReportPersistsPartialRPCResults(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	targets := []domain.WriteTarget{failureTarget("target-a", "bars-a", 0, 1), failureTarget("target-b", "bars-b", 0, 1)}
	retry := insertPermanentFailure(t, db, "retry-partial", period, targets)
	storage := &periodFailureStorageStub{call: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		if expectation.GetDatasetId() == "bars-b" {
			return nil, errors.New("Storage partition unavailable")
		}
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED), nil
	}}
	reporter := newFailureReporter(db, storage)
	reporter.budget = time.Second
	require.Error(t, reporter.RunOnce(ctx, "crypto"))
	stored, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState)
	var results []domain.PeriodFailureTargetResult
	require.NoError(t, json.Unmarshal([]byte(stored.PeriodFailureResultsJSON), &results))
	require.Len(t, results, 1)
	require.Equal(t, "target-a", results[0].WriteTargetID)
	require.Equal(t, "recorded", results[0].Disposition)
	require.Contains(t, stored.PeriodFailureLastError, "Storage partition unavailable")
}

func TestPeriodFailureReportMapsSharedSeriesIndexToEveryWriteTarget(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Now().UTC().Truncate(time.Minute)
	targetA := failureTarget("target-a", "bars", 0, 1)
	targetB := failureTarget("target-b", "bars", 0, 1)
	retry := insertPermanentFailure(t, db, "retry-shared-index", period, []domain.WriteTarget{targetA, targetB})
	var gotIndexes []uint32
	storage := &periodFailureStorageStub{call: func(_ context.Context, _ *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		gotIndexes = append([]uint32(nil), indexes...)
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED), nil
	}}
	reporter := newFailureReporter(db, storage)
	reporter.budget = time.Second
	require.NoError(t, reporter.RunOnce(ctx, "crypto"))
	require.Equal(t, []uint32{0}, gotIndexes, "a shared Storage series index is reported once")
	stored, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportAcknowledged, stored.PeriodFailureReportState)
	var results []domain.PeriodFailureTargetResult
	require.NoError(t, json.Unmarshal([]byte(stored.PeriodFailureResultsJSON), &results))
	require.Len(t, results, 2, "the one Storage receipt must fan out to each durable WriteTarget")
	require.Equal(t, []string{"target-a", "target-b"}, []string{results[0].WriteTargetID, results[1].WriteTargetID})
	require.Equal(t, uint32(0), results[0].SeriesIndex)
	require.Equal(t, uint32(0), results[1].SeriesIndex)
	require.Equal(t, "recorded", results[0].Disposition)
	require.Equal(t, "recorded", results[1].Disposition)
}

func TestPeriodFailureReportPagesAndContinuesAfterFirstTimeout(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	for i := 0; i < 125; i++ {
		key := fmt.Sprintf("retry-%03d", i)
		dataset := "bars"
		if i < 3 {
			dataset = fmt.Sprintf("bars-%03d", i)
		}
		insertPermanentFailure(t, db, key, period, []domain.WriteTarget{failureTarget("target-"+key, dataset, 0, 1)})
	}
	storage := &periodFailureStorageStub{call: func(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		if expectation.GetDatasetId() == "bars-000" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED), nil
	}}
	reporter := newFailureReporter(db, storage)
	reporter.pageSize = 25
	reporter.budget = 3 * time.Second
	reporter.rpcTimeout = 25 * time.Millisecond
	require.Error(t, reporter.RunOnce(ctx, "crypto"), "first per-row timeout should be surfaced after other retry keys are attempted")
	require.Equal(t, 125, storage.calls, "the reporter must traverse more than one retry-key page")
	first, err := db.FetchRetries().Get(ctx, "crypto", "retry-000")
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, first.PeriodFailureReportState)
	second, err := db.FetchRetries().Get(ctx, "crypto", "retry-001")
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportAcknowledged, second.PeriodFailureReportState, "a timed-out first row must not starve later rows")
}

func TestPeriodFailureReportRoundBudgetBoundsWork(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("retry-%03d", i)
		dataset := fmt.Sprintf("bars-%03d", i)
		insertPermanentFailure(t, db, key, period, []domain.WriteTarget{failureTarget("target-"+key, dataset, 0, 1)})
	}
	var calledDatasets []string
	storage := &periodFailureStorageStub{call: func(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		calledDatasets = append(calledDatasets, expectation.GetDatasetId())
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED), nil
	}}
	reporter := newFailureReporter(db, storage)
	applyStarted := make(chan struct{})
	reporter.retries = &oneShotBlockingReportApplyStore{FetchRetryRepository: db.FetchRetries(), applyStarted: applyStarted}
	reporter.budget = 250 * time.Millisecond
	registry := prometheus.NewRegistry()
	reporter.SetMetrics(NewMetrics(registry))
	started := time.Now()
	require.Error(t, reporter.RunOnce(ctx, "crypto"))
	select {
	case <-applyStarted:
	case <-time.After(time.Second):
		t.Fatal("reporter did not reach receipt persistence")
	}
	require.Less(t, time.Since(started), time.Second, "one round must stay inside its shared budget")
	require.Equal(t, 1, storage.calls, "budget expiration during receipt persistence should leave the rest of the short page unattempted")
	pending, err := db.FetchRetries().ListPendingPeriodFailuresAfter(ctx, "crypto", "", 100)
	require.NoError(t, err)
	require.NotEmpty(t, pending)
	families, err := registry.Gather()
	require.NoError(t, err)
	metric := metricFamily(t, families, "moox_collector_period_failure_pending")
	require.Len(t, metric.GetMetric(), 1)
	require.Equal(t, map[string]string{"space_id": "crypto", "frequency": "1m"}, metricLabels(metric.GetMetric()[0]))
	require.Equal(t, float64(50), metric.GetMetric()[0].GetGauge().GetValue(), "pending gauges refresh even after the RPC round's own budget expires")
	firstRoundCalls := len(calledDatasets)
	require.Greater(t, firstRoundCalls, 0)
	require.Less(t, firstRoundCalls, 50)

	reporter.budget = 2 * time.Second
	require.NoError(t, reporter.RunOnce(ctx, "crypto"))
	require.GreaterOrEqual(t, len(calledDatasets), firstRoundCalls+2)
	require.Equal(t, "bars-001", calledDatasets[firstRoundCalls], "the next round must resume after the attempted prefix, not restart at its first key")
	require.Equal(t, "bars-049", calledDatasets[len(calledDatasets)-1], "later rows must remain reachable after a budget-limited short page")
}

type oneShotBlockingReportApplyStore struct {
	*store.FetchRetryRepository
	applyStarted chan struct{}
	blocked      bool
}

func (s *oneShotBlockingReportApplyStore) ApplyPeriodFailureReportResults(ctx context.Context, spaceID, retryKey string, results []domain.PeriodFailureTargetResult, lastError string) error {
	if !s.blocked {
		s.blocked = true
		close(s.applyStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	return s.FetchRetryRepository.ApplyPeriodFailureReportResults(ctx, spaceID, retryKey, results, lastError)
}

func TestPeriodFailureReportMetricsQueryStaysInsideRoundBudget(t *testing.T) {
	countStarted := make(chan struct{})
	retries := &blockingMetricCountStore{countStarted: countStarted}
	reporter := NewPeriodFailureReporter(nil, func(string, string, string) (Storage, error) { return nil, nil }, "storage", "crypto")
	reporter.retries = retries
	reporter.budget = 300 * time.Millisecond
	reporter.SetMetrics(NewMetrics(prometheus.NewRegistry()))
	started := time.Now()
	require.Error(t, reporter.RunOnce(context.Background(), "crypto"), "the bounded metrics query should report its own timeout")
	select {
	case <-countStarted:
	case <-time.After(time.Second):
		t.Fatal("pending metric query did not start")
	}
	require.LessOrEqual(t, time.Since(started), reporter.budget+150*time.Millisecond, "outbox work and metric refresh share one total round deadline")
}

type blockingMetricCountStore struct {
	countStarted chan struct{}
}

func (*blockingMetricCountStore) ListPendingPeriodFailuresAfter(context.Context, string, string, int) ([]domain.RetryItem, error) {
	return nil, nil
}

func (*blockingMetricCountStore) ApplyPeriodFailureReportResults(context.Context, string, string, []domain.PeriodFailureTargetResult, string) error {
	return nil
}

func (s *blockingMetricCountStore) CountPendingPeriodFailuresByFrequency(ctx context.Context, _ string) (map[string]int64, error) {
	close(s.countStarted)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPeriodFailureReportLoopDoesNotDependOnCloudNode(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	insertPermanentFailure(t, db, "retry-no-node", period, []domain.WriteTarget{failureTarget("target-a", "bars", 0, 1)})
	storage := &periodFailureStorageStub{}
	reporter := newFailureReporter(db, storage)
	reporter.budget = time.Second
	require.NoError(t, reporter.RunOnce(ctx, "crypto"), "the independent receipt loop has no CloudNode dependency")
	require.Equal(t, 1, storage.calls)
}

func TestPeriodFailureReporterWakeDrainsNewDurableFailureImmediately(t *testing.T) {
	db := newTestMarketFetchStore(t)
	called := make(chan struct{}, 1)
	storage := &periodFailureStorageStub{call: func(_ context.Context, _ *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
		select {
		case called <- struct{}{}:
		default:
		}
		return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED), nil
	}}
	reporter := newFailureReporter(db, storage)
	firstListDone := make(chan struct{})
	reporter.retries = &wakeSignalingRetryRepository{FetchRetryRepository: db.FetchRetries(), firstListDone: firstListDone}
	reporter.budget = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, StartPeriodFailureReporter(ctx, reporter, "crypto", time.Hour))
	select {
	case <-firstListDone:
	case <-time.After(time.Second):
		t.Fatal("reporter did not complete its initial empty outbox scan")
	}
	retry := permanentFailureRetry(t, "retry-woken", time.Now().UTC().Truncate(time.Minute), []domain.WriteTarget{failureTarget("target-a", "bars", 0, 1)})
	require.NoError(t, db.FetchRetries().Upsert(context.Background(), &retry))
	reporter.Wake()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("reporter did not wake for the newly committed permanent failure")
	}
	require.Eventually(t, func() bool {
		stored, err := db.FetchRetries().Get(context.Background(), "crypto", "retry-woken")
		return err == nil && stored.PeriodFailureReportState == domain.PeriodFailureReportAcknowledged
	}, time.Second, 10*time.Millisecond, "wake must drain the durable outbox row")
}

type wakeSignalingRetryRepository struct {
	*store.FetchRetryRepository
	firstListDone chan struct{}
	listCalls     int
}

func (r *wakeSignalingRetryRepository) ListPendingPeriodFailuresAfter(ctx context.Context, spaceID, after string, limit int) ([]domain.RetryItem, error) {
	items, err := r.FetchRetryRepository.ListPendingPeriodFailuresAfter(ctx, spaceID, after, limit)
	r.listCalls++
	if r.listCalls == 1 {
		close(r.firstListDone)
	}
	return items, err
}

func newFailureReporter(db *store.Store, storage *periodFailureStorageStub) *PeriodFailureReporter {
	return NewPeriodFailureReporter(db.FetchRetries(), func(string, string, string) (Storage, error) { return storage, nil }, "storage", "crypto")
}

func insertPermanentFailure(t *testing.T, db *store.Store, retryKey string, period time.Time, targets []domain.WriteTarget) domain.RetryItem {
	t.Helper()
	retry := permanentFailureRetry(t, retryKey, period, targets)
	require.NoError(t, db.FetchRetries().Upsert(context.Background(), &retry))
	return retry
}

func permanentFailureRetry(t *testing.T, retryKey string, period time.Time, targets []domain.WriteTarget) domain.RetryItem {
	t.Helper()
	targetsJSON, err := json.Marshal(targets)
	require.NoError(t, err)
	item := domain.CollectionItem{InstanceID: "instance-" + retryKey, SubjectID: "BTC-USDT", Frequency: "1m", MarketType: "spot", TargetDataTime: period.Format(time.RFC3339Nano)}
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	return domain.RetryItem{
		SpaceID: "crypto", RetryKey: retryKey, InstanceID: item.InstanceID, SubjectID: item.SubjectID, Frequency: "1m",
		TargetDataTime: period, TaskJSON: string(itemJSON), FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
	}
}

func failureTarget(id, dataset string, index, expected uint32) domain.WriteTarget {
	return domain.WriteTarget{ID: id, SpaceID: "crypto", DatasetID: dataset, SeriesIndex: index, SeriesHash: "hash", ExpectedCount: expected}
}

func recordedFailureResults(indexes []uint32, disposition storagepb.PeriodFailureDisposition) []*storagepb.DatasetPeriodFailureResult {
	results := make([]*storagepb.DatasetPeriodFailureResult, 0, len(indexes))
	for _, index := range indexes {
		results = append(results, &storagepb.DatasetPeriodFailureResult{SeriesIndex: index, Disposition: disposition})
	}
	return results
}

func testPeriodStorageStateFromExpectation(expectation *storagepb.DatasetPeriodExpectation) domain.PeriodStorageState {
	return domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: expectation.GetSpaceId(), DatasetID: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(), PeriodTime: time.Unix(expectation.GetPeriodTime(), 0).UTC()},
		SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(), DeadlineAt: time.Unix(expectation.GetDeadlineAt(), 0).UTC(),
		Status: domain.PeriodStatusWaiting, ConfirmedAt: time.Now().UTC(),
	}
}

var _ periodFailureReceiptStorage = (*periodFailureStorageStub)(nil)
