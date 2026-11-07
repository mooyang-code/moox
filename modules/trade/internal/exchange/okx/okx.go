package okx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

const defaultBaseURL = "https://openapi.okx.com"

type Adapter struct {
	config     exchange.AccountConfig
	credential exchange.Credential
	client     *httpclient.Client

	mu          sync.RWMutex
	instruments map[exchange.SymbolKey]exchange.Instrument
	prices      exchange.PriceBoard
}

func New(config exchange.AccountConfig, credential exchange.Credential) *Adapter {
	client := httpclient.New(defaultBaseURL)
	client.Classify = classifyRateLimit
	return &Adapter{
		config: config, credential: credential,
		client:      client,
		instruments: make(map[exchange.SymbolKey]exchange.Instrument),
	}
}

// classifyRateLimit：OKX 的 HTTP 429 是按接口和用户的限频，不是 IP 封禁，只暂停这个账户下单。
func classifyRateLimit(status int, _ []byte) exchange.ErrorKind {
	if status == http.StatusTooManyRequests {
		return exchange.ErrorOrderRateLimited
	}
	return ""
}

func (a *Adapter) Exchange() exchange.Exchange { return exchange.ExchangeOKX }

// ownMarket 校验标的键属于本适配器服务的市场：传错市场直接报错，不会悄悄按本市场处理。
func (a *Adapter) ownMarket(key exchange.SymbolKey) error {
	if key.Market != a.config.Market() {
		return &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("标的键 %s 不属于本适配器服务的市场 %s", key, a.config.Market())}
	}
	return nil
}

// key 返回本适配器所服务市场下的标的键。
func (a *Adapter) key(symbol string) exchange.SymbolKey {
	return exchange.SymbolKey{Market: a.config.Market(), Symbol: symbol}
}

// RateStatus 返回限流状态。OKX 没有权重响应头，只有暂停信息。
func (a *Adapter) RateStatus() exchange.RateStatus {
	status := a.client.Status()
	return exchange.RateStatus{
		PausedUntil: status.PausedUntil, PauseReason: status.PauseReason,
		OrdersPausedUntil: status.OrdersPausedUntil,
	}
}

type response struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (a *Adapter) request(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	signed bool,
) (json.RawMessage, error) {
	return a.send(ctx, method, path, query, body, signed, false)
}

// orderRateLimitPause 是 OKX 限流（50011、50040）后该账户下单的暂停时长，OKX 限频窗口是 2 秒。
const orderRateLimitPause = 2 * time.Second

// orderExpiry 是下单请求的有效期：超过后 OKX 不再受理，保证未知结果窗口内不会迟到成交。
const orderExpiry = 5 * time.Second

// send 发请求；order 标记新下单，限流后只暂停这个账户下单。
func (a *Adapter) send(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	signed bool,
	order bool,
) (json.RawMessage, error) {
	fullPath := path
	if len(query) > 0 {
		fullPath += "?" + query.Encode()
	}
	var rawBody []byte
	var err error
	if body != nil {
		rawBody, err = json.Marshal(body)
		if err != nil {
			return nil, rejected("encode request", err)
		}
	}
	headers := map[string]string{}
	if signed {
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		headers = map[string]string{
			"OK-ACCESS-KEY":        a.credential.APIKey,
			"OK-ACCESS-SIGN":       signature(a.credential.APISecret, timestamp, method, fullPath, string(rawBody)),
			"OK-ACCESS-TIMESTAMP":  timestamp,
			"OK-ACCESS-PASSPHRASE": a.credential.Passphrase,
			"Content-Type":         "application/json",
		}
	}
	if a.config.Environment == exchange.AccountEnvironmentTestnet {
		headers["x-simulated-trading"] = "1"
	}
	if order {
		headers["expTime"] = strconv.FormatInt(time.Now().Add(orderExpiry).UnixMilli(), 10)
	}
	raw, err := a.client.Do(ctx, &httpclient.Request{
		Method: method, Path: fullPath, Body: rawBody, Headers: headers, Order: order,
	})
	if err != nil {
		return raw, classify(err, raw)
	}
	var envelope response
	if err := json.Unmarshal(raw, &envelope); err != nil {
		if method != http.MethodGet {
			return nil, transportUnknown("decode OKX mutation response", err)
		}
		return nil, rejected("decode OKX response", err)
	}
	if envelope.Code != "0" {
		var details []struct {
			Code    string `json:"sCode"`
			Message string `json:"sMsg"`
		}
		if err := json.Unmarshal(envelope.Data, &details); err == nil &&
			len(details) != 0 && details[0].Code != "" && details[0].Code != "0" {
			return envelope.Data, a.rateLimited(classifyOKXCode(details[0].Code, details[0].Message), order)
		}
		return envelope.Data, a.rateLimited(classifyOKXCode(envelope.Code, envelope.Msg), order)
	}
	return envelope.Data, nil
}

// rateLimited 把 HTTP 200 里的限流码转成账户下单暂停：httpclient 看不到响应体里的错误码。
// 只有下单请求触发暂停，查询接口的限流不影响下单。
func (a *Adapter) rateLimited(err error, order bool) error {
	if order && exchange.IsKind(err, exchange.ErrorOrderRateLimited) {
		a.client.PauseOrders(orderRateLimitPause, exchange.ErrorOrderRateLimited)
	}
	return err
}

