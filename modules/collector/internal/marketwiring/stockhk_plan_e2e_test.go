package marketwiring

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// 从采集计划到云函数入参再到写入：95 只标的、每函数 25 只，得到 4 个函数（25、25、25、20），都在南京，收盘后等 5 秒。
// 港股的来源按路由分配，所以函数的请求里不带 provider、source_id（计划的市场信息里记着 eastmoney / stockhk_http）；
// 请求交给流水线后由路由选中东方财富，写出的行上是 eastmoney、stockhk_http。
func TestStockHKPlanToFunctionRequests(t *testing.T) {
	subjects := make([]string, 0, 95)
	for i := 1; i <= 95; i++ {
		subjects = append(subjects, fmt.Sprintf("%05d.XHKG", i))
	}
	group := hkE2EGroup(subjects...)
	require.Equal(t, "stockhk_http", group.SourceID, "source_id 取自行情源目录的登记")
	desired, err := marketfetch.BuildDesiredState(hkE2EPlanOptions(), []marketfetch.TaskGroup{group}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return hkTime("2026-11-10 09:30:00").Unix(), nil })
	require.NoError(t, err)
	require.Len(t, content.Functions, 4)

	scheduled := hkTime("2026-11-12 14:50:00")
	body := []byte(`{"data":{"klines":["2026-11-12 14:50,20.460,20.420,20.460,20.420,24000,490280.000"]}}`)
	storage := newHKRowStore()
	var sizes []int
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		plan := content.FinalPlan(function)
		require.NotNil(t, plan, name)
		require.Equal(t, marketfetch.PlanMarket{MarketID: "stockhk", InstrumentType: "equity", Provider: "eastmoney", SourceID: "stockhk_http", MarketType: "equity"}, plan.Market, name)
		require.Equal(t, 5000, plan.Fetch.NotBeforeMS, name)
		require.Equal(t, 16, plan.Fetch.Concurrency, name)
		require.Equal(t, 5*time.Second, plan.StartDelay(scheduled, scheduled), "定时器准点触发时等 5 秒再取数")
		require.Contains(t, name, "nanjing", "函数名带地域")

		planned, err := plan.BuildRequests("stockhk", name, "req-"+name, scheduled, nil)
		require.NoError(t, err)
		require.Len(t, planned, 1, name)
		require.True(t, planned[0].Period.Equal(hkTime("2026-11-12 14:45:00")), "14:50 触发采 14:45 开始的那根")
		request := planned[0].Request
		require.Equal(t, "stockhk", request.SpaceID)
		require.Equal(t, "stockhk", request.MarketID)
		require.Equal(t, "equity", request.InstrumentType)
		require.Equal(t, "5m", request.Frequency)
		require.Empty(t, request.Provider, "来源按路由分配，请求不指定数据源")
		require.Empty(t, request.SourceID)
		sizes = append(sizes, len(request.Items))
		for _, item := range request.Items {
			require.Equal(t, strings.TrimSuffix(item.SubjectID, ".XHKG"), item.Symbol, "传给数据源的代码是五位数字")
		}

		request.FetchLimits = plan.Fetch.Limits()
		pipeline := stockHKPipeline(t, storage, hkTime("2026-11-12 14:50:05"), request.FetchLimits, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
		payload, err := pipeline.Execute(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	require.Equal(t, []int{25, 25, 25, 20}, sizes)

	require.Len(t, storage.rows, 95, "四个函数合起来把 95 只都写了")
	for _, subject := range subjects {
		fields := storage.rows[hkRowKey(subject, "2026-11-12T06:45:00Z")]
		require.NotNil(t, fields, subject)
		require.Equal(t, "eastmoney", fields["provider_id"].GetStringValue(), subject)
		require.Equal(t, "stockhk_http", fields["source_id"].GetStringValue(), subject)
		require.Equal(t, "stockhk_equity_kline_5m", fields["route_id"].GetStringValue(), subject)
	}
}
