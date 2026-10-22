package metrics

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type fakeFirstSeenStore struct {
	fresh map[string]time.Time
	err   error
	calls int
}

func (f *fakeFirstSeenStore) ObserveSubjects(context.Context, string, string, map[string]struct{}, time.Time) (map[string]time.Time, error) {
	f.calls++
	return f.fresh, f.err
}

func activeSubjects(ids ...string) []*storagepb.DatasetSubject {
	subjects := make([]*storagepb.DatasetSubject, 0, len(ids))
	for _, id := range ids {
		subjects = append(subjects, &storagepb.DatasetSubject{SubjectId: id, Status: "active"})
	}
	return subjects
}

// 现货小时线：当天新增 2 个对象，第一根完整小时线要到 14:00 收线才有，此前 View 里没有它们。
func hourlyNewSubjectEvaluation(t *testing.T, now time.Time, viewSubjects int, active []string, firstSeen map[string]time.Time) KlineFreshnessReport {
	t.Helper()
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1h", MarketID: "crypto", Lateness: 5 * time.Minute}
	latest := now.Truncate(time.Hour).Add(-time.Hour)
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		Freq: "1h", Latest: latest, TrackedSince: now.Add(-6 * time.Hour), Subjects: viewSubjects, ObservedAt: now,
	}})
	query.storage = NewStorageAdapter(&fakeMetadata{subjects: activeSubjects(active...)})
	evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20)
	evaluator.SetSubjectFirstSeenStore(&fakeFirstSeenStore{fresh: firstSeen})
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	return reports[0]
}

func TestKlineFreshnessNewSubjectsAreDegradedNotFailedWithinGrace(t *testing.T) {
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	seenAt := time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC)
	got := hourlyNewSubjectEvaluation(t, now, 3, []string{"BTC", "ETH", "SOL", "NEW1", "NEW2"}, map[string]time.Time{"NEW1": seenAt, "NEW2": seenAt})
	require.True(t, got.Success, got.Diagnostic)
	require.Equal(t, "new_subjects_pending", got.Reason)
	require.Equal(t, 2, got.PendingCount)
	require.Zero(t, got.MissingCount)
	require.Zero(t, got.StaleCount)
	// 首次出现后的第一根完整小时线 13:00～14:00，收线后再留 5 分钟允许迟到和 10 分钟采集生效余量。
	require.Equal(t, time.Date(2026, 10, 21, 14, 15, 0, 0, time.UTC), got.PendingUntil)
}

func TestKlineFreshnessNewSubjectsFailAfterGrace(t *testing.T) {
	seenAt := time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC)
	now := time.Date(2026, 10, 21, 14, 16, 0, 0, time.UTC)
	got := hourlyNewSubjectEvaluation(t, now, 3, []string{"BTC", "ETH", "SOL", "NEW1", "NEW2"}, map[string]time.Time{"NEW1": seenAt, "NEW2": seenAt})
	require.False(t, got.Success)
	require.Equal(t, "business_data_stale", got.Reason)
	require.Equal(t, 2, got.MissingCount)
	require.Zero(t, got.PendingCount)
}

func TestKlineFreshnessExistingSubjectsGetNoGrace(t *testing.T) {
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	// 首次部署：当前活跃对象全是存量（存储不返回它们），缺失照常告警。
	got := hourlyNewSubjectEvaluation(t, now, 3, []string{"BTC", "ETH", "SOL", "NEW1", "NEW2"}, nil)
	require.False(t, got.Success)
	require.Equal(t, 2, got.MissingCount)
	require.Zero(t, got.PendingCount)
}

func TestKlineFreshnessGraceOnlyCoversAsManyAsAreNew(t *testing.T) {
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	seenAt := time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC)
	// 缺 2 个，只有 1 个是新对象：另一个按真缺失告警，并带出等待中的新对象。
	got := hourlyNewSubjectEvaluation(t, now, 3, []string{"BTC", "ETH", "SOL", "NEW1", "OLD"}, map[string]time.Time{"NEW1": seenAt})
	require.False(t, got.Success)
	require.Equal(t, 1, got.MissingCount)
	require.Equal(t, 1, got.StaleCount)
	require.Equal(t, 1, got.PendingCount)
}

func TestKlineFreshnessNewSubjectWithDataIsHealthy(t *testing.T) {
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	seenAt := time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC)
	// 新对象已经有数据（View 里 4 个对象）：没有缺失，不显示等待。
	got := hourlyNewSubjectEvaluation(t, now, 4, []string{"BTC", "ETH", "SOL", "NEW1"}, map[string]time.Time{"NEW1": seenAt})
	require.True(t, got.Success)
	require.Equal(t, "fresh", got.Reason)
	require.Zero(t, got.PendingCount)
}

