package cnbc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// fakeBar 是假服务返回的一根 1M（与 CNBC 的字段写法一致：价格是字符串，量是整数，tradeTime 是纽约时间开始时刻）。
type fakeBar struct {
	Open      string `json:"open"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Close     string `json:"close"`
	Volume    int64  `json:"volume"`
	TradeTime string `json:"tradeTime"`
}

// fakeServer 按请求的开始、结束（纽约时间，两端都含）从给定的 1M 里截取返回，并记下每次请求的路径与 User-Agent。
type fakeServer struct {
	mu       sync.Mutex
	bars     []fakeBar
	status   int
	paths    []string
	agents   []string
	requests int
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.paths = append(f.paths, r.URL.Path)
	f.agents = append(f.agents, r.Header.Get("User-Agent"))
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	// /harmony/app/bars/{代码}/1M/{开始}/{结束}/unadjusted/EST5EDT.json
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, barsPath+"/"), "/")
	if len(parts) != 6 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	from, to := parts[2], parts[3]
	var picked []fakeBar
	for _, bar := range f.bars {
		if bar.TradeTime >= from && bar.TradeTime <= to {
			picked = append(picked, bar)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"barData": map[string]any{"priceBars": picked, "symbol": parts[0]}})
}

func loadBars(t *testing.T, name string) []fakeBar {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	var payload struct {
		BarData struct {
			PriceBars []fakeBar `json:"priceBars"`
		} `json:"barData"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload.BarData.PriceBars
}

func newTestProvider(t *testing.T, server *fakeServer, now time.Time) *Provider {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return New(Config{BaseURL: httpServer.URL, Now: func() time.Time { return now }})
}

func utc(t *testing.T, text string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, text)
	require.NoError(t, err)
	return parsed
}

func request(start, end time.Time) marketdata.KlineRequest {
	return marketdata.KlineRequest{MarketID: marketID, ExchangeID: exchangeNasdaq, InstrumentType: marketdata.InstrumentEquity, SubjectID: "AAPL.XNAS", ProviderSymbol: "AAPL", Frequency: "5m", Limit: 100, StartTime: start, EndTime: end, RequestID: "req"}
}

// 整天（冬令时，2026-11-23 实测返回）：只合成常规时段的 78 根，16:00 那根 1M（收盘竞价）不进任何一根。
func TestFetchKlinesWholeDayWinter(t *testing.T) {
	server := &fakeServer{bars: loadBars(t, "aapl_1m_20261123.json")}
	provider := newTestProvider(t, server, utc(t, "2026-11-24T02:00:00Z"))
	rows, err := provider.FetchKlines(context.Background(), request(utc(t, "2026-11-23T05:00:00Z"), utc(t, "2026-11-24T05:00:00Z")))
	require.NoError(t, err)
	require.Len(t, rows, 78)
	first, last := rows[0], rows[77]
	require.Equal(t, utc(t, "2026-11-23T14:30:00Z"), first.BarStart, "冬令时首根开始于纽约 09:30")
	require.Equal(t, utc(t, "2026-11-23T14:35:00Z"), first.BarEnd)
	require.Equal(t, 347.82, first.Open)
	require.Equal(t, 349.76, first.High)
	require.Equal(t, 347.01, first.Low)
	require.Equal(t, 347.79, first.Close)
	require.Equal(t, float64(2147640), first.Volume)
	require.Equal(t, utc(t, "2026-11-23T20:55:00Z"), last.BarStart, "末根是纽约 15:55")
	require.Equal(t, 349.66, last.Open)
	require.Equal(t, 349.10, last.Close, "收盘价取 15:59 那分钟，不是 16:00 那根")
	require.Equal(t, float64(2377531), last.Volume, "不含 16:00 那根 1M 的量")
	for _, row := range rows {
		require.True(t, row.AmountEstimated)
		require.Greater(t, row.Amount, row.Volume*row.Low*0.999)
		require.Less(t, row.Amount, row.Volume*row.High*1.001)
		require.Equal(t, "cnbc", row.ProviderID)
		require.Equal(t, DefaultSourceID, row.SourceID)
		require.Equal(t, "AAPL", row.ProviderSymbol)
		require.Equal(t, row.BarStart, row.ProviderTimestamp)
	}
	require.Equal(t, 1, server.requests)
	require.Equal(t, barsPath+"/AAPL/1M/20261123000000/20261123205900/unadjusted/EST5EDT.json", server.paths[0], "纽约时间；终点不超过 now，接口终点含当分钟所以写终点前一分钟")
	require.True(t, strings.HasPrefix(server.agents[0], "Mozilla/5.0"), "必须带浏览器 User-Agent")
}

