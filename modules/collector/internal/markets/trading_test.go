package markets

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
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
	// 连续交易的市场每根都是交易桶。
	got, err := TradingBar("crypto", "5m", cst("2026-10-11", "12:55"))
	require.NoError(t, err)
	require.True(t, got)
}

// 未登记的市场一律报错，不当成连续交易。
func TestUnknownMarketIsAnError(t *testing.T) {
	at := cst("2026-10-11", "12:55")
	_, err := TradingBar("unknown", "5m", at)
	require.ErrorIs(t, err, barclock.ErrUnknownMarket)
	_, err = Clock("unknown", "5m")
	require.ErrorIs(t, err, barclock.ErrUnknownMarket)
	_, err = Location("unknown")
	require.ErrorIs(t, err, barclock.ErrUnknownMarket)
	require.ErrorIs(t, CheckCalendarHorizon("unknown", at, 14), barclock.ErrUnknownMarket)
	_, err = ConfigFor("unknown")
	require.ErrorIs(t, err, ErrUnknownMarket)
	_, err = RulesFor("unknown")
	require.ErrorIs(t, err, ErrUnknownMarket)
}

// 现有两个市场的参数：允许迟到时间与序列标签是线上行为，不能变。
func TestMarketConfigs(t *testing.T) {
	require.Equal(t, []string{"crypto", "stockcn", "stockhk"}, IDs())

	crypto, err := ConfigFor("crypto")
	require.NoError(t, err)
	require.Equal(t, 15, crypto.AllowedLatenessSeconds)
	require.Empty(t, crypto.SeriesTag, "crypto 的序列标签按数据源生成")
	require.Empty(t, crypto.Currency)
	require.False(t, crypto.RealtimeBatchNoCap)
	require.Equal(t, "binance", crypto.EgressProbe)
	require.Equal(t, SubjectEgressProxy, crypto.SubjectEgress)
	require.Equal(t, &SubjectSync{Sources: []string{"binance"}, InstrumentType: "spot", Cron: "10 8 * * *", Timezone: "Asia/Shanghai"}, crypto.SubjectSync)

	stockcn, err := ConfigFor(" StockCN ")
	require.NoError(t, err)
	require.Equal(t, 90, stockcn.AllowedLatenessSeconds)
	require.Equal(t, "default", stockcn.SeriesTag)
	require.Equal(t, "cny", stockcn.AmountUnit())
	require.True(t, stockcn.RealtimeBatchNoCap)
	require.Equal(t, EgressProbePublicIP, stockcn.EgressProbe)
	require.Equal(t, SubjectEgressDirect, stockcn.SubjectEgress)
	require.Equal(t, &SubjectSync{Sources: []string{"eastmoney"}, InstrumentType: "equity", Cron: "10 8 * * *", Timezone: "Asia/Shanghai"}, stockcn.SubjectSync)
}

func TestParseConfigRejectsIncompleteFiles(t *testing.T) {
	for name, raw := range map[string]string{
		"市场不符":     "market_id: other\nallowed_lateness_seconds: 60\nsubject_egress: direct\n",
		"缺允许迟到时间":  "market_id: demo\nsubject_egress: direct\n",
		"缺出口方式":    "market_id: demo\nallowed_lateness_seconds: 60\n",
		"未知字段":     "market_id: demo\nallowed_lateness_seconds: 60\nsubject_egress: direct\ntimezone: UTC\n",
		"标的同步没有来源": "market_id: demo\nallowed_lateness_seconds: 60\nsubject_egress: direct\nsubject_sync: {cron: \"0 8 * * *\"}\n",
	} {
		_, err := parseConfig("demo", []byte(raw))
		require.Error(t, err, name)
	}
	// subject_sync、scf_plan 都可以不写。
	config, err := parseConfig("demo", []byte("market_id: demo\nallowed_lateness_seconds: 60\nsubject_egress: direct\n"))
	require.NoError(t, err)
	require.Nil(t, config.SubjectSync)
	require.True(t, config.SCFPlan.IsZero())
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

// 日线的桶键是交易日期的 UTC 零点：只有交易日才有桶，上海零点（前一日 16:00 UTC）不是桶键。
func TestTradingBarStockCN1dUsesDateKeys(t *testing.T) {
	day := func(month time.Month, d int) time.Time { return time.Date(2026, month, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		at   time.Time
		want bool
		note string
	}{
		{day(10, 12), true, "周一"},
		{day(10, 8), true, "国庆后第一个交易日"},
		{day(10, 7), false, "国庆休市最后一天"},
		{day(10, 10), false, "周六"},
		{day(10, 11), false, "周日"},
		{day(10, 1), false, "国庆休市"},
		{day(10, 12).Add(-8 * time.Hour), false, "上海零点不是日线的键"},
		{cst("2026-10-12", "09:30"), false, "开盘时刻不是日线的键"},
	}
	for _, tc := range cases {
		got, err := TradingBar("stockcn", "1d", tc.at)
		require.NoError(t, err, tc.note)
		require.Equal(t, tc.want, got, tc.note)
	}
}
