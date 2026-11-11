package sinafinance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func day(value string) time.Time {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// recorded 用 2026-11-11 录制的真实响应回答请求（每期只保留用到的科目和几个用不到的）：
// 贵州茅台取了 12 期，平安银行、中国平安、中信证券各取 6 期；其他股票返回“没有这只股票”的响应。
type recorded struct {
	t *testing.T
	// fail 是要返回 500 的报表（lrb、fzb、llb）。
	fail string

	mu       sync.Mutex
	requests []*http.Request
}

func (r *recorded) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	query := req.URL.Query()
	if query.Get("source") == r.fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	body, err := os.ReadFile("testdata/" + query.Get("paperCode") + "_" + query.Get("source") + ".json")
	if err != nil {
		body, err = os.ReadFile("testdata/unknown.json")
	}
	require.NoError(r.t, err)
	_, _ = w.Write(body)
}

func newProvider(t *testing.T, handler *recorded) *Provider {
	t.Helper()
	handler.t = t
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Config{BaseURL: server.URL, Now: func() time.Time { return day("2026-11-11") }})
}

func fetch(t *testing.T, provider *Provider, subject, start, end string) map[time.Time]map[string]marketdata.FieldValue {
	t.Helper()
	values, err := provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: subject, Frequency: "1d", Start: day(start), End: day(end)})
	require.NoError(t, err)
	return values
}

// assertRolling 比较滚动值：三项相加减有浮点误差，允许 0.01 以内。
func assertRolling(t *testing.T, row map[string]marketdata.FieldValue, id string, want float64) {
	t.Helper()
	require.Contains(t, row, id)
	assert.Equal(t, marketdata.FieldTypeDouble, row[id].Type)
	assert.InDelta(t, want, row[id].Double, 0.01, id)
}

func period(row map[string]marketdata.FieldValue) int64 { return row[FieldReportPeriod].Int }

// 贵州茅台：2026 年三季报 10-31 披露、2025 年年报 2026-04-17 披露（都在最近四期内，按真实披露日生效）。
func TestRecentReportsTakeEffectAfterRealDisclosureDate(t *testing.T) {
	provider := newProvider(t, &recorded{})
	values := fetch(t, provider, "600519.XSHG", "2026-04-16", "2026-11-03")

	// 披露日当天那一根还用上一期，下一个交易日起用新的一期。
	assert.EqualValues(t, 20250930, period(values[day("2026-04-17")]), "年报披露日当天还是上一期")
	assert.EqualValues(t, 20251231, period(values[day("2026-04-20")]), "年报披露后的下一个交易日")
	assert.EqualValues(t, 20260630, period(values[day("2026-10-30")]))
	assert.EqualValues(t, 20260930, period(values[day("2026-11-02")]), "三季报 10-31 披露")

	row := values[day("2026-11-02")]
	assert.Equal(t, marketdata.DoubleField(133283652508.95), row["fin_total_revenue"])
	assert.Equal(t, marketdata.DoubleField(130970743649.04), row["fin_revenue"])
	assert.Equal(t, marketdata.DoubleField(88979846257.79), row["fin_operating_profit"])
	assert.Equal(t, marketdata.DoubleField(66717143218.04), row["fin_net_profit"])
	assert.Equal(t, marketdata.DoubleField(64467425981.95), row["fin_net_profit_parent"])
	assert.Equal(t, marketdata.DoubleField(51.53), row["fin_eps_basic"])
	assert.Equal(t, marketdata.DoubleField(318304891882.72), row["fin_total_assets"])
	assert.Equal(t, marketdata.DoubleField(37883274859.87), row["fin_total_liabilities"])
	assert.Equal(t, marketdata.DoubleField(271204740541.63), row["fin_equity_parent"])
	assert.Equal(t, marketdata.DoubleField(84058979832.29), row["fin_operating_cash_flow"])
	// 滚动十二个月 = 2026 年前三季度 + 2025 年年报 − 2025 年前三季度。
	assertRolling(t, row, "fin_total_revenue_ttm", 133283652508.95+172054171890.91-130903889634.88)
	assertRolling(t, row, "fin_net_profit_parent_ttm", 64467425981.95+82320067101.68-64626746712.18)
	assertRolling(t, row, "fin_eps_basic_ttm", 51.53+65.66-51.53)
	assertRolling(t, row, "fin_operating_cash_flow_ttm", 84058979832.29+61522204989.35-38196802155.27)
	assert.Len(t, row, 18, "报告期、10 个科目、7 个滚动值")
	for _, id := range []string{"fin_total_assets_ttm", "fin_total_liabilities_ttm", "fin_equity_parent_ttm"} {
		assert.NotContains(t, row, id, "时点科目没有滚动值")
	}

	// 本期是年报时滚动值就是年报值。
	annual := values[day("2026-04-20")]
	assert.Equal(t, marketdata.DoubleField(172054171890.91), annual["fin_total_revenue"])
	assert.Equal(t, annual["fin_total_revenue"], annual["fin_total_revenue_ttm"])
	assert.Equal(t, marketdata.DoubleField(65.66), annual["fin_eps_basic_ttm"])
}

