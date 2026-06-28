// Package okx 实现 OKX 交易所的 ExchangeAdapter（V5 API）。
// 签名：HMAC-SHA256(secret, timestamp+method+requestPath+body) 后 base64；
// 鉴权头：OK-ACCESS-KEY / OK-ACCESS-SIGN / OK-ACCESS-TIMESTAMP / OK-ACCESS-PASSPHRASE。
package okx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

const baseURL = "https://www.okx.com"

var (
	errNotImplemented = errors.New("okx: not implemented for this market")
	errInvalidParam   = errors.New("okx: invalid parameter")
)

func init() {
	exchange.Register("okx", func() exchange.ExchangeAdapter { return &Adapter{} })
}

// Adapter 是 OKX 的统一适配实现。
type Adapter struct {
	insMu  sync.RWMutex
	ins    map[string]exchange.Instrument
	insExp time.Time
}

func (a *Adapter) Name() string { return "okx" }

func client() *httpclient.Client { return httpclient.New(baseURL) }

// ---- 签名 ----

func isoTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func sign(secret, ts, method, path, body string) string {
	payload := ts + strings.ToUpper(method) + path + body
	h := hmacSha256([]byte(secret), []byte(payload))
	return base64.StdEncoding.EncodeToString(h)
}

func authHeaders(cred exchange.Credential, method, path, body string) map[string]string {
	ts := isoTimestamp()
	return map[string]string{
		"OK-ACCESS-KEY":        cred.APIKey,
		"OK-ACCESS-SIGN":       sign(cred.APISecret, ts, method, path, body),
		"OK-ACCESS-TIMESTAMP":  ts,
		"OK-ACCESS-PASSPHRASE": cred.Passphrase,
		"Content-Type":         "application/json",
	}
}

// okxResp V5 通用响应壳。
type okxResp struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// doSign 执行签名请求并解析 V5 响应 data 字段。
func doSign(ctx context.Context, cred exchange.Credential, method, path string, query url.Values, body string) (json.RawMessage, error) {
	full := path
	if len(query) > 0 {
		full = path + "?" + query.Encode()
	}
	hdrs := authHeaders(cred, method, full, body)
	raw, err := client().Do(ctx, &httpclient.Request{Method: method, Path: full, Body: []byte(body), Headers: hdrs})
	if err != nil {
		return nil, err
	}
	var r okxResp
	if err := httpclient.DecodeJSON(raw, &r); err != nil {
		return nil, fmt.Errorf("parse okx resp: %w", err)
	}
	if r.Code != "0" {
		return nil, fmt.Errorf("okx code %s: %s", r.Code, r.Msg)
	}
	return r.Data, nil
}

func doPublic(ctx context.Context, method, path string, query url.Values) (json.RawMessage, error) {
	full := path
	if len(query) > 0 {
		full = path + "?" + query.Encode()
	}
	raw, err := client().Do(ctx, &httpclient.Request{Method: method, Path: full})
	if err != nil {
		return nil, err
	}
	var r okxResp
	if err := httpclient.DecodeJSON(raw, &r); err != nil {
		return nil, fmt.Errorf("parse okx resp: %w", err)
	}
	if r.Code != "0" {
		return nil, fmt.Errorf("okx code %s: %s", r.Code, r.Msg)
	}
	return r.Data, nil
}

// instType 把市场类型映射为 OKX instType。
func instType(market exchange.MarketType) string {
	switch market {
	case exchange.MarketSwap:
		return "SWAP"
	case exchange.MarketFutures:
		return "FUTURES"
	case exchange.MarketMargin:
		return "MARGIN"
	default:
		return "SPOT"
	}
}

// ---- 元信息 / 通道 ----

func (a *Adapter) Ping(ctx context.Context, cred exchange.Credential) (int64, error) {
	start := time.Now()
	if _, err := a.GetBalances(ctx, cred, exchange.MarketSpot, nil); err != nil {
		return 0, err
	}
	return time.Since(start).Milliseconds(), nil
}

type okxInstrument struct {
	InstID   string `json:"instId"`
	InstType string `json:"instType"`
	BaseCcy  string `json:"baseCcy"`
	QuoteCcy string `json:"quoteCcy"`
	TickSz   string `json:"tickSz"`
	LotSz    string `json:"lotSz"`
	MinSz    string `json:"minSz"`
	State    string `json:"state"`
}

