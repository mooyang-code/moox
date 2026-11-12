package eastmoneyturnover

import (
	"context"
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

func rate(value float64) map[string]marketdata.FieldValue {
	return map[string]marketdata.FieldValue{FieldTurnoverRate: marketdata.DoubleField(value)}
}

// 录制的宁德时代真实响应：2026-11-04～11-11 共 6 个交易日（11-07、11-08 是周末）。
func TestFetchFieldsReturnsTurnoverOfEachTradeDate(t *testing.T) {
	provider := serveFile(t, "kline_chinext_300750.json", func(r *http.Request) {
		assert.Equal(t, "/api/qt/stock/kline/get", r.URL.Path)
		query := r.URL.Query()
		assert.Equal(t, "0.300750", query.Get("secid"))
		assert.Equal(t, "101", query.Get("klt"))
		assert.Equal(t, "0", query.Get("fqt"), "不复权")
		assert.Equal(t, "20261104", query.Get("beg"))
		assert.Equal(t, "20261111", query.Get("end"), "End 是开区间，最后一天是前一天")
		assert.Equal(t, "Mozilla/5.0", r.Header.Get("User-Agent"))
		assert.Equal(t, "https://quote.eastmoney.com/", r.Header.Get("Referer"))
	})
	values, err := provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "300750.XSHE", Frequency: "1d", Start: day("2026-11-04"), End: day("2026-11-12")})
	require.NoError(t, err)
	assert.Equal(t, map[time.Time]map[string]marketdata.FieldValue{
		day("2026-11-04"): rate(1.32), day("2026-11-05"): rate(1.63), day("2026-11-06"): rate(1.34),
		day("2026-11-09"): rate(1.02), day("2026-11-10"): rate(0.76), day("2026-11-11"): rate(0.90),
	}, values, "周末没有行，不出值")
}

// 响应里超出请求区间的行不出值（实时采集只要一天）。
func TestFetchFieldsDropsRowsOutsideTheRange(t *testing.T) {
	provider := serveFile(t, "kline_star_688981.json", func(r *http.Request) {
		assert.Equal(t, "1.688981", r.URL.Query().Get("secid"))
	})
	values, err := provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "688981.XSHG", Frequency: "1d", Start: day("2026-11-10"), End: day("2026-11-11")})
	require.NoError(t, err)
	assert.Equal(t, map[time.Time]map[string]marketdata.FieldValue{day("2026-11-10"): rate(1.28)}, values)
}

func TestFetchFieldsHasNoValueForUnknownOrUnsupportedStock(t *testing.T) {
	requests := 0
	body, err := os.ReadFile("testdata/kline_missing_999999.json")
	require.NoError(t, err)
	provider := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(body)
	})
	req := marketdata.FieldRequest{SubjectID: "301999.XSHE", Frequency: "1d", Start: day("2026-11-10"), End: day("2026-11-11")}
	values, err := provider.FetchFields(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, values, "来源没有这只股票：200、data 为 null")

	req.SubjectID = "920819.XBSE"
	values, err = provider.FetchFields(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, values, "北交所不支持")
	assert.Equal(t, 1, requests, "不支持的标的不发请求")
}

// 换手率不是数字的行跳过，其余的行照常出值；整个响应不因此失败。
func TestFetchFieldsSkipsRowsWithoutNumericTurnover(t *testing.T) {
	provider := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"rc":0,"data":{"klines":["2026-11-06,1,1,1,1,0,0,0,0,0,NaN","2026-11-09,1,1,1,1,0,0,0,0,0,Inf","2026-11-10,1,1,1,1,0,0,0,0,0,-","2026-11-11,1,1,1,1,5,5,0,0,0,0.37"]}}`))
	})
	values, err := provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "000752.XSHE", Frequency: "1d", Start: day("2026-11-06"), End: day("2026-11-12")})
	require.NoError(t, err)
	assert.Equal(t, map[time.Time]map[string]marketdata.FieldValue{day("2026-11-11"): rate(0.37)}, values)
}

func TestFetchFieldsFailsOnBadResponse(t *testing.T) {
	req := marketdata.FieldRequest{SubjectID: "600519.XSHG", Frequency: "1d", Start: day("2026-11-10"), End: day("2026-11-11")}
	for name, handler := range map[string]http.HandlerFunc{
		"服务端错误": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"响应被截断": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"rc":0,"data":{"klines":["2026-11-10,1,1,1,1,`))
		},
		"列数不足": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"rc":0,"data":{"klines":["2026-11-10,1,1,1,1,5,5"]}}`))
		},
		"日期无效": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"rc":0,"data":{"klines":["20261110,1,1,1,1,5,5,0,0,0,0.37"]}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := serve(t, handler).FetchFields(context.Background(), req)
			require.Error(t, err)
		})
	}
}

func TestFieldSpecs(t *testing.T) {
	specs := New(Config{}).FieldSpecs()
	require.Len(t, specs, 1)
	assert.Equal(t, marketdata.FieldSpec{ID: "turnover_rate", Name: "换手率", Type: marketdata.FieldTypeDouble, Fill: "zero", Frequencies: []string{"1d"}}, specs[0])
}
