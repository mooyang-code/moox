package marketwiring

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

// usPlanOptions 与 Collector 装配采集计划时相同：每函数标的数、收盘后等待时间读编进二进制的 config/markets/stockus/market.yaml
// 的 scf_plan（部署配置只写代码包与命名空间时的默认值），函数内并发由美股的路由与 CNBC 的限速配置推导。
func usPlanOptions() fetchplan.PlanOptions {
	config, err := markets.ConfigFor("stockus")
	if err != nil {
		panic(err)
	}
	var plan struct {
		SymbolsPerFunction int `yaml:"symbols_per_function"`
		Fetch              struct {
			RequestTimeoutMS int `yaml:"request_timeout_ms"`
			StorageTimeoutMS int `yaml:"storage_timeout_ms"`
			NotBeforeMS      int `yaml:"not_before_ms"`
		} `yaml:"fetch"`
	}
	if err := config.SCFPlan.Decode(&plan); err != nil {
		panic(err)
	}
	return fetchplan.PlanOptions{SpaceID: "stockus", Markets: map[string][]string{"equity": {"ap-nanjing"}}, DefaultLatenessS: config.AllowedLatenessSeconds,
		SymbolsPerFunction: plan.SymbolsPerFunction,
		Fetch:              marketfetch.PlanFetch{RequestTimeoutMS: plan.Fetch.RequestTimeoutMS, StorageTimeoutMS: plan.Fetch.StorageTimeoutMS, NotBeforeMS: plan.Fetch.NotBeforeMS},
		DeriveFetch:        fetchplan.RouteFetchLimits("stockus")}
}

// usTime 把北京时间换成 UTC：定时器触发时刻、计划生效起点按北京时间写。
func usTime(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("CST", 8*3600))
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// usGroup 按 Collector 由任务和标签成员拼分组的做法造一个美股分组：provider 是标签的 sync.source（东方财富，标的同步用它对照交易所），
// source_id 先查行情源目录的登记——目录里东方财富已经不是美股的 K 线来源，查不到，按分组的做法退回 provider。
// K 线来源由路由分配，与分组的 provider、source_id 无关。
func usGroup(subjects ...string) fetchplan.TaskGroup {
	sourceID := DefaultSourceID("stockus", "eastmoney", "equity")
	if sourceID == "" {
		sourceID = "eastmoney"
	}
	return fetchplan.TaskGroup{Provider: "eastmoney", SourceID: sourceID, MarketType: "equity", MarketID: "stockus", InstrumentType: "equity",
		DatasetID: usDataset, Frequency: "5m", Subjects: subjects}
}

