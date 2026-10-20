package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestKlineFreshnessEvaluatorFlagsStaleView(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: now.Add(-10 * time.Minute), Subjects: 3, ObservedAt: now}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, "business_data_stale", reports[0].Reason)
	require.Equal(t, 3, reports[0].StaleCount, "a stale View makes every subject stale")
	require.True(t, reports[0].ViewStale)
}

func TestKlineFreshnessEvaluatorDoesNotAlertTransientHole(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
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
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
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
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
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
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 2, Lagging: 1, ObservedAt: now,
		LaggingSubjects: []viewSummaryLaggingSample{{Subject: "OLD", DataTime: now.Add(-10 * time.Minute), ObservedAt: now}},
	}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "active"}, {SubjectId: "OLD", Status: "archived"}}}
	query.storage = NewStorageAdapter(metadata)
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
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	// Storage View just restarted: it has seen one subject in its first bar.
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Minute), Subjects: 1, ObservedAt: now}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{{SubjectId: "BTC", Status: "active"}, {SubjectId: "ETH", Status: "active"}}}
	query.storage = NewStorageAdapter(metadata)
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.True(t, reports[0].Success, "ETH may simply not have written since the restart")
	require.Zero(t, reports[0].MissingCount)
}

func TestKlineFreshnessEvaluatorMatchesMarketSuffixDuringSubjectMigration(t *testing.T) {
	now := time.Date(2026, 9, 10, 11, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 2, Lagging: 1, ObservedAt: now,
		LaggingSubjects: []viewSummaryLaggingSample{{Subject: "OPG-USDT-SPOT", DataTime: now.Add(-20 * time.Minute), ObservedAt: now}},
	}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{
		{SubjectId: "OPG-USDT", Status: "active"},
		{SubjectId: "ETH-USDT", Status: "active"},
	}}
	query.storage = NewStorageAdapter(metadata)

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
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 1, Lagging: 1, ObservedAt: now,
		LaggingSubjects: []viewSummaryLaggingSample{{Subject: "OPG-USDT", DataTime: now.Add(-20 * time.Minute), ObservedAt: now}},
	}})
	metadata := &fakeMetadata{subjects: []*storagepb.DatasetSubject{
		{SubjectId: "OPG-USDT-SPOT", Status: "active"},
		{SubjectId: "OPG-USDT-SWAP", Status: "active"},
	}}
	query.storage = NewStorageAdapter(metadata)

	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Empty(t, reports[0].StaleSubjects, "an ambiguous alias is not attributed to either catalog subject")
	require.Equal(t, 2, reports[0].MissingCount)
}

// A 股按交易日历判断应收的最新一根：午休、下午第一根收线前、次日开盘前都指向上一时段的最后一根，
// 不需要“休市跳过”“开盘预热”；缺的根真的超时才算停止更新。2026-09-08 是周二，09-09 是周三。
func TestKlineFreshnessEvaluatorUsesTheStockCalendar(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := func(day, clock string) time.Time {
		value, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, shanghai)
		require.NoError(t, err)
		return value.UTC()
	}
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "stockcn", ViewID: "view", Frequency: "1m", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, Lateness: 10 * time.Minute}
	evaluate := func(latest, now time.Time) KlineFreshnessReport {
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{SpaceID: "stockcn", Latest: latest, Subjects: 1, ObservedAt: now}})
		reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	morningClose := local("2026-09-08", "11:29:00")
	for name, now := range map[string]time.Time{
		"午休 12:10": local("2026-09-08", "12:10:00"),
		"13:00:30": local("2026-09-08", "13:00:30"),
		"下午第一根刚收线": local("2026-09-08", "13:01:20"),
	} {
		report := evaluate(morningClose, now)
		require.False(t, report.Skipped, name)
		require.True(t, report.Success, "%s reason=%s diagnostic=%s", name, report.Reason, report.Diagnostic)
	}
	dayClose := local("2026-09-08", "14:59:00")
	report := evaluate(dayClose, local("2026-09-09", "09:00:00"))
	require.True(t, report.Success, "次日开盘前 reason=%s", report.Reason)
	// 上午停在 10:00：缺的 10:01 那根 10:02 收线，到 12:10 已逾期两个多小时（不把午休算进去也远超 10 分钟）。
	report = evaluate(local("2026-09-08", "10:00:00"), local("2026-09-08", "12:10:00"))
	require.False(t, report.Success)
	require.True(t, report.ViewStale)
	require.Equal(t, local("2026-09-08", "12:10:00").Sub(local("2026-09-08", "10:02:00")), report.StaleAge)
}

