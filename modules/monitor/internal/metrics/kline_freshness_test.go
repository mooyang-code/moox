package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestKlineFreshnessEvaluatorUsesActiveViewOutputAndInputDiagnostics(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewDatasetQuery(t, []viewDatasetTestSample{
		{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "BTC", Input: now.Add(-30 * time.Second), Output: now.Add(-10 * time.Minute), Commit: now.Add(-time.Second)},
	})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, "view_output_stale", reports[0].Reason)
	require.Equal(t, 1, reports[0].StaleCount)
	require.Equal(t, now.Add(-30*time.Second), reports[0].LatestInputTime)
	require.Contains(t, reports[0].Diagnostic, "latest_input_data_time")
}

func TestKlineFreshnessEvaluatorDoesNotAlertTransientHole(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewDatasetQuery(t, []viewDatasetTestSample{{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "BTC", Input: now.Add(-30 * time.Second), Output: now.Add(-90 * time.Second), Commit: now}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.True(t, reports[0].Success)
	require.Equal(t, "fresh", reports[0].Reason)
}

func TestKlineFreshnessEvaluatorSkipsMalformedAndFutureSeries(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewDatasetQuery(t, []viewDatasetTestSample{
		{RawLabels: `{"space_id":"crypto"}`, Output: now.Add(-time.Minute), Commit: now},
		{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "future", Output: now.Add(11 * time.Minute), Commit: now},
		{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "valid", Output: now.Add(-time.Minute), Commit: now},
	})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, 1, reports[0].ObservedCount)
	require.Equal(t, []string{"valid"}, reports[0].ObservedSubjects)
}

func TestKlineFreshnessEvaluatorPrefersNewestProducerObservation(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewDatasetQuery(t, []viewDatasetTestSample{
		{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "BTC", Output: now.Add(-10 * time.Minute), Commit: now.Add(-2 * time.Minute), ObservedAt: now.Add(-2 * time.Minute)},
		{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "BTC", Output: now.Add(-30 * time.Second), Commit: now, ObservedAt: now},
	})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.True(t, reports[0].Success)
	require.Equal(t, 1, reports[0].ObservedCount)
}

func TestKlineFreshnessEvaluatorIgnoresRetiredSubjects(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewDatasetQuery(t, []viewDatasetTestSample{
		{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "BTC", Output: now.Add(-time.Minute), Commit: now},
		{SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Subject: "OLD", Output: now.Add(-10 * time.Minute), Commit: now},
	})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "active"}, {SubjectId: "OLD", Status: "archived"}}}
	query.storage = NewStorageAdapter(nil, metadata, monconfig.MetricsStorageConfig{SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m"})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, 1, reports[0].ObservedCount)
	require.Equal(t, []string{"BTC"}, reports[0].ObservedSubjects)
	require.Equal(t, 1, reports[0].StaleCount)
	require.Equal(t, []string{"ETH"}, reports[0].StaleSubjects)
}

func TestKlineFreshnessEvaluatorSkipsStockClosedAndWarmup(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "stockcn", ViewID: "view", Frequency: "1m", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, StaleAfter: 10 * time.Minute}
	query := newViewDatasetQuery(t, []viewDatasetTestSample{{SpaceID: "stockcn", ViewID: "view", DatasetID: "dataset", Subject: "600000", Output: time.Date(2026, 9, 7, 7, 0, 0, 0, time.UTC), Commit: time.Date(2026, 9, 7, 7, 0, 0, 0, time.UTC)}})
	for now, reason := range map[time.Time]string{
		time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC):   "skipped_market_closed",
		time.Date(2026, 9, 8, 1, 30, 30, 0, time.UTC): "skipped_session_warmup",
	} {
		reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		require.True(t, reports[0].Skipped)
		require.Equal(t, reason, reports[0].Reason)
	}
}

type viewDatasetTestSample struct {
	SpaceID, ViewID, DatasetID, Subject string
	Input, Output, Commit               time.Time
	ObservedAt                          time.Time
	RawLabels                           string
}

func newViewDatasetQuery(t *testing.T, samples []viewDatasetTestSample) *QueryService {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	_, err = store.WithDatabase(mgr, func(db *gorm.DB) error {
		for index, sample := range samples {
			labels := sample.RawLabels
			if labels == "" {
				labelsMap := map[string]string{"space_id": sample.SpaceID, "view_id": sample.ViewID, "dataset_id": sample.DatasetID, "subject_id": sample.Subject, "freq": "1m", "series_tag": "default"}
				raw, marshalErr := json.Marshal(labelsMap)
				require.NoError(t, marshalErr)
				labels = string(raw)
			}
			observedAt := sample.ObservedAt
			if observedAt.IsZero() {
				observedAt = sample.Commit
			}
			rows := make([]MetricLatest, 0, 3)
			if !sample.Input.IsZero() {
				rows = append(rows, MetricLatest{SeriesID: fmt.Sprintf("input-%d", index), MetricName: ViewDatasetInputLastDataTimeMetric, LabelsJSON: labels, Value: float64(sample.Input.Unix()), ObservedAt: observedAt})
			}
			if !sample.Output.IsZero() {
				rows = append(rows, MetricLatest{SeriesID: fmt.Sprintf("output-%d", index), MetricName: ViewDatasetOutputLastDataTimeMetric, LabelsJSON: labels, Value: float64(sample.Output.Unix()), ObservedAt: observedAt})
			}
			if !sample.Commit.IsZero() {
				rows = append(rows, MetricLatest{SeriesID: fmt.Sprintf("commit-%d", index), MetricName: ViewDatasetOutputLastCommitTimestampMetric, LabelsJSON: labels, Value: float64(sample.Commit.Unix()), ObservedAt: observedAt})
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