// 从采集计划到云函数入参再到写入：标普 500 的 503 只标的（含带点的 BRK.B、ETF 的 XASE）、每函数 50 只，得到 11 个函数（10 个 50 只、1 个 3 只），都在南京，收盘后等 5 秒。
// 美股的来源按路由分配，所以函数的请求里不带 provider、source_id；请求交给流水线后由路由选中 CNBC，写出的行上是 cnbc、stockus_cnbc_http；
// 发给 CNBC 的请求路径里是标的规则换出的代码（带类别的保留点）。
func TestStockUSPlanToFunctionRequests(t *testing.T) {
	subjects := []string{"BRK.B.XNYS", "SPY.XASE"}
	for i := 0; len(subjects) < 503; i++ {
		subjects = append(subjects, fmt.Sprintf("A%c%c.XNAS", 'A'+i/26, 'A'+i%26))
	}
	group := usGroup(subjects...)
	require.Equal(t, "eastmoney", group.SourceID, "目录里没有美股东方财富 K 线的登记，source_id 退回 provider")
	desired, err := fetchplan.BuildDesiredState(usPlanOptions(), []fetchplan.TaskGroup{group}, nil)
	require.NoError(t, err)
	content, _, _, err := fetchplan.BuildPlanContent(nil, nil, desired, fetchplan.PlanTiming{Now: usTime("2026-11-10 22:30:00")})
	require.NoError(t, err)
	require.Len(t, content.Functions, 11)

	scheduled := usTime("2026-11-12 22:50:00")
	server := newUSCNBCServer(t, usSameMinutes(usSessionMinutes(usDay{"2026-11-12", "16:00"})))
	storage := newHKRowStore()
	var sizes []int
	for _, name := range content.SortedFunctionNames() {
		plan := content.Functions[name].Target
		require.NotNil(t, plan, name)
		require.Equal(t, marketfetch.PlanMarket{MarketID: "stockus", InstrumentType: "equity", Provider: "eastmoney", SourceID: "eastmoney", MarketType: "equity"}, plan.Market, name)
		require.Equal(t, 5000, plan.Fetch.NotBeforeMS, name)
		require.Equal(t, 16, plan.Fetch.Concurrency, name)
		require.Equal(t, 5*time.Second, plan.StartDelay(scheduled, scheduled), "定时器准点触发时等 5 秒再取数")
		require.Contains(t, name, "nanjing", "函数名带地域")

		planned, err := plan.BuildRequests("stockus", name, "req-"+name, scheduled)
		require.NoError(t, err)
		require.Len(t, planned, 1, name)
		require.True(t, planned[0].Period.Equal(usTime("2026-11-12 22:45:00")), "22:50 触发采 22:45 开始的那根")
		request := planned[0].Request
		require.Equal(t, "stockus", request.SpaceID)
		require.Equal(t, "stockus", request.MarketID)
		require.Equal(t, "equity", request.InstrumentType)
		require.Equal(t, "5m", request.Frequency)
		require.Empty(t, request.Provider, "来源按路由分配，请求不指定数据源")
		require.Empty(t, request.SourceID)
		sizes = append(sizes, len(request.Items))

		request.FetchLimits = plan.Fetch.Limits()
		pipeline := stockUSPipeline(t, storage, usTime("2026-11-12 22:50:05"), request.FetchLimits, server)
		payload, err := pipeline.Execute(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, "succeeded", payload.Status, payload.ErrorSummary)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	require.Equal(t, []int{50, 50, 50, 50, 50, 50, 50, 50, 50, 50, 3}, sizes)

	require.Len(t, storage.rows, 503, "11 个函数合起来把 503 只都写了")
	for _, subject := range subjects {
		fields := storage.rows[hkRowKey(subject, "2026-11-12T14:45:00Z")]
		require.NotNil(t, fields, subject)
		require.Equal(t, "cnbc", fields["provider_id"].GetStringValue(), subject)
		require.Equal(t, "stockus_cnbc_http", fields["source_id"].GetStringValue(), subject)
		require.Equal(t, "estimated", fields["amount_quality"].GetStringValue(), subject)
		require.Equal(t, "stockus_equity_kline_5m", fields["route_id"].GetStringValue(), subject)
	}
	require.Len(t, server.requested(), 503, "每只标的一次请求")
	for _, code := range []string{"BRK.B", "SPY", "AAA"} {
		require.Equal(t, 1, server.requestsFor(code), "带类别的代码保留点："+code)
	}
	require.Contains(t, server.requested(), usBarsPath("BRK.B", "202611120945", "202611120949"), "22:50（北京）是纽约 09:50，取 09:45 那根")
}

func usPlan(t *testing.T) (*marketfetch.Plan, string) {
	t.Helper()
	desired, err := fetchplan.BuildDesiredState(usPlanOptions(), []fetchplan.TaskGroup{usGroup("AAPL.XNAS", "BRK.B.XNYS")}, nil)
	require.NoError(t, err)
	// 计划份的生效起点设在这些时刻之前，返回 no_work 只可能是因为周期不是交易桶。
	content, _, _, err := fetchplan.BuildPlanContent(nil, nil, desired, fetchplan.PlanTiming{Now: usTime("2026-10-20 00:00:00")})
	require.NoError(t, err)
	require.Len(t, content.Functions, 1)
	for name, function := range content.Functions {
		plan := function.Target
		require.NotNil(t, plan)
		return plan, name
	}
	return nil, ""
}

// 美股 5m 的周期（K 线开始时刻，北京时间）是否要采：交易时段内的要采，开盘前、收市后、半日市 13:00 之后、假期、周末的不采。
func TestPlanPeriodValidStockUS(t *testing.T) {
	market := marketfetch.PlanMarket{MarketID: "stockus"}
	for period, want := range map[string]bool{
		// 冬令时 2026-11-12（周四）：纽约 09:30～16:00 = 北京 22:30～次日 05:00。
		"2026-11-12 22:25": false, // 开盘前
		"2026-11-12 22:30": true,  // 22:35 触发采的首根
		"2026-11-13 04:55": true,  // 次日 05:00 触发采的末根
		"2026-11-13 05:00": false,
		"2026-11-12 12:00": false,
		// 夏令时 2026-10-26（周一）：北京 21:30～次日 04:00。
		"2026-10-26 21:25": false,
		"2026-10-26 21:30": true,
		"2026-10-27 03:55": true,
		"2026-10-27 04:00": false,
		// 半日市 2026-11-27：只到纽约 13:00 = 北京次日 02:00。
		"2026-11-27 22:30": true,
		"2026-11-28 01:55": true,
		"2026-11-28 02:00": false,
		"2026-11-28 03:00": false,
		// 感恩节 2026-11-26 全天休市：北京 11-26 22:30 起（纽约 11-26 09:30）与 11-27 凌晨（纽约 11-26 下午）。
		"2026-11-26 22:30": false,
		"2026-11-27 03:00": false,
		// 周末：北京周六 22:30、周日 22:30 是纽约的周六、周日。
		"2026-11-14 22:30": false,
		"2026-11-15 22:30": false,
	} {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", period, time.FixedZone("CST", 8*3600))
		require.NoError(t, err)
		require.Equal(t, want, planPeriodValid(market, "5m", parsed.UTC()), period)
	}
}

// 云函数在这些时刻被定时器触发时直接返回 no_work，不连外部入口、不取数：开盘前、收市后、周末、节假日（2026-11-26）、半日市 13:00 之后。
// 触发时刻是周期的收盘时刻（北京时间）：22:35 触发对应 22:30～22:35 这个周期。
func TestStockUSTimerReturnsNoWorkOutsideSessions(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockus")
	plan, name := usPlan(t)
	handler := NewHandler()
	for _, scheduled := range []string{
		"2026-11-12 22:30", "2026-11-12 21:35", "2026-11-12 12:00", // 冬令时开盘前（首根 22:35 才有触发）
		"2026-11-13 05:05", "2026-11-13 05:30", "2026-11-13 12:00", // 冬令时收市后
		"2026-10-26 21:30", "2026-10-27 04:05", // 夏令时开盘前、收市后
		"2026-11-14 22:40", "2026-11-15 22:40", "2026-11-15 23:00", // 周末（纽约的周六、周日）
		"2026-11-26 22:40", "2026-11-27 03:00", // 感恩节 11-26 全天休市
		"2026-11-28 02:05", "2026-11-28 03:00", "2026-11-28 04:55", // 半日市 13:00 收市之后
	} {
		at := usTime(scheduled + ":00")
		response, err := handler.RunPlan(context.Background(), plan, marketfetch.PlanRun{RequestID: "req", FunctionName: name, Scheduled: at, Entry: at})
		require.NoError(t, err, scheduled)
		require.True(t, response.Success, scheduled)
		require.Equal(t, "no_work", response.Message, scheduled)
	}
	// 对照：交易时段内的触发计划里有这一期，周期判定也放行，不会返回 no_work。
	for scheduled, period := range map[string]string{
		"2026-11-12 22:35:00": "2026-11-12 22:30:00", // 冬令时首根
		"2026-11-13 05:00:00": "2026-11-13 04:55:00", // 冬令时末根
		"2026-10-26 21:35:00": "2026-10-26 21:30:00", // 夏令时首根
		"2026-10-27 04:00:00": "2026-10-27 03:55:00", // 夏令时末根
		"2026-11-28 02:00:00": "2026-11-28 01:55:00", // 半日市末根
	} {
		planned, err := plan.BuildRequests("stockus", name, "req", usTime(scheduled))
		require.NoError(t, err, scheduled)
		require.Len(t, planned, 1, scheduled)
		require.True(t, planned[0].Period.Equal(usTime(period)), scheduled)
		require.True(t, planPeriodValid(plan.Market, planned[0].Frequency, planned[0].Period), scheduled)
	}
	require.False(t, strings.Contains(name, "stockhk"))
}