func privateStreamEndpoint(
	environment exchange.AccountEnvironment,
) string {
	if environment == exchange.AccountEnvironmentTestnet {
		return "wss://wspap.okx.com:8443/ws/v5/private"
	}
	return "wss://ws.okx.com:8443/ws/v5/private"
}

func signature(secret, timestamp, method, path, body string) string {
	payload := timestamp + strings.ToUpper(method) + path + body
	return base64.StdEncoding.EncodeToString(hmacSha256([]byte(secret), []byte(payload)))
}

func (a *Adapter) instrumentType() string {
	if a.config.Market() == exchange.MarketTypeSwap {
		return "SWAP"
	}
	return "SPOT"
}

type instrumentPayload struct {
	InstID    string `json:"instId"`
	InstType  string `json:"instType"`
	BaseCcy   string `json:"baseCcy"`
	QuoteCcy  string `json:"quoteCcy"`
	SettleCcy string `json:"settleCcy"`
	TickSz    string `json:"tickSz"`
	LotSz     string `json:"lotSz"`
	MinSz     string `json:"minSz"`
	CtVal     string `json:"ctVal"`
	CtValCcy  string `json:"ctValCcy"`
	CtType    string `json:"ctType"`
	State     string `json:"state"`
}

func (a *Adapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	data, err := a.request(ctx, http.MethodGet, "/api/v5/public/instruments", url.Values{
		"instType": []string{a.instrumentType()},
	}, nil, false)
	if err != nil {
		return nil, err
	}
	var payload []instrumentPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, rejected("decode instruments", err)
	}
	out := make([]exchange.Instrument, 0, len(payload))
	for _, row := range payload {
		if row.InstType != a.instrumentType() {
			continue
		}
		baseAsset, quoteAsset := row.BaseCcy, row.QuoteCcy
		if a.config.Market() == exchange.MarketTypeSwap {
			if row.CtType != "linear" || row.SettleCcy != "USDT" ||
				row.CtVal == "" || row.CtValCcy == "" {
				continue
			}
			baseAsset, quoteAsset = row.CtValCcy, row.SettleCcy
		}
		instrumentID, idErr := exchange.CanonicalInstrumentID(
			baseAsset,
			quoteAsset,
			a.config.Market(),
		)
		if idErr != nil {
			return nil, rejected("canonical instrument ID", idErr)
		}
		instrument := exchange.Instrument{
			Exchange: exchange.ExchangeOKX, MarketType: a.config.Market(),
			ExchangeSymbol: row.InstID, InstrumentID: instrumentID,
			BaseAsset: baseAsset, QuoteAsset: quoteAsset,
			Status: row.State, ExchangeUpdatedAt: time.Now().UTC(),
		}
		instrument.PriceTick, err = decimal(row.TickSz)
		if err == nil {
			instrument.ExchangeQuantityStep, err = decimal(row.LotSz)
		}
		if err == nil {
			instrument.MinExchangeQuantity, err = decimal(row.MinSz)
		}
		if err != nil {
			return nil, err
		}
		if a.config.Market() == exchange.MarketTypeSwap {
			instrument.Linear = true
			instrument.SettlementAsset = row.SettleCcy
			instrument.ContractValue, err = decimal(row.CtVal)
			instrument.ContractValueAsset = row.CtValCcy
			if err != nil ||
				instrument.ContractValue.Cmp(shared.Zero()) <= 0 ||
				!strings.EqualFold(instrument.ContractValueAsset, instrument.BaseAsset) {
				continue
			}
		} else {
			instrument.SettlementAsset = row.QuoteCcy
			instrument.ContractValue = shared.MustDecimal("1")
			instrument.ContractValueAsset = row.BaseCcy
		}
		out = append(out, instrument)
	}
	a.mu.Lock()
	a.instruments = make(map[exchange.SymbolKey]exchange.Instrument, len(out))
	for _, instrument := range out {
		a.instruments[a.key(instrument.ExchangeSymbol)] = instrument
	}
	a.mu.Unlock()
	return out, nil
}

func (a *Adapter) GetReferencePrice(
	ctx context.Context,
	key exchange.SymbolKey,
) (exchange.ReferencePrice, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.ReferencePrice{}, err
	}
	symbol := key.Symbol
	if price, at, ok := a.prices.Get(ctx, a.key(symbol), a.allPrices); ok {
		return exchange.ReferencePrice{Price: price, UpdatedAt: at}, nil
	}
	data, err := a.request(
		ctx,
		http.MethodGet,
		"/api/v5/market/ticker",
		url.Values{"instId": []string{symbol}},
		nil,
		false,
	)
	if err != nil {
		return exchange.ReferencePrice{}, err
	}
	var rows []struct {
		Last string `json:"last"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 {
		return exchange.ReferencePrice{}, rejected("decode reference price", err)
	}
	price, err := shared.ParseDecimal(rows[0].Last)
	if err != nil || price.Cmp(shared.Zero()) <= 0 {
		return exchange.ReferencePrice{}, rejected("invalid reference price", err)
	}
	// 时效检查判断的是这次报价是否刚取到，用本地收到时间，不依赖交易所时钟。
	return exchange.ReferencePrice{Price: price, UpdatedAt: time.Now().UTC()}, nil
}

func (a *Adapter) allPrices(ctx context.Context) (map[exchange.SymbolKey]shared.Decimal, error) {
	data, err := a.request(ctx, http.MethodGet, "/api/v5/market/tickers", url.Values{"instType": []string{string(a.config.Market())}}, nil, false)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		InstID string `json:"instId"`
		Last   string `json:"last"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, rejected("decode all prices", err)
	}
	prices := make(map[exchange.SymbolKey]shared.Decimal, len(rows))
	for _, row := range rows {
		if price, err := shared.ParseDecimal(row.Last); err == nil {
			prices[a.key(row.InstID)] = price
		}
	}
	return prices, nil
}

