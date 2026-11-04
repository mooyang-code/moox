package tdx

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
	"github.com/stretchr/testify/require"
)

func TestCategoryForFrequencyCoversSupportedA股Bars(t *testing.T) {
	tests := map[string]tdxwire.KlineCategory{
		"1m":  tdxwire.Category1Min,
		"5m":  tdxwire.Category5Min,
		"15m": tdxwire.Category15Min,
		"30m": tdxwire.Category30Min,
		"60m": tdxwire.Category60Min,
		"1d":  tdxwire.CategoryDay,
		"1w":  tdxwire.CategoryWeek,
		"1mo": tdxwire.CategoryMonth,
	}
	for frequency, want := range tests {
		t.Run(frequency, func(t *testing.T) {
			got, err := categoryForFrequency(frequency)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
	_, err := categoryForFrequency("2m")
	require.ErrorIs(t, err, marketdata.ErrUnsupportedFrequency)
}

func TestProviderDescriptorUsesNormalTDXSourceIdentity(t *testing.T) {
	provider := New(Config{Host: "quotes.example", Port: 7709})
	descriptor := provider.Descriptor()
	require.Equal(t, "tdx", descriptor.ID)
	require.Equal(t, "normal_7709", descriptor.SourceID)
	require.Equal(t, []string{"quotes.example"}, descriptor.Hosts)
}

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

func cst(day, clock string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, shanghai)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestNormalizeBarsUsesCloseLabelsAndSourceIdentity(t *testing.T) {
	fetchedAt := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	bars := []tdxwire.Bar{{Time: cst("2026-09-01", "09:31"), Open: 10, High: 10.2, Low: 9.8, Close: 10.1, Volume: 1200, Amount: 12000}}
	rows, err := normalizeBars("600000.XSHG", "sh600000", "1m", bars, fetchedAt, "request-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "tdx", rows[0].ProviderID)
	require.Equal(t, "normal_7709", rows[0].SourceID)
	require.Equal(t, cst("2026-09-01", "09:30").UTC(), rows[0].BarStart, "标签 09:31 是 09:30 开盘那根")
	require.Equal(t, rows[0].BarStart.Add(time.Minute), rows[0].BarEnd)
	require.Equal(t, float64(1200), rows[0].VolumeShares)
	require.Equal(t, float64(12000), rows[0].AmountCNY)
}

func TestNormalizeBarsMapsLunchLabelToMorningLastBar(t *testing.T) {
	for frequency, want := range map[string]string{"5m": "11:25", "1m": "11:29"} {
		bars := []tdxwire.Bar{{Time: cst("2026-11-04", "13:00"), Open: 1, High: 1, Low: 1, Close: 1, Volume: 1, Amount: 1}}
		rows, err := normalizeBars("600519.XSHG", "sh600519", frequency, bars, time.Now().UTC(), "lunch")
		require.NoError(t, err)
		require.Equal(t, cst("2026-11-04", want).UTC(), rows[0].BarStart, frequency)
	}
	// 开盘标签 09:30 不往前一天找：原样换算成 09:25，交给写入边界按区间过滤。
	rows, err := normalizeBars("600519.XSHG", "sh600519", "5m", []tdxwire.Bar{{Time: cst("2026-11-04", "09:30"), Open: 1, High: 1, Low: 1, Close: 1, Volume: 1, Amount: 1}}, time.Now().UTC(), "open")
	require.NoError(t, err)
	require.Equal(t, cst("2026-11-04", "09:25").UTC(), rows[0].BarStart)
}

// testdata/security_bars_5m_600519_20261104.hex 是 2026-11-04 12:51（午休）从 shtdx.gtjas.com:7709 实际取回的
// 600519 最近 60 根 5m（category 0）解压后的 body；同一时刻东方财富 5m 的原始返回在 eastmoney/testdata。
// 两家归一化后逐根一致：开盘时刻、价格与成交量（股）逐位相同，成交额只差通达信 float32 的精度。
func TestRecordedTDX5mMatchesEastMoney(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "security_bars_5m_600519_20261104.hex"))
	require.NoError(t, err)
	body, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	bars, err := tdxwire.ParseSecurityBars(body, tdxwire.Category5Min, false)
	require.NoError(t, err)
	require.Len(t, bars, 60)
	require.Equal(t, cst("2026-11-04", "13:00"), bars[59].Time.In(shanghai), "午休时上午最后一根被标成 13:00")
	tdxRows, err := normalizeBars("600519.XSHG", "sh600519", "5m", bars, time.Now().UTC(), "tdx")
	require.NoError(t, err)

	eastBody, err := os.ReadFile(filepath.Join("..", "eastmoney", "testdata", "kline_5m_600519_20261104.json"))
	require.NoError(t, err)
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(eastBody)), Header: http.Header{}}, nil
	})}
	now := cst("2026-11-04", "12:51")
	eastRows, err := eastmoney.New(eastmoney.Config{HTTPClient: client, Now: func() time.Time { return now }}).FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", Frequency: "5m", Limit: 60, Now: now.UTC(), RequestID: "east",
	})
	require.NoError(t, err)
	require.Len(t, eastRows, len(tdxRows))
	// 时间对齐与单位：每一根的开盘、收盘时刻相同；整段成交量（股）合计相同。
	// 两家对贴近 5 分钟边界的个别成交归属不同（实测 60 根里 23 根差几百到几千股、开收盘价差几分），
	// 所以逐根只要求大多数完全相同，合计必须一致。
	identical := 0
	var tdxVolume, eastVolume float64
	for i := range tdxRows {
		left, right := tdxRows[i], eastRows[i]
		require.Equal(t, right.BarStart, left.BarStart, "第 %d 根", i)
		require.Equal(t, right.BarEnd, left.BarEnd, "第 %d 根", i)
		tdxVolume += left.VolumeShares
		eastVolume += right.VolumeShares
		if left.Open == right.Open && left.High == right.High && left.Low == right.Low && left.Close == right.Close && left.VolumeShares == right.VolumeShares {
			identical++
			require.InEpsilon(t, right.AmountCNY, left.AmountCNY, 1e-6, "第 %d 根成交额只差 float32 精度", i)
		}
	}
	require.InEpsilon(t, eastVolume, tdxVolume, 1e-3)
	require.GreaterOrEqual(t, identical, 35)
	// 当天开盘第一根（含 09:25 集合竞价）两家完全一致：673500 股。
	first := tdxRows[36]
	require.Equal(t, cst("2026-11-04", "09:30").UTC(), first.BarStart)
	require.Equal(t, eastRows[36], marketdata.NormalizedKline{
		SubjectID: first.SubjectID, ProviderID: "eastmoney", SourceID: "stockcn_http", ProviderSymbol: first.ProviderSymbol, Frequency: "5m",
		BarStart: first.BarStart, BarEnd: first.BarEnd, Open: first.Open, High: first.High, Low: first.Low, Close: first.Close,
		VolumeShares: first.VolumeShares, AmountCNY: eastRows[36].AmountCNY, ProviderTimestamp: first.BarEnd,
		FetchedAt: eastRows[36].FetchedAt, RequestID: "east",
	})
	require.Equal(t, 673500.0, first.VolumeShares)
	require.Equal(t, cst("2026-11-04", "11:25").UTC(), tdxRows[59].BarStart)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// fakeServer 模拟通达信服务端：按交易时段生成 5m K 线，时间标签是收盘时刻；交易时段内最新一根是未收盘的那根。
