package markets

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// dueRounds 把 [from, to) 按 5 分钟一轮走一遍，返回命中“收市后整天重取”的各轮起点。
func dueRounds(t *testing.T, marketID string, from, to time.Time) []time.Time {
	t.Helper()
	var hits []time.Time
	for round := from; round.Before(to); round = round.Add(5 * time.Minute) {
		_, _, due, err := CloseRefetchDay(marketID, round.Add(-5*time.Minute), round)
		require.NoError(t, err)
		if due {
			hits = append(hits, round)
		}
	}
	return hits
}

// 港股配了收市后 15 分钟：普通交易日只有 16:15 那一轮命中，范围是香港时间当天零点到次日零点。
func TestCloseRefetchDayHitsOneRoundAfterTheClose(t *testing.T) {
	// 香港时间与北京时间相同，沿用 cst。2026-11-12 是周四。
	require.Equal(t, []time.Time{cst("2026-11-12", "16:15")}, dueRounds(t, "stockhk", cst("2026-11-12", "00:00"), cst("2026-11-13", "00:00")))

	from, to, due, err := CloseRefetchDay("stockhk", cst("2026-11-12", "16:10"), cst("2026-11-12", "16:15"))
	require.NoError(t, err)
	require.True(t, due)
	require.Equal(t, time.Date(2026, 11, 11, 16, 0, 0, 0, time.UTC), from, "香港时间 11-12 零点是 UTC 前一天 16:00")
	require.Equal(t, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC), to)

	// 时刻正好等于窗口右端算命中，等于左端不算：相邻两轮不会都命中。
	_, _, due, err = CloseRefetchDay("stockhk", cst("2026-11-12", "16:15"), cst("2026-11-12", "16:20"))
	require.NoError(t, err)
	require.False(t, due)
}

// 周末不是交易日，一整天没有一轮命中；一周里命中的轮数等于交易日数。
func TestCloseRefetchDaySkipsNonTradingDays(t *testing.T) {
	require.Empty(t, dueRounds(t, "stockhk", cst("2026-11-14", "00:00"), cst("2026-11-16", "00:00")), "11-14、11-15 是周末")
	week := dueRounds(t, "stockhk", cst("2026-11-09", "00:00"), cst("2026-11-16", "00:00"))
	require.Len(t, week, 5)
}

// 半日市（2026-12-24）12:00 收市：12:15 那一轮命中，16:15 不再命中。
func TestCloseRefetchDayUsesTheEarlyClose(t *testing.T) {
	require.Equal(t, []time.Time{cst("2026-12-24", "12:15")}, dueRounds(t, "stockhk", cst("2026-12-24", "00:00"), cst("2026-12-25", "00:00")))
	from, to, due, err := CloseRefetchDay("stockhk", cst("2026-12-24", "12:10"), cst("2026-12-24", "12:15"))
	require.NoError(t, err)
	require.True(t, due)
	require.Equal(t, cst("2026-12-24", "00:00"), from)
	require.Equal(t, cst("2026-12-25", "00:00"), to)
}

// A 股、crypto 没配 refetch_after_close_minutes：任何一轮都不命中。未登记的市场报错。
func TestCloseRefetchDayIsOffWithoutTheSetting(t *testing.T) {
	for _, marketID := range []string{"stockcn", "crypto"} {
		config, err := ConfigFor(marketID)
		require.NoError(t, err)
		require.Zero(t, config.RefetchAfterCloseMinutes, marketID)
		require.Empty(t, dueRounds(t, marketID, cst("2026-11-12", "00:00"), cst("2026-11-13", "00:00")), marketID)
	}
	_, _, _, err := CloseRefetchDay("no_such_market", cst("2026-11-12", "16:10"), cst("2026-11-12", "16:15"))
	require.ErrorIs(t, err, ErrUnknownMarket)
}

// nyt 解析纽约当地时间。
func nyt(day, clock string) time.Time {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, location)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// 美股配了收市后 15 分钟：每个交易日只有纽约 16:15 那一轮命中，范围是纽约当天零点到次日零点（换算成 UTC）。
// 冬令时（UTC-5）零点是 05:00Z，夏令时（UTC-4）是 04:00Z；夏令时开始、结束后的第一个交易日也各验一天。
func TestCloseRefetchDayForStockUSUsesNewYorkMidnights(t *testing.T) {
	for _, tc := range []struct {
		name, day string
		from, to  time.Time
	}{
		{"冬令时周四", "2026-11-12", time.Date(2026, 11, 12, 5, 0, 0, 0, time.UTC), time.Date(2026, 11, 13, 5, 0, 0, 0, time.UTC)},
		{"夏令时周四", "2026-10-29", time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC), time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC)},
		// 夏令时结束（2026-11-01 凌晨）后的第一个交易日：当天零点已是冬令时。
		{"夏令时结束后的周一", "2026-11-02", time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC), time.Date(2026, 11, 3, 5, 0, 0, 0, time.UTC)},
		// 夏令时开始（2026-03-08 凌晨）后的第一个交易日。
		{"夏令时开始后的周一", "2026-03-09", time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 4, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, []time.Time{nyt(tc.day, "16:15")}, dueRounds(t, "stockus", nyt(tc.day, "00:00"), nyt(tc.day, "00:00").AddDate(0, 0, 1)))
			from, to, due, err := CloseRefetchDay("stockus", nyt(tc.day, "16:10"), nyt(tc.day, "16:15"))
			require.NoError(t, err)
			require.True(t, due)
			require.Equal(t, tc.from, from)
			require.Equal(t, tc.to, to)
		})
	}
}

// 美股的半日市（2026-11-27，13:00 收市）：13:15 那一轮命中，16:15 不再命中，范围仍是纽约当天整天。
// 周末、感恩节（11-26）全天休市，没有一轮命中。
func TestCloseRefetchDayForStockUSFollowsTheEarlyCloseAndHolidays(t *testing.T) {
	require.Equal(t, []time.Time{nyt("2026-11-27", "13:15")}, dueRounds(t, "stockus", nyt("2026-11-27", "00:00"), nyt("2026-11-28", "00:00")))
	from, to, due, err := CloseRefetchDay("stockus", nyt("2026-11-27", "13:10"), nyt("2026-11-27", "13:15"))
	require.NoError(t, err)
	require.True(t, due)
	require.Equal(t, time.Date(2026, 11, 27, 5, 0, 0, 0, time.UTC), from)
	require.Equal(t, time.Date(2026, 11, 28, 5, 0, 0, 0, time.UTC), to)

	require.Empty(t, dueRounds(t, "stockus", nyt("2026-11-26", "00:00"), nyt("2026-11-27", "00:00")), "感恩节全天休市")
	require.Empty(t, dueRounds(t, "stockus", nyt("2026-11-14", "00:00"), nyt("2026-11-16", "00:00")), "周末")
	require.Len(t, dueRounds(t, "stockus", nyt("2026-11-09", "00:00"), nyt("2026-11-16", "00:00")), 5)
}

func TestParseConfigRefetchAfterClose(t *testing.T) {
	for _, marketID := range []string{"stockhk", "stockus"} {
		config, err := ConfigFor(marketID)
		require.NoError(t, err)
		require.Equal(t, 15, config.RefetchAfterCloseMinutes, marketID)
	}
	var err error
	_, err = parseConfig("demo", []byte("market_id: demo\nallowed_lateness_seconds: 60\nsubject_egress: direct\nrefetch_after_close_minutes: -1\n"))
	require.ErrorContains(t, err, "refetch_after_close_minutes")
}
