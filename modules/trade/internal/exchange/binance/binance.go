// Package binance 实现 Binance 交易所的 ExchangeAdapter。
// 按市场类型路由：现货 /api、U本位合约 /fapi、币本位合约 /dapi。
package binance

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

const (
	baseSpot = "https://api.binance.com"
	baseSwap = "https://fapi.binance.com"

	recvWindow = 5000
)

func init() {
	exchange.Register("binance", func() exchange.ExchangeAdapter { return &Adapter{} })
}

// Adapter 是 Binance 的统一适配实现。
type Adapter struct {
	insCache *instrumentCache
}

// instrumentCache 缓存交易规则，按市场+交易所维度 TTL 失效。
type instrumentCache struct {
	mu      sync.RWMutex
	spot    map[string]exchange.Instrument
	swap    map[string]exchange.Instrument
	spotExp time.Time
	swapExp time.Time
}

var globalInsCache = &instrumentCache{}

func (a *Adapter) Name() string { return "binance" }

func (a *Adapter) ensureCache() *instrumentCache {
	if a.insCache == nil {
		a.insCache = globalInsCache
	}
	return a.insCache
}

// ---- 签名 ----

// sign 计算 HMAC-SHA256(secret, payload) 的十六进制签名。
func sign(secret, payload string) string {
	h := hmacSha256([]byte(secret), []byte(payload))
	return hex.EncodeToString(h)
}

// signedQuery 构造带 timestamp/recvWindow/signature 的签名 query。
func signedQuery(cred exchange.Credential, base url.Values) url.Values {
	q := url.Values{}
	for k, v := range base {
		q[k] = v
	}
	q.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	q.Set("recvWindow", strconv.Itoa(recvWindow))
	payload := q.Encode()
	q.Set("signature", sign(cred.APISecret, payload))
	return q
}

// apiHeader 返回币安签名请求必需的 X-MBX-APIKEY 头。
func apiHeader(cred exchange.Credential) map[string]string {
	return map[string]string{"X-MBX-APIKEY": cred.APIKey}
}

func client(market exchange.MarketType) *httpclient.Client {
	switch market {
	case exchange.MarketSwap, exchange.MarketFutures:
		return httpclient.New(baseSwap)
	default:
		return httpclient.New(baseSpot)
	}
}

func marketPath(market exchange.MarketType, spot, swap string) string {
	switch market {
	case exchange.MarketSwap, exchange.MarketFutures:
		return swap
	default:
		return spot
	}
}

// ---- 元信息 / 通道 ----

// Ping 用账户接口校验凭证连通性。
func (a *Adapter) Ping(ctx context.Context, cred exchange.Credential) (int64, error) {
	start := time.Now()
	c := client(exchange.MarketSpot)
	q := signedQuery(cred, url.Values{})
	if _, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: "/api/v3/account", Query: q, Headers: apiHeader(cred)}); err != nil {
		futuresClient := client(exchange.MarketSwap)
		futuresQuery := signedQuery(cred, url.Values{})
		if _, futuresErr := futuresClient.Do(ctx, &httpclient.Request{Method: "GET", Path: "/fapi/v3/balance", Query: futuresQuery, Headers: apiHeader(cred)}); futuresErr != nil {
			return 0, err
		}
	}
	return time.Since(start).Milliseconds(), nil
}

// GetInstruments 拉取交易规则（带 5 分钟缓存）。
func (a *Adapter) GetInstruments(ctx context.Context, market exchange.MarketType) ([]exchange.Instrument, error) {
	cache := a.ensureCache()
	now := time.Now()
	if market == exchange.MarketSwap || market == exchange.MarketFutures {
		cache.mu.RLock()
		if cache.swap != nil && now.Before(cache.swapExp) {
			out := make([]exchange.Instrument, 0, len(cache.swap))
			for _, ins := range cache.swap {
				out = append(out, ins)
			}
			cache.mu.RUnlock()
			return out, nil
		}
		cache.mu.RUnlock()
		return a.loadSwapInstruments(ctx, cache)
	}
	cache.mu.RLock()
	if cache.spot != nil && now.Before(cache.spotExp) {
		out := make([]exchange.Instrument, 0, len(cache.spot))
		for _, ins := range cache.spot {
			out = append(out, ins)
		}
		cache.mu.RUnlock()
		return out, nil
	}
	cache.mu.RUnlock()
	return a.loadSpotInstruments(ctx, cache)
}

