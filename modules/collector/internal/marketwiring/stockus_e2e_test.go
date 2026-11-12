package marketwiring

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// 美股的采集计划参数（每函数 50 只、收盘后等 5 秒）目前还没写进 market.yaml 的 scf_plan（U5 才加），这里用测试夹具构造：
// 与 Collector 装配采集计划时相同——函数内并发由美股的路由与东方财富的限速配置推导。
func usPlanOptions() marketfetch.PlanOptions {
	return marketfetch.PlanOptions{SpaceID: "stockus", Markets: map[string][]string{"equity": {"ap-nanjing"}}, DefaultLatenessS: 90,
		SymbolsPerFunction: 50, Fetch: marketfetch.PlanFetch{RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, NotBeforeMS: 5000},
		DeriveFetch: marketfetch.RouteFetchLimits("stockus")}
}

// usGroup 按 Collector 由任务和标签成员拼分组的做法造一个美股分组：source_id 取行情源目录的登记，
// 传给数据源的代码由美股的标的规则换出（BRK.B.XNYS → BRK.B）。
func usGroup(subjects ...string) marketfetch.TaskGroup {
	symbols := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		symbol, err := ResolveSymbol("eastmoney", "stockus", "equity", subject)
		if err != nil {
			panic(err)
		}
		symbols[subject] = symbol
	}
	return marketfetch.TaskGroup{Provider: "eastmoney", SourceID: DefaultSourceID("stockus", "eastmoney", "equity"), MarketType: "equity", MarketID: "stockus", InstrumentType: "equity",
		DatasetID: usDataset, Frequency: "5m", Subjects: subjects, ExternalSymbols: symbols}
}

// 从采集计划到云函数入参再到写入：120 只标的（含带点的 BRK.B、ETF 的 XASE）、每函数 50 只，得到 3 个函数（50、50、20），都在南京，收盘后等 5 秒。
// 美股的来源按路由分配，所以函数的请求里不带 provider、source_id；请求交给流水线后由路由选中东方财富，写出的行上是 eastmoney、stockus_http；
// 发给东方财富的 secid 由标的的交易所与代码换出。
func TestStockUSPlanToFunctionRequests(t *testing.T) {
	subjects := []string{"BRK.B.XNYS", "SPY.XASE"}
	for i := 0; len(subjects) < 120; i++ {
		subjects = append(subjects, fmt.Sprintf("A%c%c.XNAS", 'A'+i/26, 'A'+i%26))
	}
	group := usGroup(subjects...)
	require.Equal(t, "stockus_http", group.SourceID, "source_id 取自行情源目录的登记")
	require.Equal(t, "BRK.B", group.ExternalSymbols["BRK.B.XNYS"])
	desired, err := marketfetch.BuildDesiredState(usPlanOptions(), []marketfetch.TaskGroup{group}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return usTime("2026-11-10 22:30:00").Unix(), nil })
	require.NoError(t, err)
	require.Len(t, content.Functions, 3)

	scheduled := usTime("2026-11-12 22:50:00")
	body := usTestdata(t, "kline_5m_AAPL_boundary_2250.json")
	storage := newHKRowStore()
	var secidsMu sync.Mutex
	secids := map[string]bool{}
	var sizes []int
	for _, name := range content.SortedFunctionNames() {
		plan := content.FinalPlan(content.Functions[name])
		require.NotNil(t, plan, name)
		require.Equal(t, marketfetch.PlanMarket{MarketID: "stockus", InstrumentType: "equity", Provider: "eastmoney", SourceID: "stockus_http", MarketType: "equity"}, plan.Market, name)
		require.Equal(t, 5000, plan.Fetch.NotBeforeMS, name)
		require.Equal(t, 16, plan.Fetch.Concurrency, name)
		require.Equal(t, 5*time.Second, plan.StartDelay(scheduled, scheduled), "定时器准点触发时等 5 秒再取数")
		require.Contains(t, name, "nanjing", "函数名带地域")

		planned, err := plan.BuildRequests("stockus", name, "req-"+name, scheduled, nil)
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
		for _, item := range request.Items {
			require.Equal(t, group.ExternalSymbols[item.SubjectID], item.Symbol)
		}

		request.FetchLimits = plan.Fetch.Limits()
		pipeline := stockUSPipeline(t, storage, usTime("2026-11-12 22:50:05"), request.FetchLimits, func(w http.ResponseWriter, r *http.Request) {
			secidsMu.Lock()
			secids[r.URL.Query().Get("secid")] = true
			secidsMu.Unlock()
			_, _ = w.Write(body)
		})
		payload, err := pipeline.Execute(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	require.Equal(t, []int{50, 50, 20}, sizes)

	require.Len(t, storage.rows, 120, "三个函数合起来把 120 只都写了")
	for _, subject := range subjects {
		fields := storage.rows[hkRowKey(subject, "2026-11-12T14:45:00Z")]
		require.NotNil(t, fields, subject)
		require.Equal(t, "eastmoney", fields["provider_id"].GetStringValue(), subject)
		require.Equal(t, "stockus_http", fields["source_id"].GetStringValue(), subject)
		require.Equal(t, "stockus_equity_kline_5m", fields["route_id"].GetStringValue(), subject)
	}
	require.True(t, secids["106.BRK_B"], "带点的代码换成下划线")
	require.True(t, secids["107.SPY"])
	require.True(t, secids["105.AAA"])
}

func usPlan(t *testing.T) (*marketfetch.Plan, string) {
	t.Helper()
	desired, err := marketfetch.BuildDesiredState(usPlanOptions(), []marketfetch.TaskGroup{usGroup("AAPL.XNAS", "BRK.B.XNYS")}, nil)
	require.NoError(t, err)
	// 计划份的生效起点设在这些时刻之前，返回 no_work 只可能是因为周期不是交易桶。
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return usTime("2026-10-20 00:00:00").Unix(), nil })
	require.NoError(t, err)
	require.Len(t, content.Functions, 1)
	for name, function := range content.Functions {
		plan := content.FinalPlan(function)
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

// 云函数在这些时刻被定时器触发时直接返回 no_work，不连 Access、不取数：开盘前、收市后、周末、节假日（2026-11-26）、半日市 13:00 之后。
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
		planned, err := plan.BuildRequests("stockus", name, "req", usTime(scheduled), nil)
		require.NoError(t, err, scheduled)
		require.Len(t, planned, 1, scheduled)
		require.True(t, planned[0].Period.Equal(usTime(period)), scheduled)
		require.True(t, planPeriodValid(plan.Market, planned[0].Frequency, planned[0].Period), scheduled)
	}
	require.False(t, strings.Contains(name, "stockhk"))
}
