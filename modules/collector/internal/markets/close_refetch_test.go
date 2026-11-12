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

func TestParseConfigRefetchAfterClose(t *testing.T) {
	config, err := ConfigFor("stockhk")
	require.NoError(t, err)
	require.Equal(t, 15, config.RefetchAfterCloseMinutes)
	_, err = parseConfig("demo", []byte("market_id: demo\nallowed_lateness_seconds: 60\nsubject_egress: direct\nrefetch_after_close_minutes: -1\n"))
	require.ErrorContains(t, err, "refetch_after_close_minutes")
}