func (a *Adapter) GetInstruments(ctx context.Context, market exchange.MarketType) ([]exchange.Instrument, error) {
	a.insMu.RLock()
	if a.ins != nil && time.Now().Before(a.insExp) {
		out := make([]exchange.Instrument, 0, len(a.ins))
		for _, ins := range a.ins {
			out = append(out, ins)
		}
		a.insMu.RUnlock()
		return out, nil
	}
	a.insMu.RUnlock()

	data, err := doPublic(ctx, "GET", "/api/v5/public/instruments", url.Values{"instType": []string{instType(market)}})
	if err != nil {
		return nil, err
	}
	var arr []okxInstrument
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	m := make(map[string]exchange.Instrument, len(arr))
	for _, it := range arr {
		m[it.InstID] = exchange.Instrument{
			Symbol: it.InstID, Market: market, BaseCcy: it.BaseCcy, QuoteCcy: it.QuoteCcy,
			TickSize: it.TickSz, LotSize: it.LotSz, MinQty: it.MinSz, Status: it.State,
		}
	}
	a.insMu.Lock()
	a.ins = m
	a.insExp = time.Now().Add(5 * time.Minute)
	a.insMu.Unlock()
	out := make([]exchange.Instrument, 0, len(m))
	for _, ins := range m {
		out = append(out, ins)
	}
	return out, nil
}

// ---- 账户 / 余额 ----

type okxBalance struct {
	Ccy     string `json:"ccy"`
	AvailBal string `json:"availBal"`
	FrozenBal string `json:"frozenBal"`
	Eq      string `json:"eq"`
}

func (a *Adapter) GetBalances(ctx context.Context, cred exchange.Credential, market exchange.MarketType, currencies []string) ([]exchange.Balance, error) {
	data, err := doSign(ctx, cred, "GET", "/api/v5/account/balance", nil, "")
	if err != nil {
		return nil, err
	}
	var top []struct {
		Data []okxBalance `json:"data"`
	}
	if err := httpclient.DecodeJSON(data, &top); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, c := range currencies {
		want[strings.ToUpper(c)] = true
	}
	out := make([]exchange.Balance, 0)
	for _, t := range top {
		for _, b := range t.Data {
			if len(want) > 0 && !want[strings.ToUpper(b.Ccy)] {
				continue
			}
			avail := b.AvailBal
			if avail == "" {
				avail = b.Eq
			}
			total := b.Eq
			if total == "" {
				total = decAdd(avail, b.FrozenBal)
			}
			out = append(out, exchange.Balance{Currency: b.Ccy, Available: avail, Frozen: b.FrozenBal, Total: total})
		}
	}
	return out, nil
}

func (a *Adapter) GetAccountInfo(ctx context.Context, cred exchange.Credential, market exchange.MarketType) (*exchange.AccountInfo, error) {
	data, err := doSign(ctx, cred, "GET", "/api/v5/account/balance", nil, "")
	if err != nil {
		return nil, err
	}
	var top []struct {
		TotalEq string `json:"totalEq"`
	}
	if err := httpclient.DecodeJSON(data, &top); err != nil {
		return nil, err
	}
	info := &exchange.AccountInfo{Market: market, Raw: map[string]string{}}
	if len(top) > 0 {
		info.TotalEq = top[0].TotalEq
	}
	return info, nil
}

type okxTradeFee struct {
	TakerFee string `json:"taker"`
	MakerFee string `json:"maker"`
}

func (a *Adapter) GetTradeFee(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) (*exchange.FeeRate, error) {
	q := url.Values{}
	q.Set("instType", instType(market))
	if symbol != "" {
		q.Set("instId", symbol)
	}
	data, err := doSign(ctx, cred, "GET", "/api/v5/account/trade-fee", q, "")
	if err != nil {
		return nil, err
	}
	var arr []okxTradeFee
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return &exchange.FeeRate{Symbol: symbol}, nil
	}
	return &exchange.FeeRate{Symbol: symbol, Maker: arr[0].MakerFee, Taker: arr[0].TakerFee}, nil
}

// ---- 下单 / 撤单 ----

func mapStatus(s string) exchange.OrderStatus {
	switch strings.ToUpper(s) {
	case "live":
		return exchange.StatusSubmitted
	case "partially_filled":
		return exchange.StatusPartiallyFilled
	case "filled":
		return exchange.StatusFilled
	case "canceled", "mmp_canceled":
		return exchange.StatusCanceled
	default:
		return exchange.StatusSubmitted
	}
}

type okxOrderResp struct {
	OrdID   string `json:"ordId"`
	ClOrdID string `json:"clOrdId"`
	SCode   string `json:"sCode"`
	SMsg    string `json:"sMsg"`
	State   string `json:"state"`
}