// Storage 按连续时间判落后：下午第一根刚到时，还停在 11:29 的标的会被它算成落后。Monitor 按交易日历再筛：
// 它们缺的 13:00 那根 13:01 收线，还在允许迟到之内就不算过期；等到超过“允许迟到 + stale_after”才算。
func TestKlineFreshnessEvaluatorFiltersStorageLaggingByCalendar(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := func(clock string) time.Time {
		value, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-09-08 "+clock, shanghai)
		require.NoError(t, err)
		return value.UTC()
	}
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "stockcn", ViewID: "view", Frequency: "1m", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, Lateness: 5 * time.Minute}
	evaluate := func(now time.Time) KlineFreshnessReport {
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{
			SpaceID: "stockcn", Latest: local("13:00:00"), TrackedSince: local("09:30:00"), Subjects: 3, Lagging: 2, ObservedAt: now,
			LaggingSubjects: []viewSummaryLaggingSample{
				{Subject: "600000.XSHG", DataTime: local("11:29:00"), ObservedAt: now},
				{Subject: "600001.XSHG", DataTime: local("11:29:00"), ObservedAt: now},
			},
		}})
		reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	early := evaluate(local("13:01:20"))
	require.True(t, early.Success, "reason=%s stale=%d diagnostic=%s", early.Reason, early.StaleCount, early.Diagnostic)
	late := evaluate(local("13:07:00"))
	require.False(t, late.Success)
	require.Equal(t, 2, late.StaleCount)
	require.Equal(t, 6*time.Minute, late.StaleAge)
}

func TestKlineFreshnessEvaluatorReportsCalendarCoverage(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "stockcn", ViewID: "view", Frequency: "1m", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, Lateness: 10 * time.Minute}
	now := time.Date(2099, 1, 5, 2, 0, 0, 0, time.UTC)
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{SpaceID: "stockcn", Latest: now.Add(-time.Hour), Subjects: 1, ObservedAt: now}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.False(t, reports[0].Success)
	require.Equal(t, "calendar_not_covered", reports[0].Reason)
	require.Contains(t, reports[0].Diagnostic, "日历未覆盖")
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
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Frequency: "1h", MarketID: "crypto", Lateness: 2 * time.Hour}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Freq: "1h",
		Latest: now.Add(-80 * time.Minute), Subjects: 1, ObservedAt: now,
	}})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, 1, reports[0].ObservedCount)
	require.True(t, reports[0].Success, "reason=%s", reports[0].Reason)
}

func TestKlineFrequencyAcceptsOnlyCanonicalIdentities(t *testing.T) {
	require.True(t, isValidKlineFrequency("1h"))
	require.True(t, isValidKlineFrequency("1mo"))
	require.False(t, isValidKlineFrequency("1H"), "Storage identities are canonical")
	require.Equal(t, "1h", canonicalRuleFrequency("1H"))
}

