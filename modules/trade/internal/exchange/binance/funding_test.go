package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

type fundingRequest struct {
	from, to int64
	limit    int
	symbol   bool
	signed   bool
}

// fundingServer 模拟 /fapi/v1/fundingRate。rows 为空时原样返回样例文件；否则窗口内按结算时刻升序返回从 startTime 起的至多 limit 条。
func fundingServer(t *testing.T, body []byte, rows []fundingRatePayload) (*httptest.Server, func() []fundingRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []fundingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != fundingRatePath {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		query := request.URL.Query()
		from, _ := strconv.ParseInt(query.Get("startTime"), 10, 64)
		to, _ := strconv.ParseInt(query.Get("endTime"), 10, 64)
		limit, _ := strconv.Atoi(query.Get("limit"))
		mu.Lock()
		requests = append(requests, fundingRequest{
			from: from, to: to, limit: limit, symbol: query.Has("symbol"),
			signed: query.Has("signature") || request.Header.Get("X-MBX-APIKEY") != "",
		})
		mu.Unlock()
		if body != nil {
			_, _ = w.Write(body)
			return
		}
		out := []fundingRatePayload{}
		for _, row := range rows {
			if row.FundingTime >= from && row.FundingTime <= to && len(out) < limit {
				out = append(out, row)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(server.Close)
	return server, func() []fundingRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]fundingRequest(nil), requests...)
	}
}

func TestFundingRatesParseSample(t *testing.T) {
	server, requests := fundingServer(t, sample(t, "fapi_funding_rate.json"), nil)
	start := time.UnixMilli(1730419200000)
	rates, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListFundingRates(context.Background(), start, start.Add(fundingRateChunk-time.Millisecond),
		[]string{"BTCUSDT", "ETHUSDT", "1000PEPEUSDT", "XRPUSDT"})
	if err != nil {
		t.Fatal(err)
	}
	got := requests()
	if len(got) != 1 || got[0].from != 1730419200000 || got[0].to != 1730433599999 || got[0].limit != 1000 || got[0].symbol || got[0].signed {
		t.Fatalf("requests = %+v", got)
	}
	if len(rates) != 4 {
		t.Fatalf("rates = %+v", rates)
	}
	if rates[0].ExchangeSymbol != "BTCUSDT" || rates[0].Rate.String() != "0.0001" || rates[0].MarkPrice.String() != "70283.2" ||
		!rates[0].FundingTime.Equal(time.UnixMilli(1730419200000)) {
		t.Fatalf("btc = %+v", rates[0])
	}
	// 结算时刻原样保留（不取整到整点），负费率照样解析。
	if rates[1].Rate.String() != "-0.00003562" || rates[1].FundingTime.UnixMilli() != 1730419200001 {
		t.Fatalf("eth = %+v", rates[1])
	}
	// 老记录 markPrice 为空串：按零返回，由模拟盘跳过。
	if !rates[3].MarkPrice.IsZero() {
		t.Fatalf("xrp = %+v", rates[3])
	}
}

func TestFundingRatesSplitFullPagesAndChunks(t *testing.T) {
	// 1500 个合约在同一个整点结算，再加 2 个 1 小时周期合约在 5 小时后结算：第一段满页要拆开，第二段单独一个请求。
	base := int64(1730419200000)
	var rows []fundingRatePayload
	for i := 0; i < 1500; i++ {
		rows = append(rows, fundingRatePayload{Symbol: "S" + strconv.Itoa(i) + "USDT", FundingTime: base + int64(i%3), FundingRate: "0.0001", MarkPrice: "1"})
	}
	later := base + (5 * time.Hour).Milliseconds()
	rows = append(rows,
		fundingRatePayload{Symbol: "AUSDT", FundingTime: later, FundingRate: "0.0002", MarkPrice: "2"},
		fundingRatePayload{Symbol: "BUSDT", FundingTime: later, FundingRate: "-0.0002", MarkPrice: "3"},
	)
	server, requests := fundingServer(t, nil, rows)
	symbols := []string{"AUSDT", "BUSDT"}
	for i := 0; i < 1500; i++ {
		symbols = append(symbols, "S"+strconv.Itoa(i)+"USDT")
	}
	rates, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListFundingRates(context.Background(), time.UnixMilli(base), time.UnixMilli(later), symbols)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, rate := range rates {
		key := rate.ExchangeSymbol + "@" + strconv.FormatInt(rate.FundingTime.UnixMilli(), 10)
		if seen[key] {
			t.Fatalf("重复记录 %s", key)
		}
		seen[key] = true
	}
	if len(rates) != 1502 {
		t.Fatalf("len = %d", len(rates))
	}
	if got := requests(); len(got) < 4 {
		t.Fatalf("满页应拆开重查，requests = %+v", got)
	}
}