// 夏令时（2026-10-20 实测返回）：首根开始于 13:30Z。
func TestFetchKlinesWholeDaySummer(t *testing.T) {
	server := &fakeServer{bars: loadBars(t, "aapl_1m_20261020.json")}
	provider := newTestProvider(t, server, utc(t, "2026-10-21T02:00:00Z"))
	rows, err := provider.FetchKlines(context.Background(), request(utc(t, "2026-10-20T04:00:00Z"), utc(t, "2026-10-21T04:00:00Z")))
	require.NoError(t, err)
	require.Len(t, rows, 78)
	require.Equal(t, utc(t, "2026-10-20T13:30:00Z"), rows[0].BarStart)
	require.Equal(t, utc(t, "2026-10-20T19:55:00Z"), rows[77].BarStart)
}

// 实时那一期只要目标那一根；没收完（now 早于结束时刻）时报没有已收盘的根。
func TestFetchKlinesRealtimeTarget(t *testing.T) {
	server := &fakeServer{bars: loadBars(t, "aapl_1m_20261123.json")}
	target := utc(t, "2026-11-23T15:00:00Z")
	provider := newTestProvider(t, server, target.Add(5*time.Minute+5*time.Second))
	rows, err := provider.FetchKlines(context.Background(), request(target, target.Add(5*time.Minute)))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, target, rows[0].BarStart)
	require.Equal(t, barsPath+"/AAPL/1M/20261123100000/20261123100400/unadjusted/EST5EDT.json", server.paths[0])

	early := newTestProvider(t, server, target.Add(4*time.Minute))
	_, err = early.FetchKlines(context.Background(), request(target, target.Add(5*time.Minute)))
	require.ErrorIs(t, err, marketdata.ErrNoClosedBar)
}

// 缺分钟照常合成；整 5 分钟没有成交不出这一根；量为 0 的 1M 不参与。区间里一根都没有时报没有已收盘的根。
func TestFetchKlinesGaps(t *testing.T) {
	bars := []fakeBar{
		{"10", "11", "9", "10.5", 100, "20261123100000"},
		{"10.5", "12", "10", "11", 200, "20261123100300"},
		{"99", "99", "99", "99", 0, "20261123100400"},
		{"11", "11", "11", "11", 50, "20261123101000"},
	}
	server := &fakeServer{bars: bars}
	provider := newTestProvider(t, server, utc(t, "2026-11-23T20:00:00Z"))
	rows, err := provider.FetchKlines(context.Background(), request(utc(t, "2026-11-23T15:00:00Z"), utc(t, "2026-11-23T15:15:00Z")))
	require.NoError(t, err)
	require.Len(t, rows, 2, "10:05 那根没有成交，不出")
	require.Equal(t, utc(t, "2026-11-23T15:00:00Z"), rows[0].BarStart)
	require.Equal(t, []float64{10, 12, 9, 11, 300}, []float64{rows[0].Open, rows[0].High, rows[0].Low, rows[0].Close, rows[0].Volume})
	require.InDelta(t, 100*(10+11+9+10.5)/4+200*(10.5+12+10+11)/4, rows[0].Amount, 1e-9)
	require.Equal(t, utc(t, "2026-11-23T15:10:00Z"), rows[1].BarStart)

	_, err = provider.FetchKlines(context.Background(), request(utc(t, "2026-11-23T15:05:00Z"), utc(t, "2026-11-23T15:10:00Z")))
	require.ErrorIs(t, err, marketdata.ErrNoClosedBar)
}

