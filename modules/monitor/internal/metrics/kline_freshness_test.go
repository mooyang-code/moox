package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestKlineFreshnessEvaluatorUsesBusinessDataTimeAndGroupsReports(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rules := []KlineFreshnessRule{
		{Enabled: true, Scope: KlineScopePrimary, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute},
		{Enabled: true, Scope: KlineScopeView, SpaceID: "crypto", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute},
	}
	query := newKlineQuery(t, []klineTestSample{
		{Scope: KlineScopePrimary, SpaceID: "crypto", Target: "dataset", Subject: "BTC", DataTime: now.Add(-30 * time.Second), CommitTime: now.Add(-time.Second)},
		{Scope: KlineScopeView, SpaceID: "crypto", Target: "view", Subject: "BTC", DataTime: now.Add(-10 * time.Minute), CommitTime: now.Add(-time.Second)},
	})
	evaluator := NewKlineFreshnessEvaluator(query, rules, 20)
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	sort.Slice(reports, func(i, j int) bool { return reports[i].Rule.Scope < reports[j].Rule.Scope })
	require.True(t, reports[0].Success)
	require.Equal(t, "fresh", reports[0].Reason)
	require.False(t, reports[1].Success)
	require.Equal(t, "business_data_stale", reports[1].Reason)
	require.Equal(t, 1, reports[1].StaleCount)
	require.Equal(t, 1, reports[1].ObservedCount)
	require.Equal(t, []string{"BTC"}, reports[1].StaleSubjects)
	require.Contains(t, reports[1].Diagnostic, "latest_commit_time")
}

func TestKlineFreshnessEvaluatorDoesNotAlertTransientHole(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	query := newKlineQuery(t, []klineTestSample{{
		Scope: KlineScopePrimary, SpaceID: "crypto", Target: "dataset", Subject: "BTC",
		DataTime: time.Date(2026, 9, 8, 10, 2, 0, 0, time.UTC), CommitTime: now,
	}})
	evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{{
		Enabled: true, Scope: KlineScopePrimary, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute,
	}}, 20)
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.True(t, reports[0].Success)
	require.Zero(t, reports[0].StaleCount)
}

func TestKlineFreshnessEvaluatorSkipsMalformedAndFutureSeries(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	query := newKlineQuery(t, []klineTestSample{
		{Scope: KlineScopePrimary, SpaceID: "crypto", Target: "dataset", Subject: "bad-label", DataTime: now.Add(-time.Minute), CommitTime: now, RawLabels: `{"space_id":"crypto"}`},
		{Scope: KlineScopePrimary, SpaceID: "crypto", Target: "dataset", Subject: "future", DataTime: now.Add(11 * time.Minute), CommitTime: now},
		{Scope: KlineScopePrimary, SpaceID: "crypto", Target: "dataset", Subject: "valid", DataTime: now.Add(-time.Minute), CommitTime: now},
	})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{{
		Enabled: true, Scope: KlineScopePrimary, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute,
	}}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, 1, reports[0].ObservedCount)
	require.Equal(t, []string{"valid"}, reports[0].ObservedSubjects)
}

func TestKlineFreshnessEvaluatorBoundsStaleSubjects(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	samples := make([]klineTestSample, 25)
	for index := range samples {
		samples[index] = klineTestSample{Scope: KlineScopePrimary, SpaceID: "crypto", Target: "dataset", Subject: fmt.Sprintf("subject-%02d", index), DataTime: now.Add(-time.Hour), CommitTime: now}
	}
	reports, err := NewKlineFreshnessEvaluator(newKlineQuery(t, samples), []KlineFreshnessRule{{
		Enabled: true, Scope: KlineScopePrimary, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 5 * time.Minute,
	}}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, 25, reports[0].StaleCount)
	require.Len(t, reports[0].StaleSubjects, 20)
	require.Equal(t, "subject-00", reports[0].StaleSubjects[0])
	require.Equal(t, "subject-19", reports[0].StaleSubjects[19])
}

func TestKlineFreshnessEvaluatorSkipsStockClosedAndHoliday(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, Scope: KlineScopePrimary, SpaceID: "stockcn", DatasetID: "dataset", Frequency: "1m", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, StaleAfter: 10 * time.Minute}
	sample := klineTestSample{Scope: KlineScopePrimary, SpaceID: "stockcn", Target: "dataset", Subject: "600000", DataTime: time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC), CommitTime: time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)}
	query := newKlineQuery(t, []klineTestSample{sample})
	for _, now := range []time.Time{
		time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC),  // 12:00 Asia/Shanghai
		time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC), // holiday
	} {
		reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		require.True(t, reports[0].Skipped)
		require.Contains(t, reports[0].Reason, "skipped_")
	}
}

type klineTestSample struct {
	Scope, SpaceID, Target, Subject string
	DataTime, CommitTime            time.Time
	RawLabels                       string
}

func newKlineQuery(t *testing.T, samples []klineTestSample) *QueryService {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	_, err = store.WithDatabase(mgr, func(db *gorm.DB) error {
		for index, sample := range samples {
			labels := sample.RawLabels
			if labels == "" {
				labelsMap := map[string]string{"space_id": sample.SpaceID, "subject_id": sample.Subject, "freq": "1m", "series_tag": "default"}
				if sample.Scope == KlineScopePrimary {
					labelsMap["dataset_id"] = sample.Target
				} else {
					labelsMap["view_id"] = sample.Target
				}
				raw, marshalErr := json.Marshal(labelsMap)
				require.NoError(t, marshalErr)
				labels = string(raw)
			}
			dataName, commitName := KlinePrimaryLastDataTimeMetric, KlinePrimaryLastCommitTimestampMetric
			if sample.Scope == KlineScopeView {
				dataName, commitName = KlineViewLastDataTimeMetric, KlineViewLastCommitTimestampMetric
			}
			rows := []MetricLatest{
				{SeriesID: fmt.Sprintf("data-%d", index), MetricName: dataName, LabelsJSON: labels, Value: float64(sample.DataTime.Unix()), ObservedAt: sample.CommitTime},
				{SeriesID: fmt.Sprintf("commit-%d", index), MetricName: commitName, LabelsJSON: labels, Value: float64(sample.CommitTime.Unix()), ObservedAt: sample.CommitTime},
			}
			if err := db.Create(&rows).Error; err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	return NewQueryService(metricMessageStoreFromDatabaseForTest(t, mgr), nil)
}

func metricMessageStoreFromDatabaseForTest(t *testing.T, mgr *store.Store) *MetricMessageStore {
	t.Helper()
	result, err := store.WithDatabase(mgr, NewMetricMessageStore)
	require.NoError(t, err)
	return result
}