func TestFundingRatesRejectSameMillisecondOverflow(t *testing.T) {
	base := int64(1730419200000)
	var rows []fundingRatePayload
	for i := 0; i < fundingRatePageLimit; i++ {
		rows = append(rows, fundingRatePayload{Symbol: "S" + strconv.Itoa(i) + "USDT", FundingTime: base, FundingRate: "0.0001", MarkPrice: "1"})
	}
	server, _ := fundingServer(t, nil, rows)
	if _, err := testAdapter(exchange.MarketTypeSwap, server.URL).ListFundingRates(context.Background(), time.UnixMilli(base), time.UnixMilli(base+10), []string{"S1USDT"}); err == nil {
		t.Fatal("同一毫秒满页时不能当作完整结果")
	}
}

func TestFundingRatesOnlyForSwap(t *testing.T) {
	spot := testAdapter(exchange.MarketTypeSpot, "http://127.0.0.1:1")
	if execution.FundingRatesOf(spot) != nil {
		t.Fatal("现货适配器不应提供资金费率")
	}
	if _, err := spot.ListFundingRates(context.Background(), time.Now(), time.Now(), []string{"BTCUSDT"}); err == nil {
		t.Fatal("现货查询资金费率应被拒绝")
	}
	swap := testAdapter(exchange.MarketTypeSwap, "http://127.0.0.1:1")
	if execution.FundingRatesOf(swap) == nil {
		t.Fatal("永续适配器应提供资金费率")
	}
	combined, err := execution.NewMultiMarket(exchange.ExchangeBinance,
		[]exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		map[exchange.MarketType]execution.ExecutionAdapter{exchange.MarketTypeSpot: spot, exchange.MarketTypeSwap: swap})
	if err != nil {
		t.Fatal(err)
	}
	if execution.FundingRatesOf(combined) == nil {
		t.Fatal("同时开现货和永续的组合适配器应路由到永续")
	}
	spotOnly, err := execution.NewMultiMarket(exchange.ExchangeBinance, []exchange.MarketType{exchange.MarketTypeSpot},
		map[exchange.MarketType]execution.ExecutionAdapter{exchange.MarketTypeSpot: spot})
	if err != nil {
		t.Fatal(err)
	}
	if execution.FundingRatesOf(spotOnly) != nil {
		t.Fatal("没开永续的组合适配器不应提供资金费率")
	}
}

// 只返回、只严格解析要结算的合约：别的合约格式不对只跳过；要结算的合约格式不对整段报错，不能悄悄少算。
func TestFundingRatesOnlyParseWantedSymbols(t *testing.T) {
	base := int64(1730419200000)
	rows := []fundingRatePayload{
		{Symbol: "BTCUSDT", FundingTime: base, FundingRate: "0.0001", MarkPrice: "70000"},
		{Symbol: "BADUSDT", FundingTime: base, FundingRate: "not-a-number", MarkPrice: "1"},
		{Symbol: "", FundingTime: base, FundingRate: "0.0001", MarkPrice: "1"},
		{Symbol: "ETHUSDT", FundingTime: base, FundingRate: "0.0002", MarkPrice: "2500"},
	}
	server, _ := fundingServer(t, nil, rows)
	adapter := testAdapter(exchange.MarketTypeSwap, server.URL)
	rates, err := adapter.ListFundingRates(context.Background(), time.UnixMilli(base), time.UnixMilli(base), []string{"BTCUSDT"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rates) != 1 || rates[0].ExchangeSymbol != "BTCUSDT" {
		t.Fatalf("rates = %+v", rates)
	}
	if _, err := adapter.ListFundingRates(context.Background(), time.UnixMilli(base), time.UnixMilli(base), []string{"BTCUSDT", "BADUSDT"}); err == nil {
		t.Fatal("要结算的合约格式不对时应报错")
	}
}