func TestKlineFreshnessGraceStartsFromTheNextWholeBarForMinuteViews(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 14, 40, 0, time.UTC)
	seenAt := time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 10 * time.Second}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: time.Date(2026, 10, 21, 12, 13, 0, 0, time.UTC), TrackedSince: now.Add(-time.Hour), Subjects: 2, ObservedAt: now}})
	query.storage = NewStorageAdapter(&fakeMetadata{subjects: activeSubjects("BTC", "ETH", "NEW1")})
	evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20)
	evaluator.SetSubjectFirstSeenStore(&fakeFirstSeenStore{fresh: map[string]time.Time{"NEW1": seenAt}})
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.True(t, reports[0].Success, reports[0].Diagnostic)
	require.Equal(t, 1, reports[0].PendingCount)
	// 12:13:31 出现，12:13 那根不完整，12:14～12:15 是第一根完整的。
	require.Equal(t, time.Date(2026, 10, 21, 12, 25, 10, 0, time.UTC), reports[0].PendingUntil)
}

// 名单被截断时缺失的对象被算进 StaleCount、MissingCount 为 0：宽限也要覆盖这条路径。
func TestKlineFreshnessGraceAppliesWhenLaggingListIsTruncated(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	subjects := activeSubjects("BTC", "NEW1")
	var lagging []viewSummaryLaggingSample
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("OLD%02d", i)
		lagging = append(lagging, viewSummaryLaggingSample{Subject: id, DataTime: now.Add(-time.Duration(60-i) * time.Minute), ObservedAt: now})
		subjects = append(subjects, &storagepb.DatasetSubject{SubjectId: id, Status: "archived"})
	}
	build := func(firstSeen map[string]time.Time) KlineFreshnessReport {
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{
			Latest: now.Add(-time.Minute), TrackedSince: now.Add(-time.Hour), Subjects: 22, Lagging: 21, ObservedAt: now, LaggingSubjects: lagging,
		}})
		query.storage = NewStorageAdapter(&fakeMetadata{subjects: subjects})
		evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20)
		evaluator.SetSubjectFirstSeenStore(&fakeFirstSeenStore{fresh: firstSeen})
		reports, err := evaluator.Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	without := build(nil)
	require.False(t, without.Success, "NEW1 has no output and is not in grace")
	require.Equal(t, 1, without.StaleCount)

	got := build(map[string]time.Time{"NEW1": now.Add(-30 * time.Second)})
	require.True(t, got.Success, got.Diagnostic)
	require.False(t, got.Skipped, "扣除新对象后不能被误判成“无法判定”而跳过")
	require.Equal(t, "new_subjects_pending", got.Reason)
	require.Equal(t, 1, got.PendingCount)
}

func TestKlineFreshnessFirstSeenFailureGivesNoGrace(t *testing.T) {
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1h", MarketID: "crypto", Lateness: 5 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Freq: "1h", Latest: now.Truncate(time.Hour).Add(-time.Hour), TrackedSince: now.Add(-6 * time.Hour), Subjects: 1, ObservedAt: now}})
	query.storage = NewStorageAdapter(&fakeMetadata{subjects: activeSubjects("BTC", "NEW1")})
	evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20)
	evaluator.SetSubjectFirstSeenStore(&fakeFirstSeenStore{err: errors.New("database is locked")})
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.False(t, reports[0].Success, "记录失败时宁可告警，不掩盖真故障")
	require.Equal(t, 1, reports[0].MissingCount)
}

func TestNewSubjectDeadline(t *testing.T) {
	lateness := 5 * time.Minute
	cases := []struct {
		name      string
		freq      string
		firstSeen time.Time
		want      time.Time
	}{
		{"hour mid-bar", "1h", time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC), time.Date(2026, 10, 21, 14, 15, 0, 0, time.UTC)},
		{"hour exactly on boundary", "1h", time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 21, 13, 15, 0, 0, time.UTC)},
		{"minute mid-bar", "1m", time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC), time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := KlineFreshnessRule{Frequency: tc.freq, MarketID: "crypto"}
			clock, freq, _, err := klineExpectedBar(rule, tc.firstSeen)
			require.NoError(t, err)
			got, err := newSubjectDeadline(clock, freq, tc.firstSeen, lateness)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// 重新上架的对象在 Storage 里还带着下架前的水位，会出现在落后名单里：宽限期内同样不算过期。
