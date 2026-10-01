package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestApplyPeriodFailureReportResultsMergesPerTargetAndResumes(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	targets := []domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 2},
		{ID: "target-b", SpaceID: "crypto", DatasetID: "bars", SeriesIndex: 1, SeriesHash: "hash", ExpectedCount: 2},
	}
	targetsJSON, err := json.Marshal(targets)
	require.NoError(t, err)
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "retry-a", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: period,
		FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
	}))

	resultA := domain.PeriodFailureTargetResult{WriteTargetID: "target-a", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period, SeriesHash: "hash", ExpectedCount: 2, SeriesIndex: 0, Disposition: "recorded", ObservedAt: period}
	resultB := resultA
	resultB.WriteTargetID, resultB.SeriesIndex, resultB.Disposition = "target-b", 1, "missed_deadline"

	// A transport error after the Storage write is an unknown result, so the
	// durable receipt remains pending and can be replayed after process restart.
	require.NoError(t, s.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", "retry-a", nil, "Storage ACK lost"))
	stored, err := s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState)

	require.NoError(t, s.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", "retry-a", []domain.PeriodFailureTargetResult{resultA}, ""))
	stored, err = s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState, "another unconfirmed WriteTarget must keep aggregate state pending")
	require.Empty(t, stored.PeriodFailureLastError)

	// Re-open the durable repository view, then settle the second target with
	// Storage's explicit missed-deadline disposition.
	require.NoError(t, s.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", "retry-a", []domain.PeriodFailureTargetResult{resultB}, ""))
	stored, err = s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportMissedDeadline, stored.PeriodFailureReportState)

	// A successful retry clears the stale failure receipt rather than carrying
	// it into later cleanup/reconciliation.
	require.NoError(t, s.FetchRetries().MarkStatus(ctx, "crypto", "retry-a", "succeeded"))
	stored, err = s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState)
	require.Equal(t, "[]", stored.PeriodFailureResultsJSON)
}

func TestApplyPeriodFailureReportResultsRejectsUnknownTarget(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	targetsJSON, err := json.Marshal([]domain.WriteTarget{{ID: "target-a", SpaceID: "crypto", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 1}})
	require.NoError(t, err)
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: "retry-a", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: period, FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed"}))

	err = s.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", "retry-a", []domain.PeriodFailureTargetResult{{
		WriteTargetID: "other-target", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period, SeriesHash: "hash", ExpectedCount: 1, Disposition: "recorded",
	}}, "")
	require.ErrorContains(t, err, "unknown write_target_id")
	stored, getErr := s.FetchRetries().Get(ctx, "crypto", "retry-a")
	require.NoError(t, getErr)
	require.Equal(t, domain.PeriodFailureReportPending, stored.PeriodFailureReportState)
	require.Equal(t, "[]", stored.PeriodFailureResultsJSON)
}

func TestApplyPeriodFailureReportResultsFindsCanonicalHourlyDeadline(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	period := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	target := domain.WriteTarget{ID: "hourly-target", SpaceID: "crypto", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hourly-hash", ExpectedCount: 1}
	targetsJSON, err := json.Marshal([]domain.WriteTarget{target})
	require.NoError(t, err)
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "hourly-retry", SubjectID: "BTC-USDT", Frequency: "1H", TargetDataTime: period,
		FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
	}))
	deadline := period.Add(time.Minute)
	require.NoError(t, s.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1H", PeriodTime: period},
		SeriesHash: "hourly-hash", ExpectedCount: 1, DeadlineAt: deadline, Status: domain.PeriodStatusWaiting, ConfirmedAt: period.Add(time.Second),
	}))

	result := domain.PeriodFailureTargetResult{
		WriteTargetID: target.ID, SpaceID: "crypto", DatasetID: "bars", Frequency: "1H", PeriodTime: period,
		SeriesHash: target.SeriesHash, ExpectedCount: target.ExpectedCount, SeriesIndex: target.SeriesIndex,
		Disposition: "missed_deadline", ObservedAt: time.Now().UTC(),
	}
	require.NoError(t, s.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", "hourly-retry", []domain.PeriodFailureTargetResult{result}, ""))
	stored, err := s.FetchRetries().Get(ctx, "crypto", "hourly-retry")
	require.NoError(t, err)
	require.NotNil(t, stored.PeriodFailureDeadlineExceededAt, "canonical 1H state must be found when recording a missed deadline")
}

func TestApplyPeriodFailureReportResultsKeepsFirstAuthoritativeTargetDisposition(t *testing.T) {
	for _, test := range []struct {
		name      string
		first     string
		replay    string
		wantState domain.PeriodFailureReportState
		wantFinal string
	}{
		{name: "accepted cannot regress to missed deadline", first: "recorded", replay: "missed_deadline", wantState: domain.PeriodFailureReportAcknowledged, wantFinal: "recorded"},
		{name: "missed deadline cannot regress to accepted", first: "missed_deadline", replay: "already_succeeded", wantState: domain.PeriodFailureReportMissedDeadline, wantFinal: "missed_deadline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newCollectorStore(t)
			ctx := context.Background()
			period := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
			target := domain.WriteTarget{ID: "target-a", SpaceID: "crypto", DatasetID: "bars", SeriesIndex: 0, SeriesHash: "hash", ExpectedCount: 1}
			targetsJSON, err := json.Marshal([]domain.WriteTarget{target})
			require.NoError(t, err)
			require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{
				SpaceID: "crypto", RetryKey: "retry-a", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: period,
				FailureTargetsJSON: string(targetsJSON), Status: "permanent_failed",
			}))
			apply := func(disposition string, observedAt time.Time) {
				err := s.FetchRetries().ApplyPeriodFailureReportResults(ctx, "crypto", "retry-a", []domain.PeriodFailureTargetResult{{
					WriteTargetID: target.ID, SpaceID: "crypto", DatasetID: target.DatasetID, Frequency: "1m", PeriodTime: period,
					SeriesHash: target.SeriesHash, ExpectedCount: target.ExpectedCount, SeriesIndex: target.SeriesIndex, Disposition: disposition, ObservedAt: observedAt,
				}}, "")
				require.NoError(t, err)
			}
			apply(test.first, period)
			apply(test.replay, period.Add(time.Second))

			stored, err := s.FetchRetries().Get(ctx, "crypto", "retry-a")
			require.NoError(t, err)
			require.Equal(t, test.wantState, stored.PeriodFailureReportState)
			var results []domain.PeriodFailureTargetResult
			require.NoError(t, json.Unmarshal([]byte(stored.PeriodFailureResultsJSON), &results))
			require.Len(t, results, 1)
			require.Equal(t, test.wantFinal, results[0].Disposition)
			require.Equal(t, period, results[0].ObservedAt, "the first authoritative receipt remains the durable evidence")
		})
	}
}