func (a *Adapter) PlaceOrder(ctx context.Context, cred exchange.Credential, req *exchange.PlaceOrderReq) (*exchange.OrderResult, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	side := "buy"
	if req.Side == exchange.SideSell {
		side = "sell"
	}
	ordType := okxOrderType(req.Type)
	body := map[string]string{
		"instId":  req.Symbol,
		"tdMode":  okxTdMode(req.Market),
		"side":    side,
		"ordType": ordType,
		"clOrdId": req.ClientOrderID,
	}
	if req.Quantity != "" && req.Quantity != "0" {
		body["sz"] = req.Quantity
	}
	if req.Price != "" && req.Price != "0" {
		body["px"] = req.Price
	}
	if req.Amount != "" && req.Amount != "0" {
		// 市价买单按金额：tgtCcy=quote_ccy，sz=amount
		body["sz"] = req.Amount
		body["tgtCcy"] = "quote_ccy"
	}
	if req.PosSide != "" {
		body["posSide"] = req.PosSide
	}
	if req.ReduceOnly {
		body["reduceOnly"] = "true"
	}
	bodyStr := jsonMarshal(body)
	data, err := doSign(ctx, cred, "POST", "/api/v5/trade/order", nil, bodyStr)
	if err != nil {
		return nil, err
	}
	var arr []okxOrderResp
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("okx: empty order response")
	}
	r := arr[0]
	if r.SCode != "0" {
		return nil, fmt.Errorf("okx place: %s", r.SMsg)
	}
	return &exchange.OrderResult{OrderID: r.ClOrdID, ClientOrderID: r.ClOrdID, ExchangeOrderID: r.OrdID, Status: mapStatus(r.State)}, nil
}

func (a *Adapter) CancelOrder(ctx context.Context, cred exchange.Credential, req *exchange.CancelOrderReq) (*exchange.OrderResult, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	body := map[string]string{"instId": req.Symbol}
	if req.OrderID != "" {
		body["ordId"] = req.OrderID
	}
	if req.ClientOrderID != "" {
		body["clOrdId"] = req.ClientOrderID
	}
	bodyStr := jsonMarshal(body)
	data, err := doSign(ctx, cred, "POST", "/api/v5/trade/cancel-order", nil, bodyStr)
	if err != nil {
		return nil, err
	}
	var arr []okxOrderResp
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("okx: empty cancel response")
	}
	r := arr[0]
	return &exchange.OrderResult{OrderID: r.ClOrdID, ClientOrderID: r.ClOrdID, ExchangeOrderID: r.OrdID, Status: exchange.StatusCanceled}, nil
}

func (a *Adapter) CancelAllOrders(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) (int, error) {
	// OKX 无单接口「撤销全部」；用批量撤销需先查挂单。MVP 阶段返回未实现，由上层循环调用 CancelOrder。
	return 0, errNotImplemented
}

func (a *Adapter) AmendOrder(ctx context.Context, cred exchange.Credential, req *exchange.AmendOrderReq) (*exchange.OrderResult, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	body := map[string]string{"instId": req.Symbol}
	if req.OrderID != "" {
		body["ordId"] = req.OrderID
	}
	if req.ClientOrderID != "" {
		body["clOrdId"] = req.ClientOrderID
	}
	if req.NewPrice != "" {
		body["newPx"] = req.NewPrice
	}
	if req.NewQuantity != "" {
		body["newSz"] = req.NewQuantity
	}
	bodyStr := jsonMarshal(body)
	data, err := doSign(ctx, cred, "POST", "/api/v5/trade/amend-order", nil, bodyStr)
	if err != nil {
		return nil, err
	}
	var arr []okxOrderResp
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("okx: empty amend response")
	}
	r := arr[0]
	return &exchange.OrderResult{OrderID: r.ClOrdID, ClientOrderID: r.ClOrdID, ExchangeOrderID: r.OrdID, Status: mapStatus(r.State)}, nil
}

func (a *Adapter) SetLeverage(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol, leverage string) error {
	if market == exchange.MarketSpot {
		return errNotImplemented
	}
	body := map[string]string{"instId": symbol, "lever": leverage, "mgnMode": "cross"}
	bodyStr := jsonMarshal(body)
	_, err := doSign(ctx, cred, "POST", "/api/v5/account/set-leverage", nil, bodyStr)
	return err
}

func (a *Adapter) ClosePosition(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol, posSide string) error {
	body := map[string]string{"instId": symbol, "mgnMode": "cross"}
	if posSide != "" {
		body["posSide"] = posSide
	}
	bodyStr := jsonMarshal(body)
	_, err := doSign(ctx, cred, "POST", "/api/v5/trade/close-position", nil, bodyStr)
	return err
}

// ---- 查询 ----