type binanceExchangeInfo struct {
	Symbols []struct {
		Symbol     string `json:"symbol"`
		Status     string `json:"status"`
		BaseAsset  string `json:"baseAsset"`
		QuoteAsset string `json:"quoteAsset"`
		Filters    []struct {
			FilterType  string `json:"filterType"`
			MinPrice    string `json:"minPrice"`
			TickSize    string `json:"tickSize"`
			StepSize    string `json:"stepSize"`
			MinQty      string `json:"minQty"`
			MinNotional string `json:"minNotional"`
		} `json:"filters"`
	} `json:"symbols"`
}

type binanceTickerPrice struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

func (a *Adapter) loadSpotInstruments(ctx context.Context, cache *instrumentCache) ([]exchange.Instrument, error) {
	c := client(exchange.MarketSpot)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: "/api/v3/exchangeInfo"})
	if err != nil {
		return nil, err
	}
	var info binanceExchangeInfo
	if err := httpclient.DecodeJSON(raw, &info); err != nil {
		return nil, fmt.Errorf("parse exchangeInfo: %w", err)
	}
	prices, err := a.loadTickerPrices(ctx, exchange.MarketSpot)
	if err != nil {
		return nil, err
	}
	m := make(map[string]exchange.Instrument, len(info.Symbols))
	for _, s := range info.Symbols {
		ins := exchange.Instrument{Symbol: s.Symbol, Market: exchange.MarketSpot, BaseCcy: s.BaseAsset, QuoteCcy: s.QuoteAsset, LastPrice: prices[s.Symbol], Status: s.Status}
		for _, f := range s.Filters {
			switch f.FilterType {
			case "PRICE_FILTER":
				ins.TickSize = f.TickSize
			case "LOT_SIZE":
				ins.LotSize = f.StepSize
				ins.MinQty = f.MinQty
			case "NOTIONAL", "MIN_NOTIONAL":
				ins.MinNotional = f.MinNotional
			}
		}
		m[s.Symbol] = ins
	}
	cache.mu.Lock()
	cache.spot = m
	cache.spotExp = time.Now().Add(5 * time.Minute)
	cache.mu.Unlock()
	out := make([]exchange.Instrument, 0, len(m))
	for _, ins := range m {
		out = append(out, ins)
	}
	return out, nil
}

func (a *Adapter) loadSwapInstruments(ctx context.Context, cache *instrumentCache) ([]exchange.Instrument, error) {
	c := client(exchange.MarketSwap)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: "/fapi/v1/exchangeInfo"})
	if err != nil {
		return nil, err
	}
	var info binanceExchangeInfo
	if err := httpclient.DecodeJSON(raw, &info); err != nil {
		return nil, fmt.Errorf("parse fapi exchangeInfo: %w", err)
	}
	prices, err := a.loadTickerPrices(ctx, exchange.MarketSwap)
	if err != nil {
		return nil, err
	}
	m := make(map[string]exchange.Instrument, len(info.Symbols))
	for _, s := range info.Symbols {
		ins := exchange.Instrument{Symbol: s.Symbol, Market: exchange.MarketSwap, BaseCcy: s.BaseAsset, QuoteCcy: s.QuoteAsset, LastPrice: prices[s.Symbol], Status: s.Status}
		for _, f := range s.Filters {
			switch f.FilterType {
			case "PRICE_FILTER":
				ins.TickSize = f.TickSize
			case "MARKET_LOT_SIZE":
				ins.LotSize = f.StepSize
				ins.MinQty = f.MinQty
			case "MIN_NOTIONAL":
				ins.MinNotional = f.MinNotional
			}
		}
		m[s.Symbol] = ins
	}
	cache.mu.Lock()
	cache.swap = m
	cache.swapExp = time.Now().Add(5 * time.Minute)
	cache.mu.Unlock()
	out := make([]exchange.Instrument, 0, len(m))
	for _, ins := range m {
		out = append(out, ins)
	}
	return out, nil
}