// An hourly bar is stamped with its period start and the next bar closes two
// periods later; data is fresh until that next bar is stale_after overdue.
func TestKlineFreshnessMeasuresStalenessFromBarClose(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Frequency: "1h", MarketID: "crypto", Lateness: 5 * time.Minute}
	barStart := time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC)
	evaluate := func(now time.Time) KlineFreshnessReport {
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{
			ViewID: "view_task_kline_1h", DatasetID: "dataset_task", Freq: "1h",
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

// Storage View 每 30 秒上报一次摘要（整分与半分对齐，比收线晚几毫秒）：逾期按快照时刻算。生产上 :30 的评估偶尔早于
// :30 那次快照入库，按当前时间算会把准时写完、还没来得及上报的分钟线判成“整体停止更新”（2026-10-16 一天 22 次）。
func TestKlineFreshnessMeasuresOverdueAtTheSnapshot(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", ViewID: "view", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 30 * time.Second}
	minute := time.Date(2026, 10, 16, 10, 48, 0, 0, time.UTC)
	evaluate := func(snapshot, now time.Time) KlineFreshnessReport {
		// 快照里最新一根是 10:46 那根（10:47 收线）；10:47 那根 10:48 收线后还没出现在快照里。
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: minute.Add(-2 * time.Minute), Subjects: 1, ObservedAt: snapshot}})
		reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	// 10:48:30.6 评估，最近的快照仍是 10:48:00.002：快照时它只晚了 2 毫秒，不算停止更新。
	early := evaluate(minute.Add(2*time.Millisecond), minute.Add(30*time.Second+600*time.Millisecond))
	require.True(t, early.Success, early.Diagnostic)
	require.Zero(t, early.StaleAge)
	// 10:48:30.002 的快照里仍没有：快照时已晚超过允许迟到，判为整体停止更新，逾期按快照时刻记。
	stale := evaluate(minute.Add(30*time.Second+2*time.Millisecond), minute.Add(30*time.Second+600*time.Millisecond))
	require.False(t, stale.Success)
	require.True(t, stale.ViewStale)
	require.Equal(t, 30*time.Second+2*time.Millisecond, stale.StaleAge)
	// 快照时刻晚于当前时间（生产者时钟偏快）时按当前时间算。
	require.False(t, evaluate(minute.Add(time.Minute), minute.Add(31*time.Second)).Success)
	// 摘要超过 1 分钟没有更新（漏报，或重启后视图没有新写入、不再产生摘要）：按当前时间算，停更照样报出来。
	frozen := evaluate(minute.Add(-time.Minute), minute.Add(30*time.Second+600*time.Millisecond))
	require.False(t, frozen.Success)
	require.Equal(t, 30*time.Second+600*time.Millisecond, frozen.StaleAge)
}

// 落后对象名单只上报最落后的若干个：停用对象多于名单长度时，名单只能用于展示，不能用来扣减完整计数，
// 否则剩下那个未上报名字的停用对象会被算作过期，持续误报。
func TestKlineFreshnessEvaluatorTruncatedLaggingListDoesNotKeepAlertingForRetiredSubjects(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	var lagging []viewSummaryLaggingSample
	subjects := []*storagepb.DatasetSubject{{SubjectId: "BTC", Status: "active"}}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("OLD%02d", i)
		lagging = append(lagging, viewSummaryLaggingSample{Subject: id, DataTime: now.Add(-time.Duration(60-i) * time.Minute), ObservedAt: now})
		subjects = append(subjects, &storagepb.DatasetSubject{SubjectId: id, Status: "archived"})
	}
	// 22 个对象里 21 个已停用落后（只上报了 20 个名字），剩下 BTC 正常更新
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 22, Lagging: 21, ObservedAt: now,
		LaggingSubjects: lagging,
	}})
	query.storage = NewStorageAdapter(&fakeMetadata{subjects: subjects})
	reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	// 名单被截断，只能推出“落后数下界为零”：不能宣告健康，也不误报过期，本轮按无法判定跳过
	require.True(t, reports[0].Skipped, reports[0].Diagnostic)
	require.False(t, reports[0].Success)
	require.Equal(t, "lagging_subjects_truncated", reports[0].Reason)
	require.Zero(t, reports[0].StaleCount)
}
