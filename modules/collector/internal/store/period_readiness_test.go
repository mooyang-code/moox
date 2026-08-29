package store

import (
	"context"
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
		Tasks:      []domain.PeriodTaskSeed{{TaskID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "fetch-1", RequiredFields: `["close"]`}},
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
			{TaskID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "fetch-1"},
			{TaskID: "task-eth", SubjectID: "ETH-USDT", FunctionName: "fetch-1", WriteSource: "fetch-1"},
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
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "spot_kline_resample_5m", Frequency: "5m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute), WorkType: "resample",
		Tasks: []domain.PeriodTaskSeed{
			{TaskID: "btc", SubjectID: "BTC", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"},
			{TaskID: "eth", SubjectID: "ETH", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"},
		},
	})
	require.NoError(t, err)
	reports, err := repo.FinalizeDue(ctx, period.Add(2*time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
	var readiness domain.PeriodReadiness
	require.NoError(t, s.db.Where("c_dataset_id = ?", "spot_kline_resample_5m").First(&readiness).Error)
	require.Equal(t, domain.PeriodReportWaiting, readiness.ReportState)
	require.Equal(t, domain.PeriodStatusWaiting, readiness.Status)

	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "spot_kline_resample_5m", Frequency: "5m", PeriodTime: period}
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
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "spot_kline_resample_5m", Frequency: "5m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute), WorkType: "resample",
		Tasks: []domain.PeriodTaskSeed{{TaskID: "task-btc", SubjectID: "BTC", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"}},
	})
	require.NoError(t, err)
	failed := domain.NewResampleTaskResult(period)
	failed.State = domain.ResampleTaskStateFailed
	failed.LastError = "source Dataset retention expired"
	encoded, err := failed.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", TaskID: "task-btc", RuleID: "rule-5m", DataType: "kline_resample", DatasetID: "spot_kline_resample_5m", SubjectID: "BTC", Frequency: "5m", Result: encoded}}))
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
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "spot_kline_resample_5m", Frequency: "5m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute), WorkType: "resample",
		Tasks: []domain.PeriodTaskSeed{{TaskID: "task-eth", SubjectID: "ETH", FunctionName: "collector_local_resample", WriteSource: "collector:kline_resample"}},
	})
	require.NoError(t, err)
	result := domain.NewResampleTaskResult(period)
	raw, err := result.Marshal()
	require.NoError(t, err)
	require.NoError(t, s.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", TaskID: "task-eth", RuleID: "rule-5m", DataType: "kline_resample", DatasetID: "spot_kline_resample_5m", SubjectID: "ETH", Frequency: "5m", Result: raw}}))
	require.NoError(t, s.db.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_task_id = ?", "crypto", "task-eth").Update("c_is_deleted", true).Error)
	reports, err := repo.FinalizeDue(ctx, period.Add(2*time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
	var readiness domain.PeriodReadiness
	require.NoError(t, s.db.Where("c_period_time = ?", period).First(&readiness).Error)
	require.Equal(t, domain.PeriodReportReported, readiness.ReportState)
}

func TestPeriodReadinessRejectsDuplicateSubject(t *testing.T) {
	s := newCollectorStore(t)
	_, err := s.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: time.Now().UTC()},
		DeadlineAt: time.Now().UTC().Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{
			{TaskID: "task-a", SubjectID: "BTC-USDT"},
			{TaskID: "task-b", SubjectID: "BTC-USDT"},
		},
	})
	require.ErrorContains(t, err, "duplicate period subject")
}

func TestEnsurePeriodDoesNotExpandExistingSnapshot(t *testing.T) {
	s := newCollectorStore(t)
	repo := s.PeriodReadiness()
	ctx := context.Background()
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	seed := domain.PeriodSeed{PeriodKey: domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, DeadlineAt: period.Add(time.Minute), Tasks: []domain.PeriodTaskSeed{{TaskID: "task-btc", SubjectID: "BTC-USDT"}}}
	_, err := repo.EnsurePeriod(ctx, seed)
	require.NoError(t, err)
	seed.Tasks = append(seed.Tasks, domain.PeriodTaskSeed{TaskID: "task-eth", SubjectID: "ETH-USDT"})
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
			Tasks:      []domain.PeriodTaskSeed{{TaskID: "task-btc", SubjectID: "BTC-USDT"}},
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
	_, err := repo.DeleteReportedItemsOutsideWindow(ctx, 1)
	require.NoError(t, err)
	var items []domain.PeriodReadinessItem
	require.NoError(t, s.db.Find(&items).Error)
	require.Len(t, items, 1)
	var kept domain.PeriodReadiness
	require.NoError(t, s.db.First(&kept, "c_id = ?", items[0].ReadinessID).Error)
	require.Equal(t, base.Add(2*time.Minute), kept.PeriodTime)
}
