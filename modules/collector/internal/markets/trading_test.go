package markets

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var shanghai = time.FixedZone("CST", 8*3600)

func cst(day, clock string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, shanghai)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

func TestTradingBarStockCN5mFollowsSessions(t *testing.T) {
	cases := []struct {
		at   time.Time
		want bool
		note string
	}{
		{cst("2026-10-12", "09:25"), false, "开盘集合竞价不是桶"},
		{cst("2026-10-12", "09:30"), true, "开盘第一根"},
		{cst("2026-10-12", "09:32"), false, "不是 5m 桶起点"},
		{cst("2026-10-12", "11:25"), true, "上午最后一根"},
		{cst("2026-10-12", "11:30"), false, "午休"},
		{cst("2026-10-12", "12:55"), false, "13:00 触发对应的 12:55 不是交易桶"},
		{cst("2026-10-12", "13:00"), true, "下午第一根"},
		{cst("2026-10-12", "14:55"), true, "收盘前最后一根"},
		{cst("2026-10-12", "15:00"), false, "收盘后"},
		{cst("2026-10-11", "10:00"), false, "周日"},
		{cst("2026-10-01", "10:00"), false, "国庆休市"},
	}
	for _, tc := range cases {
		got, err := TradingBar("stockcn", "5m", tc.at)
		require.NoError(t, err, tc.note)
		require.Equal(t, tc.want, got, tc.note)
	}
}

func TestTradingBarStockCN1mAndCrypto(t *testing.T) {
	for at, want := range map[time.Time]bool{
		cst("2026-10-12", "09:30"): true, cst("2026-10-12", "11:29"): true, cst("2026-10-12", "11:30"): false,
		cst("2026-10-12", "13:00"): true, cst("2026-10-12", "14:59"): true, cst("2026-10-12", "15:00"): false,
	} {
		got, err := TradingBar("stockcn", "1m", at)
		require.NoError(t, err)
		require.Equal(t, want, got, at)
	}
	// 连续交易的市场每根都是交易桶，未登记的市场也按连续交易处理。
	for _, market := range []string{"crypto", "unknown"} {
		got, err := TradingBar(market, "5m", cst("2026-10-11", "12:55"))
		require.NoError(t, err)
		require.True(t, got, market)
	}
}

func TestTradingBarReportsCalendarCoverageErrors(t *testing.T) {
	_, err := TradingBar("stockcn", "5m", cst("2028-01-05", "10:00"))
	require.Error(t, err, "超出覆盖期必须报错，不能当作非交易")
}

func TestCheckCalendarHorizon(t *testing.T) {
	// 内嵌日历覆盖到 2027-12-31。
	require.NoError(t, CheckCalendarHorizon("stockcn", cst("2026-11-04", "10:00"), 14))
	require.ErrorIs(t, CheckCalendarHorizon("stockcn", cst("2027-11-04", "10:00"), 60), ErrCalendarExpiringSoon)
	require.ErrorIs(t, CheckCalendarHorizon("stockcn", cst("2027-12-31", "14:59"), 14), ErrCalendarExpiringSoon)
	require.ErrorIs(t, CheckCalendarHorizon("stockcn", cst("2028-01-01", "00:00"), 14), ErrCalendarExpired)
	require.NoError(t, CheckCalendarHorizon("crypto", cst("2030-01-01", "00:00"), 14), "连续交易的市场没有日历")
}