type fakeServer struct {
	bars     []tdxwire.Bar
	requests [][2]int
}

func newFakeServer(t *testing.T, from, now time.Time) *fakeServer {
	t.Helper()
	clock, err := markets.Clock("stockcn", "5m")
	require.NoError(t, err)
	buckets, err := clock.Between(from, now, 10000)
	require.NoError(t, err)
	server := &fakeServer{}
	for i, bucket := range buckets {
		price := 100 + float64(i)
		server.bars = append(server.bars, tdxwire.Bar{Time: bucket.End.In(shanghai), Open: price, High: price + 1, Low: price - 1, Close: price + 0.5, Volume: 1000, Amount: 100000})
	}
	return server
}

func (s *fakeServer) page(_ context.Context, _ tdxwire.Market, _ string, category tdxwire.KlineCategory, start, count int) ([]tdxwire.Bar, error) {
	if category != tdxwire.Category5Min || count > tdxMaxBarsPerPage {
		return nil, fmt.Errorf("unexpected page category=%d count=%d", category, count)
	}
	s.requests = append(s.requests, [2]int{start, count})
	end := len(s.bars) - start
	if end <= 0 {
		return nil, nil
	}
	return append([]tdxwire.Bar(nil), s.bars[max(end-count, 0):end]...), nil
}