// 2024 年年报在三张表里的 publish_date 都是 2026-04-17（被 2025 年年报重新列示），按法定截止日 2025-04-30 生效；
// 2025 年一季报在资产负债表里还保留着首次披露日 2025-04-30。两期同一天生效，取报告期大的一季报。
func TestOlderReportsTakeEffectAfterStatutoryDeadline(t *testing.T) {
	provider := newProvider(t, &recorded{})
	values := fetch(t, provider, "600519.XSHG", "2025-04-29", "2025-05-07")

	assert.EqualValues(t, 20240930, period(values[day("2025-04-30")]), "截止日当天年报还没生效")
	row := values[day("2025-05-06")]
	assert.EqualValues(t, 20250331, period(row), "年报与一季报同日生效时取一季报")
	// 一季报的滚动值用到 2024 年年报：它已按截止日生效，而不是等到 2026-04-17。
	assertRolling(t, row, "fin_total_revenue_ttm", 51443450583.77+174144069958.24997-46484738134.23)
	assertRolling(t, row, "fin_eps_basic_ttm", 21.38+68.64-19.16)
}

func TestDisclosureDateIsEarliestAcrossTablesCappedByDeadline(t *testing.T) {
	reports := map[int]*report{}
	for _, table := range tables {
		body, err := os.ReadFile("testdata/sh600519_" + table + ".json")
		require.NoError(t, err)
		require.NoError(t, mergeTable(reports, table, body))
	}
	require.Len(t, reports, 12)
	for period, disclosed := range map[int]int{
		20260930: 20261031, 20260630: 20260815, 20260331: 20260425,
		20251231: 20260417, // 利润表是首次披露日，资产负债表已被三季报重新列示成 2026-10-31
		20250930: 20251030, // 利润表已被重新列示成 2026-10-31，资产负债表还是首次披露日
		20241231: 20250430, // 三张表都被重新列示，按截止日
		20231231: 20240430,
	} {
		assert.Equal(t, disclosed, reports[period].disclosed, "报告期 %d", period)
	}
}

// 只取到 6 期时，2025 年三季报用不到 2024 年年报和 2024 年三季报：原值照常给出，滚动值留空。
func TestRollingValuesAreEmptyWithoutPriorYear(t *testing.T) {
	provider := newProvider(t, &recorded{})
	row := fetch(t, provider, "000001.XSHE", "2025-11-03", "2025-11-04")[day("2025-11-03")]
	assert.EqualValues(t, 20250930, period(row))
	assert.Equal(t, marketdata.DoubleField(100668000000), row["fin_revenue"])
	assert.Equal(t, marketdata.DoubleField(71783000000), row["fin_operating_cash_flow"])
	for id := range row {
		assert.NotContains(t, id, ttmSuffix)
	}
	// 6 期里最早一期（2025 年半年报）披露之前没有已生效的报告期，不出值。
	assert.Empty(t, fetch(t, provider, "000001.XSHE", "2025-08-01", "2025-08-02"))
}

func TestFinancialCompaniesParse(t *testing.T) {
	provider := newProvider(t, &recorded{})
	for _, tc := range []struct {
		name, subject                           string
		revenue, operatingProfit, parentProfit  float64
		assets, liabilities, parentEquity, cash float64
		parentProfitTTM                         float64
	}{
		{"银行（平安银行）", "000001.XSHE", 103100000000, 46847000000, 39186000000, 5952354000000, 5395653000000, 556701000000, 283221000000, 0},
		{"保险（中国平安）", "601318.XSHG", 728134000000, 131562000000, 96645000000, 14455313000000, 12980243000000, 1044366000000, 566872000000, 0},
		{"券商（中信证券）", "600030.XSHG", 66590306144.15, 40473027795.77, 31021994688.23, 2350065962154.23, 1976471000484.0498, 367408757907.79004, 126779361212.87, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := fetch(t, provider, tc.subject, "2026-11-02", "2026-11-03")[day("2026-11-02")]
			assert.EqualValues(t, 20260930, period(row))
			assert.NotContains(t, row, "fin_total_revenue", "金融类公司没有营业总收入")
			assert.NotContains(t, row, "fin_total_revenue_ttm")
			assert.Equal(t, marketdata.DoubleField(tc.revenue), row["fin_revenue"])
			assert.Equal(t, marketdata.DoubleField(tc.operatingProfit), row["fin_operating_profit"])
			assert.Equal(t, marketdata.DoubleField(tc.parentProfit), row["fin_net_profit_parent"])
			assert.Equal(t, marketdata.DoubleField(tc.assets), row["fin_total_assets"])
			assert.Equal(t, marketdata.DoubleField(tc.liabilities), row["fin_total_liabilities"])
			assert.Equal(t, marketdata.DoubleField(tc.parentEquity), row["fin_equity_parent"])
			assert.Equal(t, marketdata.DoubleField(tc.cash), row["fin_operating_cash_flow"])
			assert.Contains(t, row, "fin_net_profit_parent_ttm")
			assert.Contains(t, row, "fin_eps_basic_ttm")
			assert.Len(t, row, 16)
		})
	}
}

