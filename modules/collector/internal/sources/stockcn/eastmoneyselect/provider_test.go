package eastmoneyselect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

func serve(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Config{BaseURL: server.URL})
}

func serveFile(t *testing.T, name string, check func(r *http.Request)) *Provider {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		_, _ = w.Write(body)
	})
}

func request(subject, start, end string) marketdata.FieldRequest {
	return marketdata.FieldRequest{SubjectID: subject, Frequency: "1d", Start: day(start), End: day(end)}
}

// 录制的宁德时代真实响应：标注的交易日是 2026-11-11，最新价 243.32、股息率 3.3504454536%。
func TestFetchFieldsWritesOnlyTheLabeledTradeDate(t *testing.T) {
	provider := serveFile(t, "select_chinext_300750.json", func(r *http.Request) {
		assert.Equal(t, "/dataapi/xuangu/list", r.URL.Path)
		assert.Equal(t, `(SECURITY_CODE="300750")`, r.URL.Query().Get("filter"))
		assert.Equal(t, "SELECT_SECURITIES", r.URL.Query().Get("source"))
		assert.Equal(t, "Mozilla/5.0", r.Header.Get("User-Agent"))
	})
	// 实时采集：区间正好是标注的那一天。
	values, err := provider.FetchFields(context.Background(), request("300750.XSHE", "2026-11-11", "2026-11-12"))
	require.NoError(t, err)
	require.Len(t, values, 1)
	got := values[day("2026-11-11")]
	assert.Equal(t, marketdata.DoubleField(4627249686), got[FieldTotalShares])
	assert.Equal(t, marketdata.DoubleField(4260617083), got[FieldFloatShares])
	assert.Equal(t, marketdata.IntField(0), got[FieldIsST])
	assert.InDelta(t, 3.3504454536/100*243.32, got[FieldDividendPerShare].Double, 1e-12, "每股股利 = 股息率 × 最新价 ÷ 100")

	// 补一段包含这一天的历史：只有这一天有值。
	values, err = provider.FetchFields(context.Background(), request("300750.XSHE", "2025-11-12", "2026-11-12"))
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Contains(t, values, day("2026-11-11"))
}

// 接口标注的交易日晚于请求区间（补历史、隔天补采）：没有值，不报错。
func TestFetchFieldsHasNoValueForPastRanges(t *testing.T) {
	provider := serveFile(t, "select_chinext_300750.json", nil)
	values, err := provider.FetchFields(context.Background(), request("300750.XSHE", "2026-11-10", "2026-11-11"))
	require.NoError(t, err)
	assert.Empty(t, values)
}