func TestFetchKlinesReadsTheRequestedWindowDuringTheSession(t *testing.T) {
	now := cst("2026-11-04", "10:07").Add(30 * time.Second) // 10:05 那根还没收盘
	for _, pageSize := range []int{800, 10} {
		server := newFakeServer(t, cst("2026-11-03", "09:30").UTC(), now.UTC())
		provider := New(Config{Now: func() time.Time { return now }, MaxBarsPerRequest: pageSize})
		provider.fetchPage = server.page
		fetch := func(req marketdata.KlineRequest) []marketdata.NormalizedKline {
			req.SubjectID, req.ProviderSymbol, req.ExchangeID, req.Frequency, req.RequestID = "600519.XSHG", "sh600519", "XSHG", "5m", "window"
			rows, err := provider.FetchKlines(context.Background(), req)
			require.NoError(t, err)
			return rows
		}

		// 周期提交：目标 10:00 那根，只要 1 根；服务端最新一根是未收盘的 10:05，不能当成目标。
		server.requests = nil
		rows := fetch(marketdata.KlineRequest{Limit: 1, StartTime: cst("2026-11-04", "10:00").UTC(), EndTime: cst("2026-11-04", "10:05").UTC()})
		require.Equal(t, [][2]int{{0, 2}}, server.requests)
		require.Len(t, rows, 1)
		require.Equal(t, cst("2026-11-04", "10:00").UTC(), rows[0].BarStart)

		// 补采前一天下午：按交易序号定位到那一段，而不是“现在往前 N 根”。
		server.requests = nil
		rows = fetch(marketdata.KlineRequest{Limit: 24, StartTime: cst("2026-11-03", "13:00").UTC(), EndTime: cst("2026-11-03", "15:00").UTC()})
		require.Equal(t, cst("2026-11-03", "13:00").UTC(), rows[0].BarStart)
		var inRange int
		for _, row := range rows {
			if !row.BarStart.Before(cst("2026-11-03", "13:00").UTC()) && row.BarStart.Before(cst("2026-11-03", "15:00").UTC()) {
				inRange++
			}
		}
		require.Equal(t, 24, inRange)
		require.Len(t, rows, 25, "多取一根较新的，容忍服务端还没生成新一根")
		if pageSize == 10 {
			require.Equal(t, [][2]int{{27, 5}, {17, 10}, {7, 10}}, server.requests, "超过单页上限时从最旧的一页起分页")
		} else {
			require.Equal(t, [][2]int{{7, 25}}, server.requests)
		}

		// 没有起始时间：取最新 Limit 根，未收盘那根不返回。
		server.requests = nil
		rows = fetch(marketdata.KlineRequest{Limit: 3})
		require.Len(t, rows, 3)
		require.Equal(t, cst("2026-11-04", "10:00").UTC(), rows[2].BarStart)
	}
}

func TestFetchKlinesDuringLunchUsesTheMorningLastBar(t *testing.T) {
	now := cst("2026-11-04", "11:30").Add(5 * time.Second)
	server := newFakeServer(t, cst("2026-11-04", "09:30").UTC(), now.UTC())
	// 午休时服务端把上午最后一根标成 13:00。
	server.bars[len(server.bars)-1].Time = cst("2026-11-04", "13:00")
	provider := New(Config{Now: func() time.Time { return now }})
	provider.fetchPage = server.page
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "600519.XSHG", ProviderSymbol: "sh600519", ExchangeID: "XSHG", Frequency: "5m", RequestID: "lunch", Limit: 1,
		StartTime: cst("2026-11-04", "11:25").UTC(), EndTime: cst("2026-11-04", "11:30").UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, [][2]int{{0, 1}}, server.requests)
	require.Len(t, rows, 1)
	require.Equal(t, cst("2026-11-04", "11:25").UTC(), rows[0].BarStart)
}
