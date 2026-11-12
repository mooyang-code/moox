package eastmoney

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// 比对测试（东方财富 K 线抽成共用代码）：A 股东方财富取 K 线时发出的请求（方法、完整 URL、请求头）与归一化后的 K 线，
// 必须与 testdata 里记录的逐项相同。记录文件是在抽共用代码之前的代码上生成的；要重新生成时设 MOOX_UPDATE_GOLDEN=1。
const klineBaselineFile = "testdata/kline_baseline.json"

type klineBaselineView struct {
	Method  string                       `json:"method"`
	URL     string                       `json:"url"`
	Headers http.Header                  `json:"headers"`
	Rows    []marketdata.NormalizedKline `json:"rows"`
}

func TestKlineBaseline(t *testing.T) {
	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		return body
	}
	noon := cst("2026-11-04", "12:51")
	evening := cst("2026-11-11", "18:00")
	cases := []struct {
		name string
		body []byte
		cfg  Config
		now  time.Time
		req  marketdata.KlineRequest
	}{
		{
			name: "5m/取最近60根（不带区间）", body: read("kline_5m_600519_20261104.json"), now: noon,
			req: marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, Now: noon, RequestID: "req-5m"},
		},
		{
			name: "5m/定时请求只要目标那一根", body: read("kline_5m_600519_20261104.json"), now: noon,
			req: marketdata.KlineRequest{
				MarketID: "stockcn", ExchangeID: "XSHG", InstrumentType: marketdata.InstrumentEquity, SubjectID: "600519.XSHG", ProviderSymbol: "600519",
				Frequency: "5m", Limit: 1, StartTime: cst("2026-11-04", "11:25"), EndTime: cst("2026-11-04", "11:30"), Now: noon, RequestID: "req-5m-target",
			},
		},
		{
			name: "5m/数据源配置给的地址、source_id 与单次根数", body: read("kline_5m_600519_20261104.json"), now: noon,
			cfg: Config{KlineBaseURL: "https://72.push2his.eastmoney.com/", KlineEndpoint: "/api/qt/stock/kline/get", SourceID: " StockCN_HTTP ", MaxBarsPerRequest: 800},
			req: marketdata.KlineRequest{
				SubjectID: "000001.XSHE", ProviderSymbol: "sz000001", Frequency: "5m", Limit: 3,
				StartTime: cst("2026-11-03", "09:30"), EndTime: cst("2026-11-04", "11:30"), Now: noon, RequestID: "req-5m-range",
			},
		},
		{
			name: "1m/按区间取", body: read("kline_1m_600519_20261104.json"), now: noon,
			cfg: Config{BaseURL: "http://fixture.test", KlineBaseURL: "http://fixture.test"},
			req: marketdata.KlineRequest{
				SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "1m", Limit: 2,
				StartTime: cst("2026-11-04", "09:30"), EndTime: cst("2026-11-04", "09:32"), Now: noon, RequestID: "req-1m",
			},
		},
		{
			name: "1d/按区间取", body: read("kline_1d_600519_20251103_20251115.json"), now: evening,
			req: marketdata.KlineRequest{
				SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "1d", Limit: 12, Now: evening, RequestID: "req-1d",
				StartTime: time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2025, 11, 15, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "1d/取最近2根（当天未收盘的不返回）", now: evening,
			body: []byte(`{"data":{"klines":["2026-11-10,1438.23,1439.00,1444.50,1431.26,23038,3316078849.00","2026-11-11,1445.00,1402.30,1445.62,1402.00,39848,5633865699.00"]}}`),
			req:  marketdata.KlineRequest{SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "1d", Limit: 2, Now: evening, RequestID: "req-1d"},
		},
	}
	got := map[string]klineBaselineView{}
	for _, item := range cases {
		view := klineBaselineView{}
		cfg := item.cfg
		cfg.HTTPClient = newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			view.Method, view.URL, view.Headers = r.Method, r.URL.String(), r.Header.Clone()
			_, _ = w.Write(item.body)
		}))
		cfg.Now = func() time.Time { return item.now }
		rows, err := New(cfg).FetchKlines(context.Background(), item.req)
		require.NoError(t, err, item.name)
		require.NotEmpty(t, rows, item.name)
		view.Rows = rows
		got[item.name] = view
	}
	raw, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	raw = append(raw, '\n')
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(klineBaselineFile, raw, 0o644))
		return
	}
	want, err := os.ReadFile(klineBaselineFile)
	require.NoError(t, err)
	require.Equal(t, string(want), string(raw), "A 股东方财富的请求或归一化结果与抽共用代码之前记录的不一致")
}
