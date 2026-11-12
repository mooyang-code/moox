package eastmoney

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// 比对测试（东方财富 K 线加日期区间规则时，港股的取数不能变）：港股东方财富取 K 线时发出的请求（方法、完整 URL、请求头）
// 与归一化后的 K 线，必须与 testdata 里记录的逐项相同。记录文件是在加日期区间规则之前的代码上生成的；
// 要重新生成时设 MOOX_UPDATE_GOLDEN=1。A 股的同类比对见 stockcn/eastmoney/kline_baseline_test.go。
const klineBaselineFile = "testdata/kline_baseline.json"

type klineBaselineView struct {
	Method  string                       `json:"method"`
	URL     string                       `json:"url"`
	Headers http.Header                  `json:"headers"`
	Rows    []marketdata.NormalizedKline `json:"rows"`
}

func TestKlineBaseline(t *testing.T) {
	day, err := os.ReadFile(filepath.Join("testdata", "kline_5m_00700_20261111.json"))
	require.NoError(t, err)
	evening := hkt("2026-11-11", "16:30:00")
	cases := []struct {
		name string
		body []byte
		cfg  Config
		now  time.Time
		req  marketdata.KlineRequest
	}{
		{
			name: "5m/取最近3根（不带区间）", body: day, now: evening,
			req: marketdata.KlineRequest{SubjectID: "00700.XHKG", ProviderSymbol: "00700", Frequency: "5m", Limit: 3, Now: evening, RequestID: "req-last"},
		},
		{
			name: "5m/整日按区间取", body: day, now: evening,
			req: marketdata.KlineRequest{
				MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, SubjectID: "00700.XHKG", ProviderSymbol: "00700",
				Frequency: "5m", Limit: 100, StartTime: hkt("2026-11-11", "09:30:00"), EndTime: hkt("2026-11-11", "16:00:00"), Now: evening, RequestID: "req-day",
			},
		},
		{
			name: "5m/定时请求只要目标那一根", body: day, now: evening,
			req: marketdata.KlineRequest{
				MarketID: "stockhk", ExchangeID: "XHKG", InstrumentType: marketdata.InstrumentEquity, SubjectID: "00700.XHKG", ProviderSymbol: "00700",
				Frequency: "5m", Limit: 1, StartTime: hkt("2026-11-11", "15:55:00"), EndTime: hkt("2026-11-11", "16:00:00"), Now: evening, RequestID: "req-target",
			},
		},
		{
			name: "5m/只有起点（终点取请求时刻）", body: day, now: evening,
			req: marketdata.KlineRequest{
				SubjectID: "00700.XHKG", ProviderSymbol: "00700", Frequency: "5m", Limit: 5,
				StartTime: hkt("2026-11-11", "15:30:00"), Now: evening, RequestID: "req-start-only",
			},
		},
		{
			name: "5m/只有终点", body: day, now: evening,
			req: marketdata.KlineRequest{
				SubjectID: "00700.XHKG", ProviderSymbol: "00700", Frequency: "5m", Limit: 5,
				EndTime: hkt("2026-11-11", "16:00:00"), Now: hkt("2026-11-12", "01:00:00"), RequestID: "req-end-only",
			},
		},
		{
			name: "5m/不带区间也不带请求时刻（终点取远期日期）", body: day, now: evening,
			req: marketdata.KlineRequest{SubjectID: "00700.XHKG", ProviderSymbol: "00700", Frequency: "5m", Limit: 2, RequestID: "req-no-now"},
		},
		{
			name: "5m/跨天区间与数据源配置给的地址、source_id、单次根数", body: day, now: evening,
			cfg: Config{BaseURL: "https://72.push2his.eastmoney.com/", KlineEndpoint: "/api/qt/stock/kline/get", SourceID: " StockHK_HTTP ", MaxBarsPerRequest: 800},
			req: marketdata.KlineRequest{
				SubjectID: "09988.XHKG", ProviderSymbol: "09988", Frequency: "5m", Limit: 3,
				StartTime: hkt("2026-11-10", "09:30:00"), EndTime: hkt("2026-11-11", "16:00:00"), Now: evening, RequestID: "req-range",
			},
		},
	}
	got := map[string]klineBaselineView{}
	for _, item := range cases {
		view := klineBaselineView{}
		cfg := item.cfg
		cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			view.Method, view.URL, view.Headers = r.Method, r.URL.String(), r.Header.Clone()
			recorder := httptest.NewRecorder()
			_, _ = recorder.Write(item.body)
			return recorder.Result(), nil
		})}
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
	require.Equal(t, string(want), string(raw), "港股东方财富的请求或归一化结果与加日期区间规则之前记录的不一致")
}
