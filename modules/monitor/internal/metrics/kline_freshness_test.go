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

func TestKlineFreshnessEvaluatorFlagsStaleView(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: now.Add(-10 * time.Minute), Subjects: 3, ObservedAt: now}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, "business_data_stale", reports[0].Reason)
	require.Equal(t, 3, reports[0].StaleCount, "a stale View makes every subject stale")
}

func TestKlineFreshnessEvaluatorDoesNotAlertTransientHole(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: now.Add(-90 * time.Second), Subjects: 1, ObservedAt: now}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.True(t, reports[0].Success)
	require.Equal(t, "fresh", reports[0].Reason)
	require.Equal(t, 1, reports[0].ObservedCount)
}

func TestKlineFreshnessEvaluatorSkipsMalformedAndFutureSummaries(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{
		{RawLabels: `{"space_id":"crypto"}`, Latest: now.Add(-time.Minute), Subjects: 1, ObservedAt: now},
		{Latest: now.Add(11 * time.Minute), Subjects: 1, ObservedAt: now},
	})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "no_observation", reports[0].Reason)
}

func TestKlineFreshnessEvaluatorNamesLaggingSubjectsFromLatestSnapshotOnly(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), Subjects: 3, Lagging: 1, ObservedAt: now,
		LaggingSubjects: []viewSummaryLaggingSample{
			{Subject: "SOL", DataTime: now.Add(-20 * time.Minute), ObservedAt: now},
			{Subject: "ETH", DataTime: now.Add(-30 * time.Minute), ObservedAt: now.Add(-time.Minute)},
		},
	}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, 1, reports[0].StaleCount)
	require.Equal(t, []string{"SOL"}, reports[0].StaleSubjects, "ETH was named by an older snapshot and has caught up")
	require.Equal(t, now.Add(-20*time.Minute), reports[0].OldestDataTime)
}

