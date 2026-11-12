package marketwiring

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

func hkPeriodTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, time.FixedZone("HKT", 8*3600))
	require.NoError(t, err)
	return parsed.UTC()
}

// 港股 5m 的周期（K 线开始时刻）是否要采：交易时段内的要采，午休、收市后、半日市 12:00 之后、假期、周末的不采。
func TestPlanPeriodValidStockHK(t *testing.T) {
	market := marketfetch.PlanMarket{MarketID: "stockhk"}
	for period, want := range map[string]bool{
		// 2026-11-12（周四）。
		"2026-11-12 09:25": false, // 开盘前
		"2026-11-12 09:30": true,
		"2026-11-12 11:55": true, // 12:00 触发采的那根
		"2026-11-12 12:00": false,
		"2026-11-12 12:30": false,
		"2026-11-12 12:55": false, // 13:00 触发对应的周期
		"2026-11-12 13:00": true,
		"2026-11-12 15:55": true, // 16:00 触发采的那根
		"2026-11-12 16:00": false,
		"2026-11-12 16:10": false,
		// 半日市 2026-12-24：只有上午。
		"2026-12-24 11:55": true,
		"2026-12-24 12:00": false,
		"2026-12-24 13:00": false,
		"2026-12-24 15:55": false,
		// 圣诞节假期与周末。
		"2026-12-25 10:00": false,
		"2026-11-14 10:00": false,
	} {
		require.Equal(t, want, planPeriodValid(market, "5m", hkPeriodTime(t, period)), period)
	}
}

// 云函数在这些时刻被定时器触发时直接返回 no_work，不连 Access、不取数。
func TestStockHKTimerReturnsNoWorkOutsideSessions(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockhk")
	options := marketfetch.PlanOptions{SpaceID: "stockhk", Markets: map[string][]string{"equity": {"ap-nanjing"}}, DefaultLatenessS: 90,
		Fetch: marketfetch.PlanFetch{Concurrency: 16, NotBeforeMS: 5000}}
	desired, err := marketfetch.BuildDesiredState(options, []marketfetch.TaskGroup{{
		Provider: "eastmoney", SourceID: "stockhk_http", MarketType: "equity", MarketID: "stockhk", InstrumentType: "equity",
		DatasetID: "ds_hk_5m", Frequency: "5m", Subjects: []string{"00005.XHKG", "00700.XHKG"},
	}}, nil)
	require.NoError(t, err)
	require.Len(t, desired.Functions, 1)
	var plan *marketfetch.Plan
	var name string
	for functionName, function := range desired.Functions {
		name, plan = functionName, function.Plan
	}
	// 计划份的生效起点设在这些时刻之前，返回 no_work 只可能是因为周期不是交易桶。
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return hkPeriodTime(t, "2026-11-10 09:30").Unix(), nil })
	require.NoError(t, err)
	plan = content.FinalPlan(content.Functions[name])
	require.NotNil(t, plan)

	handler := NewHandler()
	// 触发时刻是周期的收盘时刻：12:05 触发对应 12:00～12:05 这个周期。
	for _, scheduled := range []string{
		"2026-11-12 12:05", "2026-11-12 12:30", "2026-11-12 13:00", // 午休
		"2026-11-12 16:05", "2026-11-12 16:15", "2026-11-12 20:00", // 收市后
		"2026-12-24 12:05", "2026-12-24 13:05", "2026-12-24 16:00", // 半日市 12:00 之后
		"2026-12-25 10:00", "2026-12-25 16:00", // 假期
		"2026-11-14 10:00", // 周末
	} {
		at := hkPeriodTime(t, scheduled)
		response, err := handler.RunPlan(context.Background(), plan, marketfetch.PlanRun{RequestID: "req", FunctionName: name, Scheduled: at, Entry: at})
		require.NoError(t, err, scheduled)
		require.True(t, response.Success, scheduled)
		require.Equal(t, "no_work", response.Message, scheduled)
	}
	// 对照：交易时段内的触发（12:00 触发采 11:55 那根）计划里有这一期，周期判定也放行，不会返回 no_work。
	planned, err := plan.BuildRequests("stockhk", name, "req", hkPeriodTime(t, "2026-11-12 12:00"), nil)
	require.NoError(t, err)
	require.Len(t, planned, 1)
	require.True(t, planPeriodValid(plan.Market, planned[0].Frequency, planned[0].Period))
}