type okxOrderInfo struct {
	OrdID    string `json:"ordId"`
	ClOrdID  string `json:"clOrdId"`
	InstID   string `json:"instId"`
	Side     string `json:"side"`
	OrdType  string `json:"ordType"`
	Px       string `json:"px"`
	Sz       string `json:"sz"`
	FillSz   string `json:"fillSz"`
	FillPx   string `json:"fillPx"`
	AvgPx    string `json:"avgPx"`
	State    string `json:"state"`
	Fee      string `json:"fee"`
	FeeCcy   string `json:"feeCcy"`
	CTime    string `json:"cTime"`
	UTime    string `json:"uTime"`
}

func okxOrderToDomain(o *okxOrderInfo, market exchange.MarketType) *exchange.Order {
	return &exchange.Order{
		OrderID: o.ClOrdID, ClientOrderID: o.ClOrdID, ExchangeOrderID: o.OrdID,
		Symbol: o.InstID, Market: market, Side: exchange.OrderSide(o.Side),
		Type: exchange.OrderType(o.OrdType), Price: o.Px, Quantity: o.Sz,
		FilledQty: o.FillSz, AvgPrice: o.AvgPx, Fee: o.Fee, FeeCurrency: o.FeeCcy,
		Status: mapStatus(o.State),
	}
}

func (a *Adapter) GetOrder(ctx context.Context, cred exchange.Credential, req *exchange.GetOrderReq) (*exchange.Order, error) {
	if req == nil || req.Symbol == "" {
		return nil, errInvalidParam
	}
	q := url.Values{}
	q.Set("instId", req.Symbol)
	if req.OrderID != "" {
		q.Set("ordId", req.OrderID)
	}
	if req.ClientOrderID != "" {
		q.Set("clOrdId", req.ClientOrderID)
	}
	data, err := doSign(ctx, cred, "GET", "/api/v5/trade/order", q, "")
	if err != nil {
		return nil, err
	}
	var arr []okxOrderInfo
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("okx: order not found")
	}
	return okxOrderToDomain(&arr[0], req.Market), nil
}

func (a *Adapter) ListOpenOrders(ctx context.Context, cred exchange.Credential, req *exchange.ListOrdersReq) ([]exchange.Order, error) {
	q := url.Values{}
	if req != nil && req.Symbol != "" {
		q.Set("instId", req.Symbol)
	}
	if req != nil && req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	data, err := doSign(ctx, cred, "GET", "/api/v5/trade/orders-pending", q, "")
	if err != nil {
		return nil, err
	}
	var arr []okxOrderInfo
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Order, 0, len(arr))
	for i := range arr {
		out = append(out, *okxOrderToDomain(&arr[i], req.Market))
	}
	return out, nil
}

func (a *Adapter) ListOrders(ctx context.Context, cred exchange.Credential, req *exchange.ListOrdersReq) ([]exchange.Order, error) {
	q := url.Values{}
	q.Set("instType", instType(req.Market))
	if req.Symbol != "" {
		q.Set("instId", req.Symbol)
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	data, err := doSign(ctx, cred, "GET", "/api/v5/trade/orders-history", q, "")
	if err != nil {
		return nil, err
	}
	var arr []okxOrderInfo
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Order, 0, len(arr))
	for i := range arr {
		out = append(out, *okxOrderToDomain(&arr[i], req.Market))
	}
	return out, nil
}

type okxFill struct {
	TradeID string `json:"tradeId"`
	OrdID   string `json:"ordId"`
	InstID  string `json:"instId"`
	Side    string `json:"side"`
	FillPx  string `json:"fillPx"`
	FillSz  string `json:"fillSz"`
	Fee     string `json:"fee"`
	FeeCcy  string `json:"feeCcy"`
	Ts      string `json:"ts"`
}

func (a *Adapter) ListTrades(ctx context.Context, cred exchange.Credential, req *exchange.ListTradesReq) ([]exchange.Trade, error) {
	q := url.Values{}
	if req != nil && req.Symbol != "" {
		q.Set("instId", req.Symbol)
	}
	if req != nil && req.OrderID != "" {
		q.Set("ordId", req.OrderID)
	}
	if req != nil && req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	data, err := doSign(ctx, cred, "GET", "/api/v5/trade/fills", q, "")
	if err != nil {
		return nil, err
	}
	var arr []okxFill
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Trade, 0, len(arr))
	for _, f := range arr {
		ts, _ := strconv.ParseInt(f.Ts, 10, 64)
		out = append(out, exchange.Trade{
			TradeID: f.TradeID, ExchangeTradeID: f.TradeID, OrderID: f.OrdID,
			Symbol: f.InstID, Side: exchange.OrderSide(f.Side), Price: f.FillPx,
			Quantity: f.FillSz, Fee: f.Fee, FeeCurrency: f.FeeCcy, TradedAt: ts,
		})
	}
	return out, nil
}