func parseQuote(raw string) shared.Decimal {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}

func (a *Adapter) GetQuote(ctx context.Context, key exchange.SymbolKey) (execution.MarketQuote, error) {
	if err := a.ownMarket(key); err != nil {
		return execution.MarketQuote{}, err
	}
	data, err := a.request(ctx, http.MethodGet, "/api/v5/market/ticker", url.Values{"instId": []string{key.Symbol}}, nil, false)
	if err != nil {
		return execution.MarketQuote{}, err
	}
	var rows []struct {
		Bid  string `json:"bidPx"`
		Ask  string `json:"askPx"`
		Last string `json:"last"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 {
		return execution.MarketQuote{}, rejected("decode quote", err)
	}
	return execution.MarketQuote{Bid: parseQuote(rows[0].Bid), Ask: parseQuote(rows[0].Ask), Last: parseQuote(rows[0].Last), SourceTime: time.Now().UTC()}, nil
}

func (a *Adapter) instrument(symbol string) (exchange.Instrument, error) {
	a.mu.RLock()
	instrument, ok := a.instruments[a.key(symbol)]
	a.mu.RUnlock()
	if !ok {
		return exchange.Instrument{}, &exchange.Error{
			Kind: exchange.ErrorNotReady,
			Err:  fmt.Errorf("OKX instrument %q is not loaded", symbol),
		}
	}
	return instrument, nil
}

func (a *Adapter) toExchangeQuantity(symbol string, base shared.Decimal) (shared.Decimal, error) {
	if a.config.Market() == exchange.MarketTypeSpot {
		return base, nil
	}
	instrument, err := a.instrument(symbol)
	if err != nil {
		return shared.Decimal{}, err
	}
	contracts := base.Div(instrument.ContractValue)
	if contracts.Cmp(instrument.MinExchangeQuantity) < 0 {
		return shared.Decimal{}, rejected("quantity is below OKX minimum contracts", nil)
	}
	steps := contracts.Div(instrument.ExchangeQuantityStep)
	if strings.Contains(steps.String(), ".") || strings.Contains(steps.String(), "/") {
		return shared.Decimal{}, rejected("quantity does not align with OKX lot size", nil)
	}
	if _, err := shared.ParseDecimal(contracts.String()); err != nil {
		return shared.Decimal{}, rejected("contract quantity is not a finite decimal", err)
	}
	return contracts, nil
}

func (a *Adapter) toBaseQuantity(symbol string, contracts shared.Decimal) (shared.Decimal, error) {
	if a.config.Market() == exchange.MarketTypeSpot {
		return contracts, nil
	}
	instrument, err := a.instrument(symbol)
	if err != nil {
		return shared.Decimal{}, err
	}
	base := contracts.Mul(instrument.ContractValue)
	if _, err := shared.ParseDecimal(base.String()); err != nil {
		return shared.Decimal{}, rejected("base quantity is not a finite decimal", err)
	}
	return base, nil
}

type balanceEnvelope struct {
	UTime   string `json:"uTime"`
	TotalEq string `json:"totalEq"`
	Details []struct {
		Ccy       string `json:"ccy"`
		AvailBal  string `json:"availBal"`
		FrozenBal string `json:"frozenBal"`
		CashBal   string `json:"cashBal"`
		Eq        string `json:"eq"`
		AvailEq   string `json:"availEq"`
		Imr       string `json:"imr"`
		Mmr       string `json:"mmr"`
		Upl       string `json:"upl"`
	} `json:"details"`
}

func (a *Adapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	if a.config.Market() == exchange.MarketTypeSwap {
		if err := a.ensureNetPositionMode(ctx); err != nil {
			return exchange.AccountSnapshot{}, err
		}
	}
	data, err := a.request(ctx, http.MethodGet, "/api/v5/account/balance", nil, nil, true)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	var payload []balanceEnvelope
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return exchange.AccountSnapshot{}, rejected("decode account snapshot", err)
	}
	return a.balanceSnapshot(payload[0])
}

// balanceSnapshot 把 REST 余额和私有流 account 推送统一转成账户快照。余额数量取 cashBal（不含未实现盈亏），
// 未实现盈亏只从 upl 取一次；totalEq（全币种折合 USD，账户里有别的币时会和统一权益重复）只记入 ExchangeEquity 供展示核对。
// 推送只带有变化的币种，所以结算币相关字段只有带了结算币明细才标记为存在，否则合并时会把它们写成 0。
func (a *Adapter) balanceSnapshot(row balanceEnvelope) (exchange.AccountSnapshot, error) {
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: millisString(row.UTime),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
	}
	if exchangeEquity, err := decimalOrZero(row.TotalEq); err == nil {
		snapshot.ExchangeEquity = exchangeEquity
	}
	for _, detail := range row.Details {
		// 只用 cashBal：缺失按 0 处理，不退回含未实现盈亏的 eq（会和 upl 重复计入）。
		total, err := decimalOrZero(detail.CashBal)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		availableRaw := detail.AvailEq
		if availableRaw == "" {
			availableRaw = detail.AvailBal
		}
		available, err := decimalOrZero(availableRaw)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		locked, err := decimalOrZero(detail.FrozenBal)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Wallet: exchange.WalletUnified,
			Asset:  detail.Ccy, Available: available, Locked: locked, Total: total,
		})
		if !strings.EqualFold(detail.Ccy, a.config.SettlementAsset) {
			continue
		}
		snapshot.Equity = total
		snapshot.AvailableFunds = available
		snapshot.UsedMargin, err = decimalOrZero(detail.Imr)
		if err == nil {
			snapshot.MaintenanceMargin, err = decimalOrZero(detail.Mmr)
		}
		if err == nil {
			snapshot.UnrealizedPnL, err = decimalOrZero(detail.Upl)
		}
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		snapshot.Present.Equity = true
		snapshot.Present.AvailableFunds = true
		snapshot.Present.UsedMargin = true
		snapshot.Present.MaintenanceMargin = true
		snapshot.Present.UnrealizedPnL = true
	}
	return snapshot, nil
}

func (a *Adapter) ensureNetPositionMode(ctx context.Context) error {
	data, err := a.request(ctx, http.MethodGet, "/api/v5/account/config", nil, nil, true)
	if err != nil {
		return err
	}
	var payload []struct {
		PositionMode string `json:"posMode"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return rejected("decode OKX account configuration", err)
	}
	if payload[0].PositionMode != "net_mode" {
		return rejected("OKX hedge position mode is unsupported", nil)
	}
	return nil
}