// 半日市（2026-11-27 13:00 收市）：只到 12:55 那根，13:00 那根 1M（收盘竞价）丢掉。
func TestFetchKlinesHalfDay(t *testing.T) {
	var bars []fakeBar
	for at := time.Date(2026, 11, 27, 9, 30, 0, 0, newYork); !at.After(time.Date(2026, 11, 27, 13, 0, 0, 0, newYork)); at = at.Add(time.Minute) {
		bars = append(bars, fakeBar{"10", "10", "10", "10", 1, at.Format("20060102150405")})
	}
	server := &fakeServer{bars: bars}
	provider := newTestProvider(t, server, utc(t, "2026-11-28T00:00:00Z"))
	rows, err := provider.FetchKlines(context.Background(), request(utc(t, "2026-11-27T05:00:00Z"), utc(t, "2026-11-28T05:00:00Z")))
	require.NoError(t, err)
	require.Len(t, rows, 42)
	require.Equal(t, utc(t, "2026-11-27T17:55:00Z"), rows[41].BarStart)
	require.Equal(t, float64(5), rows[41].Volume)
}

// 不带区间时取最近 Limit 根；多天的区间按 5 天一段分几次请求，段与段之间不重复。
func TestFetchKlinesLatestAndWindows(t *testing.T) {
	server := &fakeServer{bars: loadBars(t, "aapl_1m_20261123.json")}
	provider := newTestProvider(t, server, utc(t, "2026-11-24T02:00:00Z"))
	req := request(time.Time{}, time.Time{})
	req.Limit = 3
	rows, err := provider.FetchKlines(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, utc(t, "2026-11-23T20:55:00Z"), rows[2].BarStart)

	// 整天重取的终点被裁到 now：请求里的时刻取整到分钟，秒写 00。
	late := newTestProvider(t, server, utc(t, "2026-11-23T21:19:37Z"))
	server.paths = nil
	_, err = late.FetchKlines(context.Background(), request(utc(t, "2026-11-23T05:00:00Z"), utc(t, "2026-11-24T05:00:00Z")))
	require.NoError(t, err)
	require.Equal(t, barsPath+"/AAPL/1M/20261123000000/20261123161800/unadjusted/EST5EDT.json", server.paths[0])

	// 历史补齐的合并分块（约 20 个自然日）一次请求取完：限速配置的超时管整次取数，分窗多一段就多一个来回。
	server.paths = nil
	block := request(utc(t, "2026-11-04T05:00:00Z"), utc(t, "2026-11-24T05:00:00Z"))
	block.Limit = 1000
	rows, err = provider.FetchKlines(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, rows, 78)
	require.Equal(t, []string{barsPath + "/AAPL/1M/20261104000000/20261123205900/unadjusted/EST5EDT.json"}, server.paths, "20 天一次请求，终点不超过 now")

	// 超过 31 天才分段。
	server.paths = nil
	rows, err = provider.FetchKlines(context.Background(), request(utc(t, "2026-10-15T04:00:00Z"), utc(t, "2026-11-24T05:00:00Z")))
	require.NoError(t, err)
	require.Len(t, rows, 78)
	require.Equal(t, []string{
		barsPath + "/AAPL/1M/20261015000000/20261114225900/unadjusted/EST5EDT.json",
		barsPath + "/AAPL/1M/20261114230000/20261123205900/unadjusted/EST5EDT.json",
	}, server.paths)
}