func (a *Adapter) loadTickerPrices(ctx context.Context, market exchange.MarketType) (map[string]string, error) {
	c := client(market)
	path := marketPath(market, "/api/v3/ticker/price", "/fapi/v1/ticker/price")
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, err
	}
	var arr []binanceTickerPrice
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return nil, fmt.Errorf("parse ticker price: %w", err)
	}
	out := make(map[string]string, len(arr))
	for _, item := range arr {
		out[item.Symbol] = item.Price
	}
	return out, nil
}

// ---- 账户 / 余额 ----

type binanceAccount struct {
	Balances []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	} `json:"balances"`
}

func (a *Adapter) GetAccountInfo(ctx context.Context, cred exchange.Credential, market exchange.MarketType) (*exchange.AccountInfo, error) {
	bs, err := a.GetBalances(ctx, cred, market, nil)
	if err != nil {
		return nil, err
	}
	info := &exchange.AccountInfo{Market: market, Raw: map[string]string{}}
	for _, b := range bs {
		info.Available = b.Available
		info.Frozen = b.Frozen
	}
	return info, nil
}

// GetBalances 现货走 /api/v3/account；U本位合约走 /fapi/v2/balance。
func (a *Adapter) GetBalances(ctx context.Context, cred exchange.Credential, market exchange.MarketType, currencies []string) ([]exchange.Balance, error) {
	if market == exchange.MarketSwap || market == exchange.MarketFutures {
		return a.getSwapBalances(ctx, cred, currencies)
	}
	c := client(market)
	q := signedQuery(cred, url.Values{})
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: "/api/v3/account", Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var acc binanceAccount
	if err := httpclient.DecodeJSON(raw, &acc); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, c := range currencies {
		want[strings.ToUpper(c)] = true
	}
	out := make([]exchange.Balance, 0, len(acc.Balances))
	for _, b := range acc.Balances {
		if len(want) > 0 && !want[strings.ToUpper(b.Asset)] {
			continue
		}
		if b.Free == "0" && b.Locked == "0" && len(want) == 0 {
			continue
		}
		total := decAdd(b.Free, b.Locked)
		out = append(out, exchange.Balance{Currency: b.Asset, Available: b.Free, Frozen: b.Locked, Total: total})
	}
	return out, nil
}

type binanceSwapBalance struct {
	Asset       string `json:"asset"`
	Balance     string `json:"balance"`
	Available   string `json:"availableBalance"`
	CrossUnPnl  string `json:"crossUnPnl"`
	MaintMargin string `json:"maintMargin"`
	MaxWithdraw string `json:"maxWithdrawAmount"`
}

func (a *Adapter) getSwapBalances(ctx context.Context, cred exchange.Credential, currencies []string) ([]exchange.Balance, error) {
	c := client(exchange.MarketSwap)
	q := signedQuery(cred, url.Values{})
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: "/fapi/v3/balance", Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var arr []binanceSwapBalance
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, c := range currencies {
		want[strings.ToUpper(c)] = true
	}
	out := make([]exchange.Balance, 0, len(arr))
	for _, b := range arr {
		if len(want) > 0 && !want[strings.ToUpper(b.Asset)] {
			continue
		}
		avail := b.Available
		if avail == "" {
			avail = b.MaxWithdraw
		}
		out = append(out, exchange.Balance{Currency: b.Asset, Available: avail, Frozen: decSub(b.Balance, avail), Total: b.Balance})
	}
	return out, nil
}

type binanceTradeFee struct {
	Symbol string `json:"symbol"`
	Maker  string `json:"maker"`
	Taker  string `json:"taker"`
}

func (a *Adapter) GetTradeFee(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) (*exchange.FeeRate, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", strings.ToUpper(symbol))
	}
	q := signedQuery(cred, params)
	c := client(exchange.MarketSpot)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: "/sapi/v1/asset/tradeFee", Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var arr []binanceTradeFee
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return &exchange.FeeRate{Symbol: symbol}, nil
	}
	return &exchange.FeeRate{Symbol: arr[0].Symbol, Maker: arr[0].Maker, Taker: arr[0].Taker}, nil
}