func TestKlineFreshnessRelistedSubjectWithOldWatermarkGetsGrace(t *testing.T) {
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1h", MarketID: "crypto", Lateness: 5 * time.Minute}
	build := func(firstSeen map[string]time.Time) KlineFreshnessReport {
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{
			Freq: "1h", Latest: now.Truncate(time.Hour).Add(-time.Hour), TrackedSince: now.Add(-48 * time.Hour), Subjects: 2, Lagging: 1, ObservedAt: now,
			LaggingSubjects: []viewSummaryLaggingSample{{Subject: "ETH", DataTime: now.Add(-30 * time.Hour), ObservedAt: now}},
		}})
		query.storage = NewStorageAdapter(&fakeMetadata{subjects: activeSubjects("BTC", "ETH")})
		evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20)
		evaluator.SetSubjectFirstSeenStore(&fakeFirstSeenStore{fresh: firstSeen})
		reports, err := evaluator.Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	require.False(t, build(nil).Success, "没有重新上架记录时，ETH 的旧水位就是真落后")

	got := build(map[string]time.Time{"ETH": time.Date(2026, 10, 21, 12, 13, 31, 0, time.UTC)})
	require.True(t, got.Success, got.Diagnostic)
	require.Equal(t, "new_subjects_pending", got.Reason)
	require.Equal(t, 1, got.PendingCount)
	require.Empty(t, got.StaleSubjects)
	require.Equal(t, time.Date(2026, 10, 21, 14, 15, 0, 0, time.UTC), got.PendingUntil)
}

// View 落后于采集已完成的最新一期时会提前返回：首次出现时间仍要在读到清单的这一轮记下，否则对象会在 View 追平后才开始计时。
func TestKlineFreshnessRecordsFirstSeenEvenWhenViewIsBehind(t *testing.T) {
	now := time.Date(2026, 10, 21, 13, 51, 0, 0, time.UTC)
	rule := KlineFreshnessRule{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1h", MarketID: "crypto", Lateness: 5 * time.Minute,
		LatestCompletedPeriod: now.Truncate(time.Hour).Add(-time.Hour), LatestCompletedStatus: "complete",
	}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Freq: "1h", Latest: now.Truncate(time.Hour).Add(-2 * time.Hour), TrackedSince: now.Add(-6 * time.Hour), Subjects: 1, ObservedAt: now}})
	query.storage = NewStorageAdapter(&fakeMetadata{subjects: activeSubjects("BTC", "NEW1")})
	observer := &fakeFirstSeenStore{}
	evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20)
	evaluator.SetSubjectFirstSeenStore(observer)
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, "view_behind_latest_completed_period", reports[0].Reason)
	require.Equal(t, 1, observer.calls)
}

// A 股午休后刚开盘：大量对象还停在午休前最后一根，落后名单被截断。名单里的都在允许迟到之内，没列出的落后得更少，
// 同样不算过期；这时只剩一个等首根 K 线的新对象，应是黄色关注而不是失败。
func TestKlineFreshnessTruncatedLaggingAllWithinGraceDoesNotCountUnlisted(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := func(clock string) time.Time {
		value, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-09-08 "+clock, shanghai)
		require.NoError(t, err)
		return value.UTC()
	}
	now := local("13:01:20")
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "stockcn", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}, Lateness: 5 * time.Minute}
	ids := make([]string, 0, 203)
	var lagging []viewSummaryLaggingSample
	for i := 0; i < 202; i++ {
		id := fmt.Sprintf("S%03d", i)
		ids = append(ids, id)
		if i < 200 {
			lagging = append(lagging, viewSummaryLaggingSample{Subject: id, DataTime: local("11:29:00"), ObservedAt: now})
		}
	}
	ids = append(ids, "NEW1")
	build := func(firstSeen map[string]time.Time) KlineFreshnessReport {
		// 202 个对象有输出，其中 201 个停在午休前（只列出 200 个名字）；NEW1 没有任何输出。
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{
			SpaceID: "stockcn", Latest: local("13:00:00"), TrackedSince: local("09:30:00"), Subjects: 202, Lagging: 201, ObservedAt: now, LaggingSubjects: lagging,
		}})
		query.storage = NewStorageAdapter(&fakeMetadata{subjects: activeSubjects(ids...)})
		evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20)
		evaluator.SetSubjectFirstSeenStore(&fakeFirstSeenStore{fresh: firstSeen})
		reports, err := evaluator.Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	got := build(map[string]time.Time{"NEW1": local("12:59:00")})
	require.True(t, got.Success, "reason=%s stale=%d missing=%d diagnostic=%s", got.Reason, got.StaleCount, got.MissingCount, got.Diagnostic)
	require.Equal(t, 1, got.PendingCount)

	// 没有新对象宽限时，缺的那一个照常算缺失；午休后落后的 201 个不能再被多算一个。
	plain := build(nil)
	require.False(t, plain.Success)
	require.Equal(t, 1, plain.StaleCount, "只有 NEW1 缺失")
	require.Equal(t, 1, plain.MissingCount)
}
