package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/stretchr/testify/require"
)

var hongKong = time.FixedZone("Asia/Hong_Kong", 8*60*60)

func hkAt(t *testing.T, day, clock string) time.Time {
	t.Helper()
	value, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, hongKong)
	require.NoError(t, err)
	return value.UTC()
}

var stockHKSessions = []string{"09:30-12:00", "13:00-16:00"}

// 港股 5m 的应收最新一根按内嵌的港股日历和交易时段算：普通日、午休、收市后，以及半日市（2026-12-24 只开上午）。
func TestStockHKClockFollowsSessionsAndHalfDays(t *testing.T) {
	clock, err := MarketClockFor("stockhk", "hk_stock", "Asia/Hong_Kong", stockHKSessions)
	require.NoError(t, err)
	require.True(t, clock.Exchange())
	for _, tc := range []struct {
		name, day, now string
		wantDay, open  string
	}{
		// 2026-11-12 是周四，普通交易日。
		{"时段中", "2026-11-12", "10:17:42", "2026-11-12", "10:10:00"},
		{"午休", "2026-11-12", "12:30:00", "2026-11-12", "11:55:00"},
		{"下午第一根还没收线", "2026-11-12", "13:02:00", "2026-11-12", "11:55:00"},
		{"下午第一根已收线", "2026-11-12", "13:05:05", "2026-11-12", "13:00:00"},
		{"收市后", "2026-11-12", "20:00:00", "2026-11-12", "15:55:00"},
		{"次日开盘前", "2026-11-13", "09:00:00", "2026-11-12", "15:55:00"},
		// 半日市：12:00 收市，下午没有 K 线，15:00 应收的仍是 11:55 那根。
		{"半日市下午", "2026-12-24", "15:00:00", "2026-12-24", "11:55:00"},
		// 圣诞假期（12-25、12-26）加周日，12-28 开盘前仍回溯到半日市的最后一根。
		{"半日市后的假期", "2026-12-26", "11:00:00", "2026-12-24", "11:55:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clock.LatestClosed(hkAt(t, tc.day, tc.now), frequency.Minute5)
			require.NoError(t, err)
			require.True(t, got.Open.Equal(hkAt(t, tc.wantDay, tc.open)), "应收 %s，实际 %s", tc.open, got.Open.In(hongKong))
			require.Equal(t, 5*time.Minute, got.Close.Sub(got.Open))
		})
	}
	// 半日市最后一根的下一根是 12-28 09:30，不是当天 13:00。
	last, err := clock.BarAt(hkAt(t, "2026-12-24", "11:55:00"), frequency.Minute5)
	require.NoError(t, err)
	next, err := clock.Next(last, frequency.Minute5)
	require.NoError(t, err)
	require.True(t, next.Open.Equal(hkAt(t, "2026-12-28", "09:30:00")), "下一根是 %s", next.Open.In(hongKong))
	// 半日市下午的时间不属于任何一根。
	_, err = clock.BarAt(hkAt(t, "2026-12-24", "13:00:00"), frequency.Minute5)
	require.ErrorIs(t, err, ErrOutsideSession)
}

// 港股 5m 的 K 线新鲜度：午休、收市后、半日市下午与其后的假期里，View 停在该时段最后一根不算过期；
// 时段中停更超过允许迟到才判失败。
func TestKlineFreshnessEvaluatorUsesTheStockHKCalendar(t *testing.T) {
	rule := KlineFreshnessRule{Enabled: true, SpaceID: "stockhk", ViewID: "view", Frequency: "5m", MarketID: "stockhk", CalendarID: "hk_stock", Timezone: "Asia/Hong_Kong", Sessions: stockHKSessions, Lateness: 90 * time.Second}
	evaluate := func(latest, now time.Time) KlineFreshnessReport {
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{SpaceID: "stockhk", Freq: "5m", Latest: latest, Subjects: 1, ObservedAt: now}})
		reports, err := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{rule}, 20).Evaluate(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		return reports[0]
	}
	for _, tc := range []struct{ name, latestDay, latest, nowDay, now string }{
		{"时段中", "2026-11-12", "10:10:00", "2026-11-12", "10:15:30"},
		{"午休", "2026-11-12", "11:55:00", "2026-11-12", "12:30:00"},
		{"下午第一根还没收线", "2026-11-12", "11:55:00", "2026-11-12", "13:04:30"},
		{"收市后", "2026-11-12", "15:55:00", "2026-11-12", "20:00:00"},
		{"次日开盘前", "2026-11-12", "15:55:00", "2026-11-13", "09:00:00"},
		{"半日市下午", "2026-12-24", "11:55:00", "2026-12-24", "15:00:00"},
		{"半日市后的假期", "2026-12-24", "11:55:00", "2026-12-26", "11:00:00"},
		{"假期后开盘第一根还没收线", "2026-12-24", "11:55:00", "2026-12-28", "09:34:00"},
	} {
		report := evaluate(hkAt(t, tc.latestDay, tc.latest), hkAt(t, tc.nowDay, tc.now))
		require.False(t, report.Skipped, tc.name)
		require.True(t, report.Success, "%s reason=%s diagnostic=%s", tc.name, report.Reason, report.Diagnostic)
	}
	// 上午停在 10:00：缺的 10:05 那根 10:10 收线，到 10:30 已逾期 20 分钟。
	report := evaluate(hkAt(t, "2026-11-12", "10:00:00"), hkAt(t, "2026-11-12", "10:30:00"))
	require.False(t, report.Success)
	require.True(t, report.ViewStale)
	require.Equal(t, 20*time.Minute, report.StaleAge)
	// 半日市上午停在 11:00：缺的 11:05 那根 11:10 收线，到 15:00 仍算过期，不因为下午休市而放过。
	report = evaluate(hkAt(t, "2026-12-24", "11:00:00"), hkAt(t, "2026-12-24", "15:00:00"))
	require.False(t, report.Success)
	require.True(t, report.ViewStale)
	// 假期后 12-28 09:30 那根 09:35 收线，到 09:40 还没到就过期。
	report = evaluate(hkAt(t, "2026-12-24", "11:55:00"), hkAt(t, "2026-12-28", "09:40:00"))
	require.False(t, report.Success)
	require.Equal(t, 5*time.Minute, report.StaleAge)
}