type positionPayload struct {
	InstID   string `json:"instId"`
	PosSide  string `json:"posSide"`
	Pos      string `json:"pos"`
	AvgPx    string `json:"avgPx"`
	MarkPx   string `json:"markPx"`
	Lever    string `json:"lever"`
	MgnMode  string `json:"mgnMode"`
	Margin   string `json:"margin"`
	IMR      string `json:"imr"`
	LiqPx    string `json:"liqPx"`
	Upl      string `json:"upl"`
	Realized string `json:"realizedPnl"`
	UTime    string `json:"uTime"`
}

func (a *Adapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	if a.config.Market() == exchange.MarketTypeSpot {
		return nil, nil
	}
	data, err := a.request(ctx, http.MethodGet, "/api/v5/account/positions", url.Values{
		"instType": []string{"SWAP"},
	}, nil, true)
	if err != nil {
		return nil, err
	}
	var payload []positionPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, rejected("decode positions", err)
	}
	out := make([]exchange.Position, 0, len(payload))
	for _, row := range payload {
		if row.PosSide != "" && row.PosSide != "net" {
			return nil, rejected("hedge position mode is unsupported", nil)
		}
		contracts, err := decimalOrZero(row.Pos)
		if err != nil || contracts.IsZero() {
			continue
		}
		quantity, err := a.toBaseQuantity(row.InstID, contracts)
		if err != nil {
			return nil, err
		}
		position := exchange.Position{
			Market:           a.config.Market(),
			TradingAccountID: a.config.TradingAccountID,
			ExchangeSymbol:   row.InstID, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: millisString(row.UTime),
		}
		position.EntryPrice, err = decimalOrZero(row.AvgPx)
		if err == nil {
			position.MarkPrice, err = decimalOrZero(row.MarkPx)
		}
		if err == nil {
			position.Leverage, err = decimalOrZero(row.Lever)
		}
		if err == nil {
			usedMargin := row.IMR
			if usedMargin == "" {
				usedMargin = row.Margin
			}
			position.UsedMargin, err = decimalOrZero(usedMargin)
		}
		if err == nil {
			position.LiquidationPrice, err = decimalOrZero(row.LiqPx)
		}
		if err == nil {
			position.UnrealizedPnL, err = decimalOrZero(row.Upl)
		}
		if err == nil {
			position.RealizedPnL, err = decimalOrZero(row.Realized)
		}
		if err != nil {
			return nil, err
		}
		if row.MgnMode != "" && row.MgnMode != "cross" {
			return nil, rejected("isolated margin is unsupported", nil)
		}
		out = append(out, position)
	}
	return out, nil
}

type orderPayload struct {
	InstID     string `json:"instId"`
	OrdID      string `json:"ordId"`
	ClOrdID    string `json:"clOrdId"`
	Side       string `json:"side"`
	PosSide    string `json:"posSide"`
	OrdType    string `json:"ordType"`
	Sz         string `json:"sz"`
	AccFillSz  string `json:"accFillSz"`
	AvgPx      string `json:"avgPx"`
	Px         string `json:"px"`
	State      string `json:"state"`
	ReduceOnly string `json:"reduceOnly"`
	CTime      string `json:"cTime"`
	UTime      string `json:"uTime"`
	SCode      string `json:"sCode"`
	SMsg       string `json:"sMsg"`
}