// 接口标注的交易日早于请求区间的起点：接口还没更新到这一天，返回错误等下次重试，不写出一行空值。
func TestFetchFieldsFailsWhenTheSourceIsNotUpdatedYet(t *testing.T) {
	provider := serveFile(t, "select_chinext_300750.json", nil)
	_, err := provider.FetchFields(context.Background(), request("300750.XSHE", "2026-11-12", "2026-11-13"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "选股器的数据还停在 2026-11-11")
	// 不能是 ErrNoClosedBar：补采会把它当成“行情源确认没有这根 K 线”，把这一天整行记成已确认缺失。
	assert.False(t, errors.Is(err, marketdata.ErrNoClosedBar))
	assert.NotContains(t, err.Error(), marketdata.ErrNoClosedBar.Error())
}

// 录制的 *ST西发：名称带 ST；股息率为 0，每股股利是 0。
func TestFetchFieldsMarksSTStocks(t *testing.T) {
	provider := serveFile(t, "select_st_000752.json", nil)
	values, err := provider.FetchFields(context.Background(), request("000752.XSHE", "2026-11-11", "2026-11-12"))
	require.NoError(t, err)
	got := values[day("2026-11-11")]
	assert.Equal(t, marketdata.IntField(1), got[FieldIsST])
	assert.Equal(t, marketdata.DoubleField(0), got[FieldDividendPerShare])
	assert.Equal(t, marketdata.DoubleField(263758491), got[FieldTotalShares])
}

// 科创板：总股本与流通股本不同。
func TestFetchFieldsOnStarMarket(t *testing.T) {
	provider := serveFile(t, "select_star_688981.json", nil)
	values, err := provider.FetchFields(context.Background(), request("688981.XSHG", "2026-11-11", "2026-11-12"))
	require.NoError(t, err)
	got := values[day("2026-11-11")]
	assert.Equal(t, marketdata.DoubleField(8562425860), got[FieldTotalShares])
	assert.Equal(t, marketdata.DoubleField(2000593909), got[FieldFloatShares])
}

func TestFetchFieldsHasNoValueForUnknownOrUnsupportedStock(t *testing.T) {
	requests := 0
	body, err := os.ReadFile("testdata/select_missing_999999.json")
	require.NoError(t, err)
	provider := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(body)
	})
	values, err := provider.FetchFields(context.Background(), request("301999.XSHE", "2026-11-11", "2026-11-12"))
	require.NoError(t, err)
	assert.Empty(t, values, "查不到这只股票：success 为 true、data 为空")

	values, err = provider.FetchFields(context.Background(), request("920819.XBSE", "2026-11-11", "2026-11-12"))
	require.NoError(t, err)
	assert.Empty(t, values, "北交所不支持")
	assert.Equal(t, 1, requests, "不支持的标的不发请求")
}

// 单个字段没有值时其余字段照写：null、"-"、非有限值都按没有值处理，写成字符串的数字照常取值，整条响应不因此失败。
func TestFetchFieldsKeepsOtherFieldsWhenOneIsNull(t *testing.T) {
	provider := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"data":[{"SECURITY_CODE":"600519","SECURITY_NAME_ABBR":"贵州茅台","NEW_PRICE":"-","TOTAL_SHARES":"1250081601","FREE_SHARES":"Inf","ZXGXL":3.5,"MAX_TRADE_DATE":"2026-11-11 00:00:00"}]},"success":true}`))
	})
	values, err := provider.FetchFields(context.Background(), request("600519.XSHG", "2026-11-11", "2026-11-12"))
	require.NoError(t, err)
	assert.Equal(t, map[string]marketdata.FieldValue{
		FieldTotalShares: marketdata.DoubleField(1250081601), FieldIsST: marketdata.IntField(0),
	}, values[day("2026-11-11")], "交易日期带时间部分时只取日期")
}

func TestFetchFieldsFailsOnBadResponse(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"服务端错误": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"响应被截断": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"result":{"data":[{"SECURITY_CODE":"6005`))
		},
		"接口返回失败": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"result":null,"success":false,"message":"系统繁忙"}`))
		},
		"交易日期无效": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"result":{"data":[{"SECURITY_CODE":"600519","SECURITY_NAME_ABBR":"贵州茅台","TOTAL_SHARES":1,"MAX_TRADE_DATE":""}]},"success":true}`))
		},
		"返回的是别的股票": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"result":{"data":[{"SECURITY_CODE":"000001","SECURITY_NAME_ABBR":"平安银行","TOTAL_SHARES":1,"MAX_TRADE_DATE":"2026-11-11"}]},"success":true}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := serve(t, handler).FetchFields(context.Background(), request("600519.XSHG", "2026-11-11", "2026-11-12"))
			require.Error(t, err)
		})
	}
}

func TestFieldSpecs(t *testing.T) {
	specs := New(Config{}).FieldSpecs()
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
		assert.Equal(t, []string{"1d"}, spec.Frequencies, spec.ID)
		assert.Equal(t, "prev", spec.Fill, spec.ID)
	}
	assert.Equal(t, []string{"total_shares", "float_shares", "dividend_per_share", "is_st"}, ids)
	assert.Equal(t, marketdata.FieldTypeInt, specs[3].Type)
}