// 下单/撤单状态映射。
func mapStatus(s string) exchange.OrderStatus {
	switch strings.ToUpper(s) {
	case "NEW", "PENDING_NEW":
		return exchange.StatusSubmitted
	case "PARTIALLY_FILLED":
		return exchange.StatusPartiallyFilled
	case "FILLED":
		return exchange.StatusFilled
	case "CANCELED", "CANCELLED", "PENDING_CANCEL":
		return exchange.StatusCanceled
	case "REJECTED":
		return exchange.StatusRejected
	case "EXPIRED", "EXPIRED_IN_MATCH":
		return exchange.StatusExpired
	default:
		return exchange.StatusSubmitted
	}
}

// ---- 下单 / 撤单 ----

type binanceOrderResp struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Status        string `json:"status"`
}

func (a *Adapter) PlaceOrder(ctx context.Context, cred exchange.Credential, req *exchange.PlaceOrderReq) (*exchange.OrderResult, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	posSide := binancePositionSide(req.PosSide)
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(req.Symbol))
	params.Set("side", string(req.Side))
	params.Set("type", binanceOrderType(req.Type))
	if posSide != "" && req.Market != exchange.MarketSpot {
		params.Set("positionSide", posSide)
	}
	if req.Price != "" && req.Price != "0" {
		params.Set("price", req.Price)
	}
	if req.Quantity != "" && req.Quantity != "0" {
		params.Set("quantity", req.Quantity)
	}
	if req.Amount != "" && req.Amount != "0" {
		params.Set("quoteOrderQty", req.Amount)
	}
	if req.TimeInForce != "" {
		params.Set("timeInForce", req.TimeInForce)
	} else if req.Type == exchange.TypeLimit {
		params.Set("timeInForce", "GTC")
	}
	if req.ClientOrderID != "" {
		params.Set("newClientOrderId", req.ClientOrderID)
	}
	if binanceShouldSendReduceOnly(req.ReduceOnly, posSide) {
		params.Set("reduceOnly", "true")
	}
	path := marketPath(req.Market, "/api/v3/order", "/fapi/v1/order")
	q := signedQuery(cred, params)
	c := client(req.Market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "POST", Path: path, Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var r binanceOrderResp
	if err := httpclient.DecodeJSON(raw, &r); err != nil {
		return nil, fmt.Errorf("parse order resp: %w", err)
	}
	return &exchange.OrderResult{
		OrderID: r.ClientOrderID, ClientOrderID: r.ClientOrderID,
		ExchangeOrderID: strconv.FormatInt(r.OrderID, 10), Status: mapStatus(r.Status),
	}, nil
}

func binancePositionSide(posSide string) string {
	switch strings.ToUpper(strings.TrimSpace(posSide)) {
	case "":
		return ""
	case "LONG":
		return "LONG"
	case "SHORT":
		return "SHORT"
	case "BOTH", "NET":
		return "BOTH"
	default:
		return strings.ToUpper(strings.TrimSpace(posSide))
	}
}

func binanceShouldSendReduceOnly(reduceOnly bool, posSide string) bool {
	if !reduceOnly {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(posSide)) {
	case "LONG", "SHORT":
		return false
	default:
		return true
	}
}

func (a *Adapter) CancelOrder(ctx context.Context, cred exchange.Credential, req *exchange.CancelOrderReq) (*exchange.OrderResult, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(req.Symbol))
	if req.OrderID != "" {
		params.Set("orderId", req.OrderID)
	}
	if req.ClientOrderID != "" {
		params.Set("origClientOrderId", req.ClientOrderID)
	}
	path := marketPath(req.Market, "/api/v3/order", "/fapi/v1/order")
	q := signedQuery(cred, params)
	c := client(req.Market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "DELETE", Path: path, Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var r binanceOrderResp
	if err := httpclient.DecodeJSON(raw, &r); err != nil {
		return nil, err
	}
	return &exchange.OrderResult{
		OrderID: r.ClientOrderID, ClientOrderID: r.ClientOrderID,
		ExchangeOrderID: strconv.FormatInt(r.OrderID, 10), Status: mapStatus(r.Status),
	}, nil
}