// openOrdersPageSize 是 orders-pending 每页的上限。
const openOrdersPageSize = 100

func (a *Adapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	var all []orderPayload
	after := ""
	for {
		query := url.Values{
			"instType": []string{a.instrumentType()},
			"limit":    []string{strconv.Itoa(openOrdersPageSize)},
		}
		if after != "" {
			query.Set("after", after)
		}
		data, err := a.request(ctx, http.MethodGet, "/api/v5/trade/orders-pending", query, nil, true)
		if err != nil {
			return nil, err
		}
		var page []orderPayload
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, rejected("decode open orders", err)
		}
		all = append(all, page...)
		// 按订单号倒序分页，after 取本页最后一笔。
		if len(page) < openOrdersPageSize || page[len(page)-1].OrdID == "" || page[len(page)-1].OrdID == after {
			break
		}
		after = page[len(page)-1].OrdID
	}
	return a.orders(all)
}

func (a *Adapter) GetOrder(
	ctx context.Context,
	key exchange.SymbolKey,
	clientOrderID string,
) (exchange.Order, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.Order{}, err
	}
	symbolValue := key.Symbol
	data, err := a.request(ctx, http.MethodGet, "/api/v5/trade/order", url.Values{
		"instId":  []string{symbolValue},
		"clOrdId": []string{clientOrderID},
	}, nil, true)
	if err != nil {
		return exchange.Order{}, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return exchange.Order{}, &exchange.Error{
			Kind: exchange.ErrorOrderNotFound, Err: errors.New("OKX order not found"),
		}
	}
	return a.order(payload[0])
}

func (a *Adapter) GetOrderByExchangeID(
	ctx context.Context,
	key exchange.SymbolKey,
	exchangeOrderID string,
) (exchange.Order, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.Order{}, err
	}
	symbol := key.Symbol
	data, err := a.request(ctx, http.MethodGet, "/api/v5/trade/order", url.Values{
		"instId": []string{symbol},
		"ordId":  []string{exchangeOrderID},
	}, nil, true)
	if err != nil {
		return exchange.Order{}, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return exchange.Order{}, &exchange.Error{
			Kind: exchange.ErrorOrderNotFound, Err: errors.New("OKX order not found"),
		}
	}
	return a.order(payload[0])
}

func (a *Adapter) PlaceOrder(
	ctx context.Context,
	request exchange.OrderRequest,
) (exchange.Order, error) {
	if err := validateRequest(request, a.config.Market()); err != nil {
		return exchange.Order{}, err
	}
	quantity, err := a.toExchangeQuantity(request.ExchangeSymbol, request.Quantity)
	if err != nil {
		return exchange.Order{}, err
	}
	body := map[string]string{
		"instId": request.ExchangeSymbol, "clOrdId": request.ClientOrderID,
		"tdMode": "cash", "side": strings.ToLower(string(request.Side)),
		"ordType": strings.ToLower(string(request.OrderType)),
		"sz":      quantity.String(),
	}
	if a.config.Market() == exchange.MarketTypeSpot &&
		request.OrderType == exchange.OrderTypeMarket &&
		request.Side == exchange.SideBuy {
		// Domain MARKET quantities are always base-asset quantities. OKX
		// otherwise interprets a SPOT MARKET buy sz as quote currency.
		body["tgtCcy"] = "base_ccy"
	}
	if request.OrderType == exchange.OrderTypeLimit {
		body["px"] = request.LimitPrice.String()
		body["ordType"] = mapOKXLimitType(request.EffectiveFillPolicy())
	}
	if a.config.Market() == exchange.MarketTypeSwap {
		body["tdMode"] = "cross"
		body["posSide"] = "net"
		if request.ReduceOnly {
			body["reduceOnly"] = "true"
		}
	}
	data, err := a.send(ctx, http.MethodPost, "/api/v5/trade/order", nil, body, true, true)
	if err != nil {
		return exchange.Order{}, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return exchange.Order{}, transportUnknown("decode placed order", err)
	}
	if payload[0].SCode != "" && payload[0].SCode != "0" {
		return exchange.Order{}, a.rateLimited(classifyOKXCode(payload[0].SCode, payload[0].SMsg), true)
	}
	return exchange.Order{
		Market:          a.config.Market(),
		ExchangeOrderID: payload[0].OrdID,
		ClientOrderID:   request.ClientOrderID,
		ExchangeSymbol:  request.ExchangeSymbol,
		OrderType:       request.OrderType,
		TimeInForce:     request.NativeTimeInForce(),
		Side:            request.Side,
		PositionSide:    request.PositionSide,
		Quantity:        request.Quantity,
		ReduceOnly:      request.ReduceOnly,
		Status:          exchange.OrderStatusOpen,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}, nil
}