func TestFetchKlinesErrors(t *testing.T) {
	now := utc(t, "2026-11-24T02:00:00Z")
	day := request(utc(t, "2026-11-23T05:00:00Z"), utc(t, "2026-11-24T05:00:00Z"))

	_, err := newTestProvider(t, &fakeServer{}, now).FetchKlines(context.Background(), request(now.Add(-301*24*time.Hour), now.Add(-300*24*time.Hour)))
	require.ErrorIs(t, err, marketdata.ErrHistoryOutOfRange)

	_, err = newTestProvider(t, &fakeServer{status: http.StatusForbidden}, now).FetchKlines(context.Background(), day)
	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)

	_, err = newTestProvider(t, &fakeServer{status: http.StatusTooManyRequests}, now).FetchKlines(context.Background(), day)
	require.ErrorIs(t, err, marketdata.ErrRateLimited)

	oneMinute := request(utc(t, "2026-11-23T15:00:00Z"), utc(t, "2026-11-23T15:05:00Z"))
	oneMinute.Frequency = "1m"
	_, err = newTestProvider(t, &fakeServer{}, now).FetchKlines(context.Background(), oneMinute)
	require.ErrorIs(t, err, marketdata.ErrUnsupportedFrequency)

	bad := day
	bad.ProviderSymbol = "AAPL/../X"
	_, err = newTestProvider(t, &fakeServer{}, now).FetchKlines(context.Background(), bad)
	require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol)

	otherExchange := day
	otherExchange.ExchangeID = "XHKG"
	_, err = newTestProvider(t, &fakeServer{}, now).FetchKlines(context.Background(), otherExchange)
	require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol)
}

// 带类别的代码保留点（CNBC 认 BRK.B，不认 BRK_B）。
func TestFetchKlinesDottedSymbol(t *testing.T) {
	server := &fakeServer{bars: []fakeBar{{"500", "501", "499", "500.5", 10, "20261123100000"}}}
	provider := newTestProvider(t, server, utc(t, "2026-11-24T02:00:00Z"))
	req := request(utc(t, "2026-11-23T15:00:00Z"), utc(t, "2026-11-23T15:05:00Z"))
	req.SubjectID, req.ProviderSymbol, req.ExchangeID = "BRK.B.XNYS", "BRK.B", exchangeNYSE
	rows, err := provider.FetchKlines(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, barsPath+"/BRK.B/1M/20261123100000/20261123100400/unadjusted/EST5EDT.json", server.paths[0])
}

func TestParseMinutesRejectsBadPayload(t *testing.T) {
	for name, body := range map[string]string{
		"不是 JSON":    `<html>`,
		"没有 barData": `{"requestDetails":{}}`,
		"时刻不递增":      `{"barData":{"priceBars":[{"open":"1","high":"1","low":"1","close":"1","volume":1,"tradeTime":"20261123100100"},{"open":"1","high":"1","low":"1","close":"1","volume":1,"tradeTime":"20261123100000"}]}}`,
		"价格不对":       `{"barData":{"priceBars":[{"open":"","high":"1","low":"1","close":"1","volume":1,"tradeTime":"20261123100000"}]}}`,
	} {
		_, err := parseMinutes([]byte(body), "AAPL")
		require.True(t, errors.Is(err, marketdata.ErrProtocol), fmt.Sprintf("%s: %v", name, err))
	}
	minutes, err := parseMinutes([]byte(`{"barData":{"symbol":"AAPL"}}`), "AAPL")
	require.NoError(t, err, "区间内没有成交时没有 priceBars")
	require.Empty(t, minutes)
}

func TestKlineSpec(t *testing.T) {
	spec := New(Config{}).KlineSpec()
	require.NoError(t, spec.Validate())
	require.Equal(t, []string{"XNAS", "XNYS", "XASE"}, spec.Exchanges)
	require.True(t, spec.History.SupportsArbitraryRange)
	require.Equal(t, marketdata.TimestampModeOpen, spec.TimestampMode)
	require.Equal(t, "cnbc/stockus_cnbc_http", New(Config{}).Descriptor().SourceKey().String())
}