type okxPosition struct {
	InstID  string `json:"instId"`
	PosSide string `json:"posSide"`
	Pos     string `json:"pos"`
	AvgPx   string `json:"avgPx"`
	Lever   string `json:"lever"`
	Margin  string `json:"margin"`
	LiqPx   string `json:"liqPx"`
	Upl     string `json:"upl"`
}

func (a *Adapter) ListPositions(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) ([]exchange.Position, error) {
	q := url.Values{}
	if symbol != "" {
		q.Set("instId", symbol)
	}
	data, err := doSign(ctx, cred, "GET", "/api/v5/account/positions", q, "")
	if err != nil {
		return nil, err
	}
	var arr []okxPosition
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.Position, 0, len(arr))
	for _, p := range arr {
		if p.Pos == "0" {
			continue
		}
		out = append(out, exchange.Position{
			Symbol: p.InstID, PosSide: p.PosSide, Quantity: p.Pos, AvgPrice: p.AvgPx,
			Leverage: p.Lever, Margin: p.Margin, LiqPrice: p.LiqPx, UnrealizedPnl: p.Upl,
		})
	}
	return out, nil
}

// ---- 资金 ----

func (a *Adapter) ListFundFlows(ctx context.Context, cred exchange.Credential, req *exchange.FundFlowQuery) ([]exchange.FundFlow, error) {
	q := url.Values{}
	if req != nil && req.Currency != "" {
		q.Set("ccy", strings.ToUpper(req.Currency))
	}
	if req != nil && req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	data, err := doSign(ctx, cred, "GET", "/api/v5/asset/bills", q, "")
	if err != nil {
		return nil, err
	}
	var arr []struct {
		BillID string `json:"billId"`
		Ccy    string `json:"ccy"`
		Type   string `json:"type"`
		Amt    string `json:"amt"`
		BalChg string `json:"balChg"`
		Ts     string `json:"ts"`
	}
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	out := make([]exchange.FundFlow, 0, len(arr))
	for _, b := range arr {
		ts, _ := strconv.ParseInt(b.Ts, 10, 64)
		dir := 1
		if strings.HasPrefix(b.BalChg, "-") {
			dir = -1
		}
		out = append(out, exchange.FundFlow{FlowID: b.BillID, Currency: b.Ccy, BizType: b.Type, Direction: dir, Amount: b.Amt, Balance: b.BalChg, Timestamp: ts})
	}
	return out, nil
}

func (a *Adapter) Transfer(ctx context.Context, cred exchange.Credential, req *exchange.TransferReq) (*exchange.TransferResult, error) {
	if req == nil || req.Currency == "" || req.Amount == "" {
		return nil, errInvalidParam
	}
	body := map[string]string{
		"ccy":   strings.ToUpper(req.Currency),
		"amt":   req.Amount,
		"from":  okxAccountType(req.From),
		"to":    okxAccountType(req.To),
		"type":  "0",
	}
	bodyStr := jsonMarshal(body)
	data, err := doSign(ctx, cred, "POST", "/api/v5/asset/transfer", nil, bodyStr)
	if err != nil {
		return nil, err
	}
	var arr []struct {
		TransID string `json:"transId"`
	}
	if err := httpclient.DecodeJSON(data, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("okx: empty transfer response")
	}
	return &exchange.TransferResult{TransferID: arr[0].TransID}, nil
}

// ---- 辅助 ----

func okxOrderType(t exchange.OrderType) string {
	switch t {
	case exchange.TypeMarket:
		return "market"
	case exchange.TypeLimit:
		return "limit"
	case exchange.TypePostOnly:
		return "post_only"
	case exchange.TypeIOC:
		return "ioc"
	case exchange.TypeFOK:
		return "fok"
	case exchange.TypeStopLimit:
		return "conditional"
	default:
		return "limit"
	}
}

func okxTdMode(market exchange.MarketType) string {
	switch market {
	case exchange.MarketSpot:
		return "cash"
	case exchange.MarketMargin:
		return "cross"
	case exchange.MarketSwap, exchange.MarketFutures:
		return "cross"
	default:
		return "cash"
	}
}

func okxAccountType(market exchange.MarketType) string {
	switch market {
	case exchange.MarketSpot, exchange.MarketMargin:
		return "6"
	case exchange.MarketSwap, exchange.MarketFutures:
		return "1"
	default:
		return "6"
	}
}