func (a *Adapter) CancelOrder(
	ctx context.Context,
	key exchange.SymbolKey,
	clientOrderID string,
) (exchange.Order, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.Order{}, err
	}
	symbolValue := key.Symbol
	data, err := a.request(ctx, http.MethodPost, "/api/v5/trade/cancel-order", nil, map[string]string{
		"instId": symbolValue, "clOrdId": clientOrderID,
	}, true)
	if err != nil {
		return exchange.Order{}, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return exchange.Order{}, transportUnknown("decode canceled order", err)
	}
	if payload[0].SCode != "" && payload[0].SCode != "0" {
		return exchange.Order{}, a.rateLimited(classifyOKXCode(payload[0].SCode, payload[0].SMsg), false)
	}
	// 回执只说明撤单已受理；最终状态和可能抢先发生的成交由调用方撤单后的同步拿到，这里不再追查订单：
	// 追查失败会被当成撤单没发出而重发。
	return exchange.Order{
		Market:          a.config.Market(),
		ExchangeOrderID: payload[0].OrdID, ClientOrderID: clientOrderID, ExchangeSymbol: symbolValue,
	}, nil
}

type fillPayload struct {
	BillID   string `json:"billId"`
	InstID   string `json:"instId"`
	TradeID  string `json:"tradeId"`
	OrdID    string `json:"ordId"`
	ClOrdID  string `json:"clOrdId"`
	Side     string `json:"side"`
	PosSide  string `json:"posSide"`
	FillSz   string `json:"fillSz"`
	FillPx   string `json:"fillPx"`
	Fee      string `json:"fee"`
	FeeCcy   string `json:"feeCcy"`
	FillPnl  string `json:"fillPnl"`
	ExecType string `json:"execType"`
	Ts       string `json:"ts"`
	FillTime string `json:"fillTime"`
}

func (a *Adapter) ListRecentFills(
	ctx context.Context,
	key exchange.SymbolKey,
	cursor string,
) ([]exchange.Fill, string, error) {
	if err := a.ownMarket(key); err != nil {
		return nil, "", err
	}
	symbolValue := key.Symbol
	if strings.TrimSpace(symbolValue) == "" {
		return nil, cursor, rejected("symbol is required to list OKX fills", nil)
	}
	const pageSize = 100
	payload, err := a.fillPage(ctx, symbolValue, "before", cursor, pageSize)
	if err != nil {
		return nil, cursor, err
	}
	next := cursor
	if len(payload) != 0 && payload[0].BillID != "" {
		next = payload[0].BillID
	}
	rows := append([]fillPayload(nil), payload...)
	for len(payload) == pageSize {
		oldest := payload[len(payload)-1].BillID
		if oldest == "" ||
			(cursor != "" && compareBillID(oldest, cursor) <= 0) {
			break
		}
		nextPage, pageErr := a.fillPage(ctx, symbolValue, "after", oldest, pageSize)
		if pageErr != nil {
			return nil, cursor, pageErr
		}
		if len(nextPage) > 0 &&
			nextPage[len(nextPage)-1].BillID == oldest {
			return nil, cursor, rejected("OKX Fill page did not advance", nil)
		}
		payload = nextPage
		rows = append(rows, payload...)
	}
	// Pagination follows bill IDs, but position reducers need execution order.
	// Record generation may lag execution, so reversing pages is insufficient.
	type billedFill struct {
		fill   exchange.Fill
		billID string
	}
	normalized := make([]billedFill, 0, len(rows))
	for index := len(rows) - 1; index >= 0; index-- {
		row := rows[index]
		if cursor != "" && compareBillID(row.BillID, cursor) <= 0 {
			continue
		}
		fill, err := a.fill(row)
		if err != nil {
			return nil, cursor, err
		}
		normalized = append(normalized, billedFill{fill: fill, billID: row.BillID})
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].fill.TradedAt.Equal(normalized[j].fill.TradedAt) {
			return compareBillID(normalized[i].billID, normalized[j].billID) < 0
		}
		return normalized[i].fill.TradedAt.Before(normalized[j].fill.TradedAt)
	})
	out := make([]exchange.Fill, len(normalized))
	for i, row := range normalized {
		out[i] = row.fill
	}
	return out, next, nil
}

func (a *Adapter) fillPage(
	ctx context.Context,
	symbol string,
	direction string,
	cursor string,
	limit int,
) ([]fillPayload, error) {
	query := url.Values{
		"instType": []string{a.instrumentType()},
		"instId":   []string{symbol},
		"limit":    []string{strconv.Itoa(limit)},
	}
	if cursor != "" {
		query.Set(direction, cursor)
	}
	data, err := a.request(ctx, http.MethodGet, "/api/v5/trade/fills-history", query, nil, true)
	if err != nil {
		return nil, err
	}
	var payload []fillPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, rejected("decode fills", err)
	}
	return payload, nil
}

func compareBillID(left string, right string) int {
	leftID, leftOK := new(big.Int).SetString(left, 10)
	rightID, rightOK := new(big.Int).SetString(right, 10)
	if !leftOK || !rightOK {
		return strings.Compare(left, right)
	}
	return leftID.Cmp(rightID)
}

func (a *Adapter) SetLeverage(
	ctx context.Context,
	key exchange.SymbolKey,
	leverage shared.Decimal,
) error {
	if err := a.ownMarket(key); err != nil {
		return err
	}
	symbolValue := key.Symbol
	if a.config.Market() != exchange.MarketTypeSwap ||
		strings.TrimSpace(symbolValue) == "" ||
		leverage.Cmp(shared.Zero()) <= 0 ||
		strings.ContainsAny(leverage.String(), "./") {
		return rejected("positive integer SWAP leverage is required", nil)
	}
	_, err := a.request(ctx, http.MethodPost, "/api/v5/account/set-leverage", nil, map[string]string{
		"instId": symbolValue, "lever": leverage.String(), "mgnMode": "cross", "posSide": "net",
	}, true)
	return err
}