func (a *Adapter) CancelAllOrders(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) (int, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", strings.ToUpper(symbol))
	}
	path := marketPath(market, "/api/v3/openOrders", "/fapi/v1/allOpenOrders")
	q := signedQuery(cred, params)
	c := client(market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "DELETE", Path: path, Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return 0, err
	}
	var arr []binanceOrderResp
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return 0, err
	}
	return len(arr), nil
}

// AmendOrder 现货无原生改单，退化为撤单 + 重下（用 ClientOrderID 保幂等）。
func (a *Adapter) AmendOrder(ctx context.Context, cred exchange.Credential, req *exchange.AmendOrderReq) (*exchange.OrderResult, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	if _, err := a.CancelOrder(ctx, cred, &exchange.CancelOrderReq{
		Market: req.Market, Symbol: req.Symbol, OrderID: req.OrderID, ClientOrderID: req.ClientOrderID,
	}); err != nil {
		return nil, err
	}
	return a.PlaceOrder(ctx, cred, &exchange.PlaceOrderReq{
		Market: req.Market, Symbol: req.Symbol, ClientOrderID: req.ClientOrderID,
		Price: req.NewPrice, Quantity: req.NewQuantity, Type: exchange.TypeLimit,
	})
}

func (a *Adapter) SetLeverage(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol, leverage string) error {
	if market != exchange.MarketSwap && market != exchange.MarketFutures {
		return errNotImplemented
	}
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(symbol))
	params.Set("leverage", leverage)
	q := signedQuery(cred, params)
	c := client(market)
	if _, err := c.Do(ctx, &httpclient.Request{Method: "POST", Path: "/fapi/v1/leverage", Query: q, Headers: apiHeader(cred)}); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) ClosePosition(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol, posSide string) error {
	return errNotImplemented
}

// ---- 查询 ----

type binanceOrderInfo struct {
	OrderID          int64  `json:"orderId"`
	ClientOrderID    string `json:"clientOrderId"`
	Symbol           string `json:"symbol"`
	Side             string `json:"side"`
	Type             string `json:"type"`
	Price            string `json:"price"`
	OrigQty          string `json:"origQty"`
	ExecutedQty      string `json:"executedQty"`
	CummulativeQuote string `json:"cummulativeQuoteQty"`
	CumQuote         string `json:"cumQuote"`
	AvgPrice         string `json:"avgPrice"`
	Status           string `json:"status"`
	Time             int64  `json:"time"`
	UpdateTime       int64  `json:"updateTime"`
}

func (a *Adapter) GetOrder(ctx context.Context, cred exchange.Credential, req *exchange.GetOrderReq) (*exchange.Order, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(req.Symbol))
	if req.OrderID != "" {
		params.Set("orderId", req.OrderID)
	}
	if req.ClientOrderID != "" {
		params.Set("origClientOrderId", req.ClientOrderID)
	}
	path := marketPath(req.Market, "/api/v3/order", "/fapi/v1/order")
	q := signedQuery(cred, params)
	c := client(req.Market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: path, Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var o binanceOrderInfo
	if err := httpclient.DecodeJSON(raw, &o); err != nil {
		return nil, err
	}
	return binanceOrderToDomain(&o, req.Market), nil
}

func (a *Adapter) ListOpenOrders(ctx context.Context, cred exchange.Credential, req *exchange.ListOrdersReq) ([]exchange.Order, error) {
	params := url.Values{}
	if req != nil && req.Symbol != "" {
		params.Set("symbol", strings.ToUpper(req.Symbol))
	}
	path := marketPath(req.Market, "/api/v3/openOrders", "/fapi/v1/openOrders")
	q := signedQuery(cred, params)
	c := client(req.Market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: path, Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var arr []binanceOrderInfo
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Order, 0, len(arr))
	for i := range arr {
		out = append(out, *binanceOrderToDomain(&arr[i], req.Market))
	}
	return out, nil
}