func TestKlineFreshnessEvaluatorIgnoresRetiredSubjectsAndCountsMissing(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 2, Lagging: 1, ObservedAt: now,
		LaggingSubjects: []viewSummaryLaggingSample{{Subject: "OLD", DataTime: now.Add(-10 * time.Minute), ObservedAt: now}},
	}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "active"}, {SubjectId: "OLD", Status: "archived"}}}
	query.storage = NewStorageAdapter(nil, metadata, monconfig.MetricsStorageConfig{SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m"})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, 1, reports[0].MissingCount, "ETH is active but has produced no output")
	require.Equal(t, 1, reports[0].StaleCount)
	require.Empty(t, reports[0].StaleSubjects, "the retired OLD subject is not reported")
}

func TestKlineFreshnessEvaluatorWaitsOneBarBeforeCountingMissing(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	// Storage View just restarted: it has seen one subject in its first bar.
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Minute), Subjects: 1, ObservedAt: now}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "active"}}}
	query.storage = NewStorageAdapter(nil, metadata, monconfig.MetricsStorageConfig{SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m"})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.True(t, reports[0].Success, "ETH may simply not have written since the restart")
	require.Zero(t, reports[0].MissingCount)
}

func TestKlineFreshnessEvaluatorMatchesMarketSuffixDuringSubjectMigration(t *testing.T) {
	now := time.Date(2026, 9, 10, 11, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 2, Lagging: 1, ObservedAt: now,
		LaggingSubjects: []viewSummaryLaggingSample{{Subject: "OPG-USDT-SPOT", DataTime: now.Add(-20 * time.Minute), ObservedAt: now}},
	}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{
		{SubjectId: "OPG-USDT", Status: "active"},
		{SubjectId: "ETH-USDT", Status: "active"},
	}}
	query.storage = NewStorageAdapter(nil, metadata, monconfig.MetricsStorageConfig{SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m"})

	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, []string{"OPG-USDT"}, reports[0].StaleSubjects)
	require.Equal(t, 1, reports[0].StaleCount)
	require.Zero(t, reports[0].MissingCount)
}

func TestKlineFreshnessEvaluatorRejectsAmbiguousMarketSuffix(t *testing.T) {
	now := time.Date(2026, 9, 10, 11, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 1, Lagging: 1, ObservedAt: now,
		LaggingSubjects: []viewSummaryLaggingSample{{Subject: "OPG-USDT", DataTime: now.Add(-20 * time.Minute), ObservedAt: now}},
	}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{
		{SubjectId: "OPG-USDT-SPOT", Status: "active"},
		{SubjectId: "OPG-USDT-SWAP", Status: "active"},
	}}
	query.storage = NewStorageAdapter(nil, metadata, monconfig.MetricsStorageConfig{SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m"})

	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Empty(t, reports[0].StaleSubjects, "an ambiguous alias is not attributed to either catalog subject")
	require.Equal(t, 2, reports[0].MissingCount)
}

func TestKlineFreshnessEvaluatorSkipsStockClosedAndWarmup(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "stockcn", ViewID: "view", Frequency: "1m", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, StaleAfter: 10 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{SpaceID: "stockcn", Latest: time.Date(2026, 9, 7, 7, 0, 0, 0, time.UTC), Subjects: 1, ObservedAt: time.Date(2026, 9, 7, 7, 0, 0, 0, time.UTC)}})
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

type viewSummaryLaggingSample struct {
	Subject              string
	DataTime, ObservedAt time.Time
}

// viewSummaryTestSample is one View's summary as Storage View reports it;
// identity fields default to crypto/view/dataset/1m.
type viewSummaryTestSample struct {
	SpaceID, ViewID, DatasetID, Freq string
	RawLabels                        string
	Latest, TrackedSince, ObservedAt time.Time
	Subjects, Lagging                int
	LaggingSubjects                  []viewSummaryLaggingSample
}

func newViewSummaryQuery(t *testing.T, samples []viewSummaryTestSample) *QueryService {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	or := func(value, fallback string) string {
		if value == "" {
			return fallback
		}
		return value
	}
	_, err = store.WithDatabase(mgr, func(db *gorm.DB) error {
		var rows []MetricLatest
		for index, sample := range samples {
			scope := map[string]string{
				"space_id": or(sample.SpaceID, "crypto"), "view_id": or(sample.ViewID, "view"),
				"dataset_id": or(sample.DatasetID, "dataset"), "freq": or(sample.Freq, "1m"),
			}
			labels := func(subject string) string {
				if sample.RawLabels != "" {
					return sample.RawLabels
				}
				values := map[string]string{}
				for key, value := range scope {
					values[key] = value
				}
				if subject != "" {
					values["subject_id"] = subject
				}
				raw, marshalErr := json.Marshal(values)
				require.NoError(t, marshalErr)
				return string(raw)
			}
			trackedSince := sample.TrackedSince
			if trackedSince.IsZero() {
				trackedSince = sample.Latest
			}
			add := func(name, subject string, value float64, observedAt time.Time) {
				rows = append(rows, MetricLatest{SeriesID: fmt.Sprintf("%d-%s-%s", index, name, subject), MetricName: name, LabelsJSON: labels(subject), Value: value, ObservedAt: observedAt})
			}
			add(ViewOutputLatestMetric, "", float64(sample.Latest.Unix()), sample.ObservedAt)
			add(ViewOutputTrackedSinceMetric, "", float64(trackedSince.Unix()), sample.ObservedAt)
			add(ViewOutputSubjectsMetric, "", float64(sample.Subjects), sample.ObservedAt)
			add(ViewOutputLaggingMetric, "", float64(sample.Lagging), sample.ObservedAt)
			for _, item := range sample.LaggingSubjects {
				add(ViewOutputLaggingSubjectMetric, item.Subject, float64(item.DataTime.Unix()), item.ObservedAt)
			}
		}
		if len(rows) == 0 {
			return nil
		}
		return db.Create(&rows).Error
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

// Collector names hourly results 1H; their View output must be observed.
func TestKlineFreshnessEvaluatorObservesCollectorHourlyFrequency(t *testing.T) {
	now := time.Date(2026, 10, 7, 7, 20, 0, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Frequency: "1H", MarketID: "crypto", StaleAfter: 2 * time.Hour}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Freq: "1H",
		Latest: now.Add(-80 * time.Minute), Subjects: 1, ObservedAt: now,
	}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, 1, reports[0].ObservedCount)
	require.True(t, reports[0].Success, "reason=%s", reports[0].Reason)
}

func TestKlineFrequencyUnitsAreCaseInsensitiveExceptMonth(t *testing.T) {
	require.True(t, isValidKlineFrequency("1H"))
	require.True(t, isValidKlineFrequency("1h"))
	require.True(t, isValidKlineFrequency("1M"))
	require.Equal(t, klineFrequencyUnit("H"), klineFrequencyUnit("h"))
	require.NotEqual(t, klineFrequencyUnit("M"), klineFrequencyUnit("m"), "M is month, m is minute")
}

// An hourly bar is stamped with its period start and the next bar closes two
// periods later; data is fresh until that next bar is stale_after overdue.
func TestKlineFreshnessMeasuresStalenessFromBarClose(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Frequency: "1H", MarketID: "crypto", StaleAfter: 5 * time.Minute}
	barStart := time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC)
	evaluate := func(now time.Time) KlineFreshnessReport {
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{
			ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Freq: "1H",
			Latest: barStart, Subjects: 1, ObservedAt: now,
		}})
		reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	current := evaluate(barStart.Add(time.Hour + 29*time.Minute))
	require.True(t, current.Success, "the 06:00 bar is the newest possible at 07:29: %s", current.Diagnostic)
	due := evaluate(barStart.Add(2*time.Hour + 4*time.Minute))
	require.True(t, due.Success, "the 07:00 bar is only four minutes overdue: %s", due.Diagnostic)
	stale := evaluate(barStart.Add(2*time.Hour + 6*time.Minute))
	require.False(t, stale.Success, "the 07:00 bar is six minutes overdue with stale_after=5m")
	require.Equal(t, 6*time.Minute, stale.StaleAge)
}