func (a *Adapter) SetMarginMode(
	_ context.Context,
	_ shared.ExchangeSymbol,
	mode exchange.MarginMode,
) error {
	if a.config.Market() != exchange.MarketTypeSwap || mode != exchange.MarginModeCross {
		return rejected("only CROSS SWAP margin mode is supported", nil)
	}
	// OKX applies tdMode=cross per order and leverage setting; there is no
	// separate account-wide margin-mode mutation for this v1 scope.
	return nil
}

func (a *Adapter) orders(payload []orderPayload) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(payload))
	for _, row := range payload {
		order, err := a.order(row)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, nil
}

func (a *Adapter) order(row orderPayload) (exchange.Order, error) {
	contracts, err := decimalOrZero(row.Sz)
	if err != nil {
		return exchange.Order{}, err
	}
	quantity, err := a.toBaseQuantity(row.InstID, contracts)
	if err != nil {
		return exchange.Order{}, err
	}
	filledContracts, err := decimalOrZero(row.AccFillSz)
	if err != nil {
		return exchange.Order{}, err
	}
	filled, err := a.toBaseQuantity(row.InstID, filledContracts)
	if err != nil {
		return exchange.Order{}, err
	}
	average, err := decimalOrZero(row.AvgPx)
	if err != nil {
		return exchange.Order{}, err
	}
	price, err := decimalOrZero(row.Px)
	if err != nil {
		return exchange.Order{}, err
	}
	positionSide := exchange.PositionSideUnspecified
	if a.config.Market() == exchange.MarketTypeSwap {
		if row.PosSide != "" && row.PosSide != "net" {
			return exchange.Order{}, rejected("hedge order is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	orderType, tif := parseOKXOrderType(row.OrdType)
	var limitPrice *shared.Decimal
	if orderType == exchange.OrderTypeLimit && price.Cmp(shared.Zero()) > 0 {
		limitPrice = &price
	}
	return exchange.Order{
		Market:          a.config.Market(),
		ExchangeOrderID: row.OrdID, ClientOrderID: row.ClOrdID,
		ExchangeSymbol: row.InstID, OrderType: orderType, TimeInForce: tif,
		Side: exchange.Side(strings.ToUpper(row.Side)), PositionSide: positionSide,
		Quantity: quantity, LimitPrice: limitPrice,
		FilledQuantity: filled, AveragePrice: average,
		ReduceOnly: row.ReduceOnly == "true",
		Status:     mapStatus(row.State, filled),
		CreatedAt:  millisString(row.CTime), UpdatedAt: millisString(row.UTime),
	}, nil
}

func (a *Adapter) fill(row fillPayload) (exchange.Fill, error) {
	fillMillis, err := strconv.ParseInt(row.FillTime, 10, 64)
	if err != nil || fillMillis <= 0 {
		return exchange.Fill{}, rejected("fill execution time is required", err)
	}
	// An incomplete fee must be recovered from REST, not guessed in an immutable fill.
	if strings.TrimSpace(row.Fee) == "" || strings.TrimSpace(row.FeeCcy) == "" {
		return exchange.Fill{}, rejected("fill fee amount and currency are required", nil)
	}
	contracts, err := decimalOrZero(row.FillSz)
	if err != nil {
		return exchange.Fill{}, err
	}
	quantity, err := a.toBaseQuantity(row.InstID, contracts)
	if err != nil {
		return exchange.Fill{}, err
	}
	price, err := decimalOrZero(row.FillPx)
	if err != nil {
		return exchange.Fill{}, err
	}
	fee, err := decimalOrZero(row.Fee)
	if err != nil {
		return exchange.Fill{}, err
	}
	pnl, err := decimalOrZero(row.FillPnl)
	if err != nil {
		return exchange.Fill{}, err
	}
	positionSide := exchange.PositionSideUnspecified
	if a.config.Market() == exchange.MarketTypeSwap {
		if row.PosSide != "" && row.PosSide != "net" {
			return exchange.Fill{}, rejected("hedge fill is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	return exchange.Fill{
		Market:          a.config.Market(),
		ExchangeTradeID: row.TradeID, ExchangeOrderID: row.OrdID,
		ClientOrderID: row.ClOrdID, ExchangeSymbol: row.InstID,
		Side: exchange.Side(strings.ToUpper(row.Side)), PositionSide: positionSide,
		Quantity: quantity, Price: price, Fee: fee.Neg(), FeeAsset: row.FeeCcy,
		RealizedPnL: pnl, SettlementAsset: a.config.SettlementAsset,
		LiquidityRole: liquidityRole(row.ExecType), TradedAt: time.UnixMilli(fillMillis).UTC(),
	}, nil
}

func validateRequest(request exchange.OrderRequest, market exchange.MarketType) error {
	if strings.TrimSpace(request.ClientOrderID) == "" ||
		strings.TrimSpace(request.ExchangeSymbol) == "" ||
		!request.Side.Valid() ||
		request.Quantity.Cmp(shared.Zero()) <= 0 {
		return rejected("invalid order request", nil)
	}
	if !ValidClientOrderID(request.ClientOrderID) {
		return rejected("client order ID must be 1-32 ASCII alphanumeric characters", nil)
	}
	switch request.OrderType {
	case exchange.OrderTypeMarket:
		if request.LimitPrice != nil ||
			request.EffectiveFillPolicy() != exchange.FillPolicyUnspecified {
			return rejected("MARKET order cannot carry price or TimeInForce", nil)
		}
	case exchange.OrderTypeLimit:
		if request.LimitPrice == nil || request.LimitPrice.Cmp(shared.Zero()) <= 0 ||
			!request.EffectiveFillPolicy().ValidForLimit() {
			return rejected("invalid LIMIT order", nil)
		}
	default:
		return rejected("unsupported order type", nil)
	}
	if market == exchange.MarketTypeSpot {
		if request.PositionSide != exchange.PositionSideUnspecified || request.ReduceOnly {
			return rejected("SPOT cannot use position side or reduce-only", nil)
		}
	} else if request.PositionSide != exchange.PositionSideNet {
		return rejected("SWAP requires NET position side", nil)
	}
	return nil
}

// ValidClientOrderID exposes the clOrdId boundary contract to order producers.
func ValidClientOrderID(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') &&
			(char < 'A' || char > 'Z') &&
			(char < 'a' || char > 'z') {
			return false
		}
	}
	return true
}

func mapOKXLimitType(policy exchange.FillPolicy) string {
	switch policy {
	case exchange.FillPolicyIOC:
		return "ioc"
	case exchange.FillPolicyFOK:
		return "fok"
	default:
		return "limit"
	}
}

func parseOKXOrderType(raw string) (exchange.OrderType, exchange.TimeInForce) {
	switch raw {
	case "market":
		return exchange.OrderTypeMarket, exchange.TimeInForceUnspecified
	case "ioc":
		return exchange.OrderTypeLimit, exchange.TimeInForceIOC
	case "fok":
		return exchange.OrderTypeLimit, exchange.TimeInForceFOK
	default:
		return exchange.OrderTypeLimit, exchange.TimeInForceGTC
	}
}

func mapStatus(raw string, filled shared.Decimal) exchange.OrderStatus {
	switch raw {
	case "live":
		return exchange.OrderStatusOpen
	case "partially_filled":
		return exchange.OrderStatusPartiallyFilled
	case "filled":
		return exchange.OrderStatusFilled
	case "canceled":
		if filled.Cmp(shared.Zero()) > 0 {
			return exchange.OrderStatusPartiallyCanceled
		}
		return exchange.OrderStatusCanceled
	case "mmp_canceled":
		return exchange.OrderStatusCanceled
	default:
		return exchange.OrderStatusSubmitUnknown
	}
}

func classify(err error, _ []byte) error {
	if err == nil {
		return nil
	}
	var typed *exchange.Error
	if errors.As(err, &typed) {
		return typed
	}
	return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
}

func classifyOKXCode(code, message string) error {
	kind := exchange.ErrorRejected
	switch code {
	case "50011", "50040", "50061":
		kind = exchange.ErrorOrderRateLimited
	// 鉴权类：API 冻结、环境不符、请求头缺失或错误、签名错误（50014 是“参数不能为空”，不是鉴权）。
	// 50110（IP 不在密钥的白名单里）未在官方文档里逐字核对，按鉴权类处理。
	case "50100", "50101", "50103", "50104", "50105", "50106", "50110", "50111", "50112", "50113", "50114":
		kind = exchange.ErrorAuthentication
	case "51008":
		kind = exchange.ErrorInsufficientBalance
	case "51400", "51401", "51603":
		kind = exchange.ErrorOrderNotFound
	// 结果未知，走查询恢复：服务暂不可用、超时（50004 官方说明不代表成功或失败）、系统繁忙/错误、下单超时，
	// 以及时间戳过期（交易所没有受理，但本地无法确认，与币安 -1021 一致）。50000 是请求体为空，属于拒单。
	case "50001", "50004", "50013", "50026", "51149", "50102":
		kind = exchange.ErrorTransportUnknown
	}
	return &exchange.Error{Kind: kind, Code: code, Err: errors.New(message)}
}

func rejected(message string, err error) error {
	if err == nil {
		err = errors.New(message)
	} else {
		err = fmt.Errorf("%s: %w", message, err)
	}
	return &exchange.Error{Kind: exchange.ErrorRejected, Err: err}
}

func transportUnknown(message string, err error) error {
	if err == nil {
		err = errors.New(message)
	} else {
		err = fmt.Errorf("%s: %w", message, err)
	}
	return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
}

func liquidityRole(value string) string {
	switch strings.ToUpper(value) {
	case "M", "MAKER":
		return "MAKER"
	case "T", "TAKER":
		return "TAKER"
	default:
		return ""
	}
}

func decimal(raw string) (shared.Decimal, error) {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Decimal{}, rejected("invalid OKX decimal", err)
	}
	return value, nil
}

func decimalOrZero(raw string) (shared.Decimal, error) {
	if raw == "" {
		return shared.Zero(), nil
	}
	return decimal(raw)
}

func millisString(raw string) time.Time {
	value, _ := strconv.ParseInt(raw, 10, 64)
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
