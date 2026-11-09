package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/stretchr/testify/require"
)

// fundingServer 按请求参数从内存记录里返回资金费：endTime 含端点，按 fundingTime 升序，最多 limit 条。
type fundingServer struct {
	mu       sync.Mutex
	records  []binanceapi.FundingRateRecord
	requests []fundingRequest
	status   int
}

type fundingRequest struct {
	symbol     string
	start, end int64
}

func (s *fundingServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path != binanceapi.SwapFundingRateEndpoint {
		http.NotFound(w, r)
		return
	}
	query := r.URL.Query()
	start, _ := strconv.ParseInt(query.Get("startTime"), 10, 64)
	end, _ := strconv.ParseInt(query.Get("endTime"), 10, 64)
	limit, _ := strconv.Atoi(query.Get("limit"))
	s.requests = append(s.requests, fundingRequest{symbol: query.Get("symbol"), start: start, end: end})
	if s.status != 0 {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(s.status)
		return
	}
	var rows []binanceapi.FundingRateRecord
	for _, record := range s.records {
		if record.FundingTime < start || record.FundingTime > end {
			continue
		}
		if symbol := query.Get("symbol"); symbol != "" && record.Symbol != symbol {
			continue
		}
		rows = append(rows, record)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].FundingTime < rows[j].FundingTime })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func newFundingTestAdapter(t *testing.T, server *fundingServer) *MarketDataAdapter {
	t.Helper()
	tls := httptest.NewTLSServer(http.HandlerFunc(server.handler))
	t.Cleanup(tls.Close)
	client := binanceapi.NewClient(httpclient.NewHTTPClient(tls.Client()))
	require.NoError(t, client.SetSwapBaseURL(tls.URL))
	return NewMarketDataAdapter(AdapterConfig{InstrumentType: marketdata.InstrumentSwap, KlineCollector: NewKlineCollector(client), SymbolCollector: NewSymbolCollector(client)})
}

func fastLimiter() *fundingLimiter {
	return newFundingLimiter(time.Millisecond, 50*time.Millisecond, time.Second)
}

func record(symbol string, at time.Time, rate string) binanceapi.FundingRateRecord {
	return binanceapi.FundingRateRecord{Symbol: symbol, FundingTime: at.UnixMilli(), FundingRate: rate}
}

var fundingBase = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func TestFundingRatesOnlySwapSupportsIt(t *testing.T) {
	spot := NewMarketDataAdapter(AdapterConfig{InstrumentType: marketdata.InstrumentSpot})
	require.Nil(t, marketdata.FundingRatesOf(spot), "现货没有资金费")
	swap := NewMarketDataAdapter(AdapterConfig{InstrumentType: marketdata.InstrumentSwap})
	require.NotNil(t, marketdata.FundingRatesOf(swap))
}

// 带 symbol：区间左闭右开，晚 1 毫秒的结算算在这一小时；endTime 发给接口时减 1 毫秒。
func TestFundingRatesWithSymbolIsHalfOpenAndKeepsLateMillisecond(t *testing.T) {
	server := &fundingServer{records: []binanceapi.FundingRateRecord{
		record("BTCUSDT", fundingBase.Add(time.Millisecond), "0.0001"),
		record("BTCUSDT", fundingBase.Add(8*time.Hour), "0.0002"),
		record("ETHUSDT", fundingBase.Add(8*time.Hour), "0.0003"),
	}}
	adapter := newFundingTestAdapter(t, server)
	rates, err := adapter.fundingRates(context.Background(), fastLimiter(), "BTCUSDT", fundingBase, fundingBase.Add(8*time.Hour))
	require.NoError(t, err)
	require.Len(t, rates, 1)
	require.Equal(t, fundingBase.Add(time.Millisecond), rates[0].FundingTime)
	require.Equal(t, 0.0001, rates[0].Rate)
	require.Equal(t, fundingBase.Add(8*time.Hour).UnixMilli()-1, server.requests[0].end)
	require.Equal(t, "BTCUSDT", server.requests[0].symbol)
}

// 1 小时结算的合约，1000 小时正好 1000 条：满页后还要再翻一页确认。
func TestFundingRatesWithSymbolPagesPastExactlyFullPage(t *testing.T) {
	server := &fundingServer{}
	for hour := 0; hour < binanceapi.FundingRatePageLimit; hour++ {
		server.records = append(server.records, record("PEPEUSDT", fundingBase.Add(time.Duration(hour)*time.Hour), "0.00001"))
	}
	adapter := newFundingTestAdapter(t, server)
	rates, err := adapter.fundingRates(context.Background(), fastLimiter(), "PEPEUSDT", fundingBase, fundingBase.Add(2000*time.Hour))
	require.NoError(t, err)
	require.Len(t, rates, binanceapi.FundingRatePageLimit)
	require.Len(t, server.requests, 2, "满页之后再翻一页，返回不足一页才结束")
	require.Equal(t, fundingBase.Add(999*time.Hour).UnixMilli()+1, server.requests[1].start)
}