func TestAnyTableFailureFailsTheWholeFetch(t *testing.T) {
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			provider := newProvider(t, &recorded{fail: table})
			values, err := provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "600519.XSHG", Frequency: "1d", Start: day("2026-11-02"), End: day("2026-11-03")})
			require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
			assert.Nil(t, values)
		})
	}
}

func TestMalformedResponseFails(t *testing.T) {
	for name, body := range map[string]string{
		"响应被截断":   `{"result":{"status":{"code":0},"data":{"report_list":{"20260930":{"publish_date":"20261031","data":[{"item_field":"BIZINCO"`,
		"接口报错":    `{"result":{"status":{"code":11},"data":null}}`,
		"科目值不是数字": `{"result":{"status":{"code":0},"data":{"report_list":{"20260930":{"publish_date":"20261031","data":[{"item_field":"BIZINCO","item_value":"--"}]}}}}}`,
		"披露日格式不对": `{"result":{"status":{"code":0},"data":{"report_list":{"20260930":{"publish_date":"2026-10-31","data":[]}}}}}`,
		"报告期格式不对": `{"result":{"status":{"code":0},"data":{"report_list":{"2026Q3":{"publish_date":"20261031","data":[]}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, mergeTable(map[int]*report{}, "lrb", []byte(body)))
		})
	}
	// 没有报告期时 report_list 是空数组，不算错。
	reports := map[int]*report{}
	require.NoError(t, mergeTable(reports, "lrb", []byte(`{"result":{"status":{"code":0},"data":{"report_list":[]}}}`)))
	assert.Empty(t, reports)
}

func TestUnknownOrUnsupportedStockHasNoValue(t *testing.T) {
	handler := &recorded{}
	provider := newProvider(t, handler)
	assert.Empty(t, fetch(t, provider, "301999.XSHE", "2026-11-02", "2026-11-03"), "来源没有这只股票")
	assert.Len(t, handler.requests, 3)
	assert.Empty(t, fetch(t, provider, "920992.XBSE", "2026-11-02", "2026-11-03"), "北交所不支持")
	assert.Len(t, handler.requests, 3, "不支持的标的不发请求")
}

// 实时取 6 期；补采取“区间起点到现在跨的季度数 + 6”期，一次请求不翻页；请求带 Accept-Encoding: gzip。
func TestRequestAsksForEnoughPeriodsInOnePage(t *testing.T) {
	handler := &recorded{}
	provider := newProvider(t, handler)
	fetch(t, provider, "600519.XSHG", "2026-11-10", "2026-11-11")
	fetch(t, provider, "600519.XSHG", "2025-11-11", "2026-11-11")
	require.Len(t, handler.requests, 6)
	for index, req := range handler.requests {
		query := req.URL.Query()
		assert.Equal(t, "/cn/api/openapi.php/CompanyFinanceService.getFinanceReport2022", req.URL.Path)
		assert.Equal(t, "sh600519", query.Get("paperCode"))
		assert.Equal(t, tables[index%3], query.Get("source"))
		assert.Equal(t, "0", query.Get("type"))
		assert.Equal(t, "1", query.Get("page"))
		assert.Equal(t, "gzip", req.Header.Get("Accept-Encoding"))
		if index < 3 {
			assert.Equal(t, "6", query.Get("num"), "实时")
		} else {
			assert.Equal(t, "10", query.Get("num"), "补一年")
		}
	}
	assert.Equal(t, 6, periodCount(time.Time{}, day("2026-11-11")))
	assert.Equal(t, 6, periodCount(day("2026-12-01"), day("2026-11-11")))
	assert.Equal(t, 7, periodCount(day("2026-10-31"), day("2026-11-11")))
	assert.Equal(t, 26, periodCount(day("2021-11-11"), day("2026-11-11")))
}

func TestFieldSpecs(t *testing.T) {
	specs := New(Config{}).FieldSpecs()
	require.Len(t, specs, 18)
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
		assert.Equal(t, "prev", spec.Fill, spec.ID)
		assert.Equal(t, []string{"1d"}, spec.Frequencies, spec.ID)
		if spec.ID == FieldReportPeriod {
			assert.Equal(t, marketdata.FieldTypeInt, spec.Type)
		} else {
			assert.Equal(t, marketdata.FieldTypeDouble, spec.Type, spec.ID)
		}
	}
	assert.Equal(t, []string{
		"fin_report_period",
		"fin_total_revenue", "fin_revenue", "fin_operating_profit", "fin_net_profit", "fin_net_profit_parent", "fin_eps_basic",
		"fin_total_assets", "fin_total_liabilities", "fin_equity_parent", "fin_operating_cash_flow",
		"fin_total_revenue_ttm", "fin_revenue_ttm", "fin_operating_profit_ttm", "fin_net_profit_ttm", "fin_net_profit_parent_ttm", "fin_eps_basic_ttm",
		"fin_operating_cash_flow_ttm",
	}, ids)
	require.NoError(t, New(Config{}).Descriptor().Validate())
}