func (a *Adapter) ListOrders(ctx context.Context, cred exchange.Credential, req *exchange.ListOrdersReq) ([]exchange.Order, error) {
	params := url.Values{}
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	params.Set("symbol", strings.ToUpper(req.Symbol))
	if req.Limit > 0 {
		params.Set("limit", strconv.Itoa(req.Limit))
	} else {
		params.Set("limit", "500")
	}
	if req.StartTime > 0 {
		params.Set("startTime", strconv.FormatInt(req.StartTime, 10))
	}
	if req.EndTime > 0 {
		params.Set("endTime", strconv.FormatInt(req.EndTime, 10))
	}
	path := marketPath(req.Market, "/api/v3/allOrders", "/fapi/v1/allOrders")
	q := signedQuery(cred, params)
	c := client(req.Market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: path, Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var arr []binanceOrderInfo
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Order, 0, len(arr))
	for i := range arr {
		out = append(out, *binanceOrderToDomain(&arr[i], req.Market))
	}
	return out, nil
}

type binanceMyTrade struct {
	ID              int64  `json:"id"`
	OrderID         int64  `json:"orderId"`
	Symbol          string `json:"symbol"`
	Side            string `json:"side"`
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	QuoteQty        string `json:"quoteQty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	IsBuyer         bool   `json:"isBuyer"`
	Buyer           bool   `json:"buyer"`
	Maker           bool   `json:"maker"`
	Time            int64  `json:"time"`
}

func (a *Adapter) ListTrades(ctx context.Context, cred exchange.Credential, req *exchange.ListTradesReq) ([]exchange.Trade, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(req.Symbol))
	if req.OrderID != "" {
		params.Set("orderId", req.OrderID)
	}
	if req.Limit > 0 {
		params.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.StartTime > 0 {
		params.Set("startTime", strconv.FormatInt(req.StartTime, 10))
	}
	if req.EndTime > 0 {
		params.Set("endTime", strconv.FormatInt(req.EndTime, 10))
	}
	path := marketPath(req.Market, "/api/v3/myTrades", "/fapi/v1/userTrades")
	q := signedQuery(cred, params)
	c := client(req.Market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: path, Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var arr []binanceMyTrade
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Trade, 0, len(arr))
	for _, t := range arr {
		side := exchange.SideSell
		switch strings.ToUpper(t.Side) {
		case "BUY":
			side = exchange.SideBuy
		case "SELL":
			side = exchange.SideSell
		default:
			if t.IsBuyer || t.Buyer {
				side = exchange.SideBuy
			}
		}
		role := "taker"
		if t.Maker {
			role = "maker"
		}
		out = append(out, exchange.Trade{
			TradeID: strconv.FormatInt(t.ID, 10), ExchangeTradeID: strconv.FormatInt(t.ID, 10),
			OrderID: strconv.FormatInt(t.OrderID, 10), Symbol: t.Symbol, Side: side,
			Price: t.Price, Quantity: t.Qty, Amount: t.QuoteQty,
			Fee: t.Commission, FeeCurrency: t.CommissionAsset, Role: role, TradedAt: t.Time,
		})
	}
	return out, nil
}

func (a *Adapter) ListPositions(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) ([]exchange.Position, error) {
	if market != exchange.MarketSwap && market != exchange.MarketFutures {
		return nil, nil
	}
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", strings.ToUpper(symbol))
	}
	q := signedQuery(cred, params)
	c := client(market)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "GET", Path: "/fapi/v3/positionRisk", Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var arr []struct {
		Symbol           string `json:"symbol"`
		PositionAmt      string `json:"positionAmt"`
		PositionSide     string `json:"positionSide"`
		EntryPrice       string `json:"entryPrice"`
		Leverage         string `json:"leverage"`
		IsolatedMargin   string `json:"isolatedMargin"`
		UnRealizedProfit string `json:"unRealizedProfit"`
		LiquidationPrice string `json:"liquidationPrice"`
		UpdateTime       int64  `json:"updateTime"`
	}
	if err := httpclient.DecodeJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Position, 0, len(arr))
	for _, p := range arr {
		if symbol != "" && !strings.EqualFold(p.Symbol, symbol) {
			continue
		}
		if p.PositionAmt == "0" {
			continue
		}
		posSide := strings.ToLower(p.PositionSide)
		if posSide == "" || posSide == "both" {
			posSide = "long"
		}
		if strings.HasPrefix(p.PositionAmt, "-") {
			posSide = "short"
		}
		out = append(out, exchange.Position{
			Symbol: p.Symbol, PosSide: posSide, Quantity: p.PositionAmt,
			AvgPrice: p.EntryPrice, Leverage: p.Leverage, LiqPrice: p.LiquidationPrice,
			Margin: p.IsolatedMargin, UnrealizedPnl: p.UnRealizedProfit, UpdatedAt: p.UpdateTime,
		})
	}
	return out, nil
}

// ---- 资金 ----

func (a *Adapter) ListFundFlows(ctx context.Context, cred exchange.Credential, req *exchange.FundFlowQuery) ([]exchange.FundFlow, error) {
	return nil, errNotImplemented
}

func (a *Adapter) Transfer(ctx context.Context, cred exchange.Credential, req *exchange.TransferReq) (*exchange.TransferResult, error) {
	if req == nil || req.Currency == "" || req.Amount == "" {
		return nil, errInvalidParam
	}
	params := url.Values{}
	params.Set("asset", strings.ToUpper(req.Currency))
	params.Set("amount", req.Amount)
	params.Set("type", binanceTransferType(req.From, req.To))
	if req.Remark != "" {
		params.Set("loanId", req.Remark) // 复用字段承载备注/标识
	}
	q := signedQuery(cred, params)
	c := client(exchange.MarketSpot)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "POST", Path: "/sapi/v1/asset/transfer", Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var r struct {
		TranID int64 `json:"tranId"`
	}
	if err := httpclient.DecodeJSON(raw, &r); err != nil {
		return nil, err
	}
	return &exchange.TransferResult{TransferID: strconv.FormatInt(r.TranID, 10)}, nil
}

// ListConvertibleDustAssets 查询当前 Binance 现货可转换为 BNB 的小额资产。
func (a *Adapter) ListConvertibleDustAssets(ctx context.Context, cred exchange.Credential, req *exchange.DustConvertibleReq) ([]exchange.DustConvertibleAsset, error) {
	accountType := "SPOT"
	if req != nil && strings.TrimSpace(req.AccountType) != "" {
		accountType = strings.ToUpper(strings.TrimSpace(req.AccountType))
	}
	params := url.Values{}
	params.Set("accountType", accountType)
	q := signedQuery(cred, params)
	c := client(exchange.MarketSpot)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "POST", Path: "/sapi/v1/asset/dust-btc", Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var r struct {
		Details []struct {
			Asset            string `json:"asset"`
			AssetFullName    string `json:"assetFullName"`
			AmountFree       string `json:"amountFree"`
			ToBTC            string `json:"toBTC"`
			ToBNB            string `json:"toBNB"`
			ToBNBOffExchange string `json:"toBNBOffExchange"`
			Exchange         string `json:"exchange"`
		} `json:"details"`
	}
	if err := httpclient.DecodeJSON(raw, &r); err != nil {
		return nil, fmt.Errorf("parse convertible dust assets: %w", err)
	}
	out := make([]exchange.DustConvertibleAsset, 0, len(r.Details))
	for _, item := range r.Details {
		out = append(out, exchange.DustConvertibleAsset{
			Asset:            item.Asset,
			AssetFullName:    item.AssetFullName,
			AmountFree:       item.AmountFree,
			ToBTC:            item.ToBTC,
			ToBNB:            item.ToBNB,
			ToBNBOffExchange: item.ToBNBOffExchange,
			Exchange:         item.Exchange,
		})
	}
	return out, nil
}

// ConvertDust 将 Binance 现货小额资产转换为 BNB。
func (a *Adapter) ConvertDust(ctx context.Context, cred exchange.Credential, req *exchange.DustTransferReq) (*exchange.DustTransferResult, error) {
	if req == nil || len(req.Assets) == 0 {
		return nil, errInvalidParam
	}
	assets := make([]string, 0, len(req.Assets))
	for _, asset := range req.Assets {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset != "" {
			assets = append(assets, asset)
		}
	}
	if len(assets) == 0 {
		return nil, errInvalidParam
	}
	accountType := strings.ToUpper(strings.TrimSpace(req.AccountType))
	if accountType == "" {
		accountType = "SPOT"
	}
	params := url.Values{}
	params.Set("asset", strings.Join(assets, ","))
	params.Set("accountType", accountType)
	q := signedQuery(cred, params)
	c := client(exchange.MarketSpot)
	raw, err := c.Do(ctx, &httpclient.Request{Method: "POST", Path: "/sapi/v1/asset/dust", Query: q, Headers: apiHeader(cred)})
	if err != nil {
		return nil, err
	}
	var r struct {
		TotalServiceCharge string `json:"totalServiceCharge"`
		TotalTransfered    string `json:"totalTransfered"`
		TransferResult     []struct {
			Amount              string `json:"amount"`
			FromAsset           string `json:"fromAsset"`
			OperateTime         int64  `json:"operateTime"`
			ServiceChargeAmount string `json:"serviceChargeAmount"`
			TranID              int64  `json:"tranId"`
			TransferedAmount    string `json:"transferedAmount"`
		} `json:"transferResult"`
	}
	if err := httpclient.DecodeJSON(raw, &r); err != nil {
		return nil, fmt.Errorf("parse dust transfer: %w", err)
	}
	out := &exchange.DustTransferResult{
		TotalServiceCharge: r.TotalServiceCharge,
		TotalTransfered:    r.TotalTransfered,
		Results:            make([]exchange.DustTransferItem, 0, len(r.TransferResult)),
	}
	for _, item := range r.TransferResult {
		out.Results = append(out.Results, exchange.DustTransferItem{
			Asset:               item.FromAsset,
			Amount:              item.Amount,
			OperateTime:         item.OperateTime,
			ServiceChargeAmount: item.ServiceChargeAmount,
			TranID:              item.TranID,
			TransferedAmount:    item.TransferedAmount,
		})
	}
	return out, nil
}

// ---- 辅助 ----

func binanceOrderType(t exchange.OrderType) string {
	switch t {
	case exchange.TypeMarket:
		return "MARKET"
	case exchange.TypeLimit:
		return "LIMIT"
	case exchange.TypeStopLimit:
		return "STOP_LOSS_LIMIT"
	case exchange.TypeStop:
		return "STOP_LOSS"
	case exchange.TypeIOC:
		return "LIMIT"
	case exchange.TypeFOK:
		return "LIMIT"
	case exchange.TypePostOnly:
		return "LIMIT_MAKER"
	default:
		return "LIMIT"
	}
}

func binanceTransferType(from, to exchange.MarketType) string {
	// Binance asset transfer type: MAIN_UMFUTURE / UMFUTURE_MAIN ...
	if from == exchange.MarketSpot && (to == exchange.MarketSwap || to == exchange.MarketFutures) {
		return "MAIN_UMFUTURE"
	}
	if (from == exchange.MarketSwap || from == exchange.MarketFutures) && to == exchange.MarketSpot {
		return "UMFUTURE_MAIN"
	}
	return "MAIN_UMFUTURE"
}

func binanceOrderToDomain(o *binanceOrderInfo, market exchange.MarketType) *exchange.Order {
	filledAmount := o.CummulativeQuote
	if filledAmount == "" {
		filledAmount = o.CumQuote
	}
	return &exchange.Order{
		OrderID: o.ClientOrderID, ClientOrderID: o.ClientOrderID,
		ExchangeOrderID: strconv.FormatInt(o.OrderID, 10),
		Symbol:          o.Symbol, Market: market,
		Side: exchange.OrderSide(strings.ToLower(o.Side)), Type: exchange.OrderType(strings.ToLower(o.Type)),
		Price: o.Price, Quantity: o.OrigQty, FilledQty: o.ExecutedQty,
		FilledAmount: filledAmount, AvgPrice: o.AvgPrice, Status: mapStatus(o.Status),
		CreatedAt: o.Time, UpdatedAt: o.UpdateTime,
	}
}