// 不带 symbol：按 4 小时分段；某段满页时对半拆开重查，结果不重不漏。
func TestFundingRatesAllMarketSplitsFullPage(t *testing.T) {
	server := &fundingServer{}
	for index := 0; index < 1500; index++ {
		at := fundingBase.Add(time.Duration(index%3) * time.Hour)
		server.records = append(server.records, record("S"+strconv.Itoa(index)+"USDT", at, "0.0001"))
	}
	server.records = append(server.records, record("BTCUSDT", fundingBase.Add(4*time.Hour), "0.0002"))
	adapter := newFundingTestAdapter(t, server)
	rates, err := adapter.fundingRates(context.Background(), fastLimiter(), "", fundingBase, fundingBase.Add(8*time.Hour))
	require.NoError(t, err)
	require.Len(t, rates, 1501)
	seen := map[string]bool{}
	for _, rate := range rates {
		require.False(t, seen[rate.ProviderSymbol], "拆分不能重复返回 %s", rate.ProviderSymbol)
		seen[rate.ProviderSymbol] = true
	}
	require.Greater(t, len(server.requests), 2, "第一段满页要拆开")
	for _, request := range server.requests {
		require.Empty(t, request.symbol)
	}
}

func TestFundingRatesAllMarketSkipsMalformedRecords(t *testing.T) {
	server := &fundingServer{records: []binanceapi.FundingRateRecord{
		record("BTCUSDT", fundingBase, "0.0001"),
		record("BADUSDT", fundingBase, "not-a-number"),
	}}
	adapter := newFundingTestAdapter(t, server)
	rates, err := adapter.fundingRates(context.Background(), fastLimiter(), "", fundingBase, fundingBase.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, rates, 1)
	_, err = adapter.fundingRates(context.Background(), fastLimiter(), "BADUSDT", fundingBase, fundingBase.Add(time.Hour))
	require.ErrorIs(t, err, marketdata.ErrProtocol, "指定合约的记录格式不对要报错")
}

// 收到 429 后暂停全部请求：下一次请求要等到退避结束。
func TestFundingLimiterBacksOffAfterRateLimit(t *testing.T) {
	server := &fundingServer{status: http.StatusTooManyRequests}
	adapter := newFundingTestAdapter(t, server)
	limiter := newFundingLimiter(time.Millisecond, 150*time.Millisecond, time.Second)
	_, err := adapter.fundingRates(context.Background(), limiter, "BTCUSDT", fundingBase, fundingBase.Add(time.Hour))
	require.ErrorIs(t, err, marketdata.ErrRateLimited)
	require.Len(t, server.requests, 1, "429 不在请求内重试")
	started := time.Now()
	require.NoError(t, limiter.Wait(context.Background(), true))
	require.GreaterOrEqual(t, time.Since(started), 100*time.Millisecond, "退避期间所有请求都要等")

	limiter.Done(&httpclient.StatusError{StatusCode: http.StatusTeapot})
	require.Equal(t, 300*time.Millisecond, limiter.backoff, "连续触发时翻倍")
	limiter.Done(nil)
	require.Zero(t, limiter.backoff, "成功一次复位")
}

// 有实时请求在等时，补采请求让它先走。
func TestFundingLimiterPrefersRealtime(t *testing.T) {
	limiter := newFundingLimiter(80*time.Millisecond, time.Second, time.Second)
	require.NoError(t, limiter.Wait(context.Background(), false)) // 占掉当前配额
	order := make(chan string, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = limiter.Wait(context.Background(), false)
		order <- "backfill"
	}()
	time.Sleep(10 * time.Millisecond) // 补采先开始等
	go func() {
		defer wg.Done()
		_ = limiter.Wait(context.Background(), true)
		order <- "realtime"
	}()
	wg.Wait()
	require.Equal(t, "realtime", <-order)
	require.Equal(t, "backfill", <-order)
}

func TestFundingLimiterWaitHonoursContext(t *testing.T) {
	limiter := newFundingLimiter(time.Hour, time.Second, time.Second)
	require.NoError(t, limiter.Wait(context.Background(), true))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, limiter.Wait(ctx, true), context.DeadlineExceeded)
	require.Zero(t, limiter.realtimeWaiting, "放弃等待后不能留下实时排队计数")
}
