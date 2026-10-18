package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestPeriodReadinessFinalizesCompleteAndKeepsPayloadInputs(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	_, err := repo.EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute),
		Tasks:      []domain.PeriodTaskSeed{{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "fetch-1", RequiredFields: `["close"]`}},
	})
	require.NoError(t, err)
	require.NoError(t, repo.MarkSubjectSuccess(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, "BTC-USDT", "fetch-1", "fetch-1", period.Add(10*time.Second)))

	reports, err := repo.FinalizeDue(ctx, period.Add(20*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, domain.PeriodStatusComplete, reports[0].Readiness.Status)
	require.Equal(t, domain.PeriodReportPending, reports[0].Readiness.ReportState)
	require.Equal(t, period.Add(20*time.Second), reports[0].Readiness.CollectedAt)
	require.Len(t, reports[0].Items, 1)
	require.Equal(t, domain.PeriodItemSuccess, reports[0].Items[0].State)

	// A second finalize does not create another report or move its fixed time.
	reports, err = repo.FinalizeDue(ctx, period.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
}

func TestPeriodReadinessDeadlineMarksPendingDegraded(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	_, err := repo.EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{
			{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "fetch-1"},
			{InstanceID: "task-eth", WriteTargetID: "task-eth", SubjectID: "ETH-USDT", FunctionName: "fetch-1", WriteSource: "fetch-1"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, repo.MarkSubjectSuccess(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, "BTC-USDT", "fetch-1", "fetch-1", period.Add(10*time.Second)))
	reports, err := repo.FinalizeDue(ctx, period.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, domain.PeriodStatusDegraded, reports[0].Readiness.Status)
	var timedOut bool
	for _, item := range reports[0].Items {
		if item.SubjectID == "ETH-USDT" {
			timedOut = item.State == domain.PeriodItemTimedOut
		}
	}
	require.True(t, timedOut)
}

func TestResampleReadinessSuppressesDeadlineUntilAllSubjectsSucceed(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	_, err := repo.EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_resample_5m", Frequency: "5m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute), WorkType: "resample",
		Tasks: []domain.PeriodTaskSeed{
			{InstanceID: "btc", WriteTargetID: "btc", SubjectID: "BTC", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"},
			{InstanceID: "eth", WriteTargetID: "eth", SubjectID: "ETH", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"},
		},
	})
	require.NoError(t, err)
	reports, err := repo.FinalizeDue(ctx, period.Add(2*time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
	var readiness domain.PeriodReadiness
	require.NoError(t, s.db.Where("c_dataset_id = ?", "dataset_spot_kline_resample_5m").First(&readiness).Error)
	require.Equal(t, domain.PeriodReportWaiting, readiness.ReportState)
	require.Equal(t, domain.PeriodStatusWaiting, readiness.Status)

	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_resample_5m", Frequency: "5m", PeriodTime: period}
	require.NoError(t, repo.MarkSubjectSuccess(ctx, key, "BTC", "collector_local_resample", "collector:kline_resample", period.Add(3*time.Minute)))
	reports, err = repo.FinalizeDue(ctx, period.Add(3*time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.NoError(t, repo.MarkSubjectSuccess(ctx, key, "ETH", "collector_local_resample", "collector:kline_resample", period.Add(3*time.Minute)))
	reports, err = repo.FinalizeDue(ctx, period.Add(4*time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, domain.PeriodStatusComplete, reports[0].Readiness.Status)
}

func TestResampleReadinessSuppressesTerminalFailedSource(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	_, err := repo.EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_resample_5m", Frequency: "5m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute), WorkType: "resample",
		Tasks: []domain.PeriodTaskSeed{{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"}},
	})
	require.NoError(t, err)
	failed := domain.NewResampleTaskResult(period)
	failed.State = domain.ResampleTaskStateFailed
	failed.LastError = "source Dataset retention expired"
	encoded, err := failed.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "task-btc", DataType: "kline_resample", SubjectID: "BTC", Frequency: "5m", Result: encoded}}))
	reports, err := repo.FinalizeDue(ctx, period.Add(2*time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
	var readiness domain.PeriodReadiness
	require.NoError(t, s.db.Where("c_period_time = ?", period).First(&readiness).Error)
	require.Equal(t, domain.PeriodReportReported, readiness.ReportState)
	require.Equal(t, domain.PeriodStatusDegraded, readiness.Status)
}

func TestResampleReadinessSuppressesDeletedSourceTask(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	_, err := repo.EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_resample_5m", Frequency: "5m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute), WorkType: "resample",
		Tasks: []domain.PeriodTaskSeed{{InstanceID: "task-eth", WriteTargetID: "task-eth", SubjectID: "ETH", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"}},
	})
	require.NoError(t, err)
	result := domain.NewResampleTaskResult(period)
	raw, err := result.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "task-eth", DataType: "kline_resample", SubjectID: "ETH", Frequency: "5m", Result: raw}}))
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_instance_id = ?", "crypto", "task-eth").Update("c_is_deleted", true).Error)
	reports, err := repo.FinalizeDue(ctx, period.Add(2*time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
	var readiness domain.PeriodReadiness
	require.NoError(t, s.db.Where("c_period_time = ?", period).First(&readiness).Error)
	require.Equal(t, domain.PeriodReportReported, readiness.ReportState)
}

func TestPeriodReadinessAllowsSameSubjectAcrossSeries(t *testing.T) {
	s := newCollectorStore(t)
	period := time.Now().UTC().Truncate(time.Minute)
	_, err := s.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{
			{InstanceID: "task-a", WriteTargetID: "task-a", SubjectID: "BTC-USDT", SeriesTag: "venue:binance|market:spot|source:spot_http"},
			{InstanceID: "task-b", WriteTargetID: "task-b", SubjectID: "BTC-USDT", SeriesTag: "venue:okx|market:spot|source:spot_http"},
		},
	})
	require.NoError(t, err)
	var count int64
	require.NoError(t, s.db.Model(&domain.PeriodReadinessItem{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestPeriodReadinessWaitsForAllSeriesTags(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	_, err := repo.EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  key,
		DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{
			{InstanceID: "spot", WriteTargetID: "spot", SubjectID: "BTCUSDT", SeriesTag: "venue:binance"},
			{InstanceID: "perpetual", WriteTargetID: "perpetual", SubjectID: "BTCUSDT", SeriesTag: "venue:binance|market:perpetual"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, repo.MarkSubjectSuccess(ctx, domain.PeriodKey{SpaceID: key.SpaceID, DatasetID: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime, SeriesTag: "venue:binance"}, "BTCUSDT", "", "", period.Add(10*time.Second)))

	reports, err := repo.FinalizeDue(ctx, period.Add(20*time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
	var readiness domain.PeriodReadiness
	require.NoError(t, s.db.Where("c_dataset_id = ? AND c_period_time = ?", key.DatasetID, key.PeriodTime).First(&readiness).Error)
	require.Equal(t, domain.PeriodStatusWaiting, readiness.Status)

	require.NoError(t, repo.MarkSubjectSuccess(ctx, domain.PeriodKey{SpaceID: key.SpaceID, DatasetID: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime, SeriesTag: "venue:binance|market:perpetual"}, "BTCUSDT", "", "", period.Add(30*time.Second)))
	reports, err = repo.FinalizeDue(ctx, period.Add(40*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, domain.PeriodStatusComplete, reports[0].Readiness.Status)
	require.Len(t, reports[0].Items, 2)
}

func TestEnsurePeriodDoesNotExpandExistingSnapshot(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	seed := domain.PeriodSeed{PeriodKey: domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, DeadlineAt: period.Add(time.Minute), Tasks: []domain.PeriodTaskSeed{{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT"}}}
	_, err := repo.EnsurePeriod(ctx, seed)
	require.NoError(t, err)
	seed.Tasks = append(seed.Tasks, domain.PeriodTaskSeed{InstanceID: "task-eth", WriteTargetID: "task-eth", SubjectID: "ETH-USDT"})
	_, err = repo.EnsurePeriod(ctx, seed)
	require.NoError(t, err)
	reports, err := repo.FinalizeDue(ctx, period.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Len(t, reports[0].Items, 1)
	require.Equal(t, "BTC-USDT", reports[0].Items[0].SubjectID)
}

func TestDeleteReportedItemsOutsideWindowKeepsNewestPeriods(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		period := base.Add(time.Duration(i) * time.Minute)
		_, err := repo.EnsurePeriod(ctx, domain.PeriodSeed{
			PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
			DeadlineAt: period.Add(time.Minute),
			Tasks:      []domain.PeriodTaskSeed{{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT"}},
		})
		require.NoError(t, err)
		require.NoError(t, repo.MarkSubjectSuccess(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, "BTC-USDT", "", "", period))
		_, err = repo.FinalizeDue(ctx, period.Add(time.Minute), 10)
		require.NoError(t, err)
		var report domain.PeriodReadiness
		require.NoError(t, s.db.Where("c_period_time = ?", period).First(&report).Error)
		require.NoError(t, repo.PersistPayload(ctx, report.ID, `{"status":"complete"}`))
		require.NoError(t, repo.MarkReported(ctx, report.ID))
	}
	_, err := repo.CleanupReportedRetentionInSpace(ctx, "crypto", base.Add(-time.Hour), 1, 100)
	require.NoError(t, err)
	var items []domain.PeriodReadinessItem
	require.NoError(t, s.db.Find(&items).Error)
	require.Len(t, items, 1)
	var kept domain.PeriodReadiness
	require.NoError(t, s.db.First(&kept, "c_id = ?", items[0].ReadinessID).Error)
	require.Equal(t, base.Add(2*time.Minute), kept.PeriodTime.UTC())
}

func TestCleanupReportedRetentionInSpaceBoundsPhysicalRows(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour)

	createPeriod := func(space, dataset string, period, collected time.Time, reportState string, itemCount int) int64 {
		t.Helper()
		row := domain.PeriodReadiness{
			SpaceID: space, DatasetID: dataset, Frequency: "1m", PeriodTime: period,
			DeadlineAt: period.Add(time.Minute), CollectedAt: collected,
			Status: domain.PeriodStatusComplete, ReportState: reportState, PayloadJSON: `{"status":"complete"}`,
		}
		require.NoError(t, s.db.Create(&row).Error)
		items := make([]domain.PeriodReadinessItem, 0, itemCount)
		for index := 0; index < itemCount; index++ {
			items = append(items, domain.PeriodReadinessItem{
				ReadinessID: row.ID, InstanceID: "instance", WriteTargetID: fmt.Sprintf("target-%d", index),
				SubjectID: fmt.Sprintf("SUBJECT-%d", index), State: domain.PeriodItemSuccess, UpdatedAt: period,
			})
		}
		if len(items) > 0 {
			require.NoError(t, s.db.Create(&items).Error)
		}
		return row.ID
	}

	expiredID := createPeriod("crypto", "bars", cutoff.Add(-time.Minute), cutoff.Add(-time.Minute), domain.PeriodReportReported, 3)
	currentID := createPeriod("crypto", "bars", now.Add(-time.Minute), now, domain.PeriodReportReported, 2)
	pendingID := createPeriod("crypto", "bars", now.Add(-2*time.Minute), cutoff.Add(-time.Minute), domain.PeriodReportPending, 2)
	otherSpaceID := createPeriod("stockcn", "bars", cutoff.Add(-time.Minute), cutoff.Add(-time.Minute), domain.PeriodReportReported, 2)

	deleted, err := repo.CleanupReportedRetentionInSpace(ctx, "crypto", cutoff, 1, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)
	var expiredItems int64
	require.NoError(t, s.db.Model(&domain.PeriodReadinessItem{}).Where("c_readiness_id = ?", expiredID).Count(&expiredItems).Error)
	require.EqualValues(t, 1, expiredItems)
	require.NoError(t, s.db.First(&domain.PeriodReadiness{}, expiredID).Error)

	deleted, err = repo.CleanupReportedRetentionInSpace(ctx, "crypto", cutoff, 1, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)
	require.Error(t, s.db.First(&domain.PeriodReadiness{}, expiredID).Error)
	require.NoError(t, s.db.First(&domain.PeriodReadiness{}, currentID).Error)
	require.NoError(t, s.db.First(&domain.PeriodReadiness{}, pendingID).Error)
	require.NoError(t, s.db.First(&domain.PeriodReadiness{}, otherSpaceID).Error)
	var currentItems int64
	require.NoError(t, s.db.Model(&domain.PeriodReadinessItem{}).Where("c_readiness_id = ?", currentID).Count(&currentItems).Error)
	require.EqualValues(t, 2, currentItems)

	var plan []struct{ Detail string }
	require.NoError(t, s.db.Raw(`EXPLAIN QUERY PLAN
SELECT c_id FROM t_period_readiness
 WHERE c_space_id = ? AND c_report_state = ? AND c_collected_at < ?
 ORDER BY c_collected_at ASC, c_id ASC LIMIT 1`, "crypto", domain.PeriodReportReported, cutoff).Scan(&plan).Error)
	require.NotEmpty(t, plan)
	require.Contains(t, plan[0].Detail, "idx_period_readiness_retention")
}

func TestLatestCompletedPeriodUsesExactScopeAndCompletionOrder(t *testing.T) {
	s := newCollectorStore(t)
	base := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	rows := []domain.PeriodReadiness{
		{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: base, DeadlineAt: base.Add(time.Minute), Status: domain.PeriodStatusComplete, ReportState: domain.PeriodReportReported, WorkType: "collection"},
		{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: base.Add(time.Minute), DeadlineAt: base.Add(2 * time.Minute), Status: domain.PeriodStatusDegraded, ReportState: domain.PeriodReportReported, WorkType: "collection"},
		{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: base.Add(2 * time.Minute), DeadlineAt: base.Add(3 * time.Minute), Status: domain.PeriodStatusWaiting, ReportState: domain.PeriodReportWaiting, WorkType: "collection"},
		{SpaceID: "crypto", DatasetID: "other-bars", Frequency: "1m", PeriodTime: base.Add(3 * time.Minute), DeadlineAt: base.Add(4 * time.Minute), Status: domain.PeriodStatusComplete, ReportState: domain.PeriodReportReported, WorkType: "collection"},
		{SpaceID: "stockcn", DatasetID: "bars", Frequency: "1m", PeriodTime: base.Add(4 * time.Minute), DeadlineAt: base.Add(5 * time.Minute), Status: domain.PeriodStatusComplete, ReportState: domain.PeriodReportReported, WorkType: "collection"},
		{SpaceID: "crypto", DatasetID: "bars", Frequency: "5m", PeriodTime: base.Add(5 * time.Minute), DeadlineAt: base.Add(6 * time.Minute), Status: domain.PeriodStatusComplete, ReportState: domain.PeriodReportReported, WorkType: "collection"},
	}
	require.NoError(t, s.db.Create(&rows).Error)

	got, err := s.PeriodReadiness().LatestCompletedPeriod(context.Background(), "crypto", "bars", "1m")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, domain.PeriodStatusDegraded, got.Status)
	require.Equal(t, base.Add(time.Minute), got.PeriodTime.UTC())

	var plan []struct{ Detail string }
	require.NoError(t, s.db.Raw(`EXPLAIN QUERY PLAN SELECT c_id FROM t_period_readiness WHERE c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_status = ? ORDER BY c_period_time DESC, c_id DESC LIMIT 1`, "crypto", "bars", "1m", domain.PeriodStatusComplete).Scan(&plan).Error)
	require.NotEmpty(t, plan)
	require.Contains(t, plan[0].Detail, "idx_period_readiness_completed_scope")
}

func TestLatestCompletedPeriodReturnsEmptyWhenNoTerminalPeriod(t *testing.T) {
	s := newCollectorStore(t)
	period := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	row := domain.PeriodReadiness{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period, DeadlineAt: period.Add(time.Minute), Status: domain.PeriodStatusWaiting, ReportState: domain.PeriodReportWaiting, WorkType: "collection"}
	require.NoError(t, s.db.Create(&row).Error)

	got, err := s.PeriodReadiness().LatestCompletedPeriod(context.Background(), "crypto", "bars", "1m")
	require.NoError(t, err)
	require.Nil(t, got)
}
