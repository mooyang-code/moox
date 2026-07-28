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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

const defaultBaseURL = "https://www.okx.com"

type Adapter struct {
	config     exchange.AccountConfig
	credential exchange.Credential
	client     *httpclient.Client

	mu          sync.RWMutex
	instruments map[string]exchange.Instrument

	privateGateMu sync.Mutex
	privateGate   chan struct{}
}

func init() {
	exchange.Register(exchange.ExchangeOKX, func(
		config exchange.AccountConfig,
		credential exchange.Credential,
	) (exchange.Adapter, error) {
		if config.ExecutionMode == exchange.ExecutionModeLive &&
			strings.TrimSpace(credential.Passphrase) == "" {
			return nil, rejected("OKX live account requires passphrase", nil)
		}
		return New(config, credential), nil
	})
}

func New(config exchange.AccountConfig, credential exchange.Credential) *Adapter {
	return &Adapter{
		config: config, credential: credential,
		client:      httpclient.New(defaultBaseURL),
		instruments: make(map[string]exchange.Instrument),
	}
}

func NewWithClient(
	config exchange.AccountConfig,
	credential exchange.Credential,
	client *httpclient.Client,
) *Adapter {
	adapter := New(config, credential)
	if client != nil {
		adapter.client = client
	}
	return adapter
}

func (a *Adapter) Exchange() exchange.Exchange { return exchange.ExchangeOKX }

func (a *Adapter) MarkPrivateStreamMetadataReady() {
	a.privateGateMu.Lock()
	gate := a.privateGate
	if gate != nil {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
	a.privateGateMu.Unlock()
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
	raw, err := a.client.Do(ctx, &httpclient.Request{
		Method: method, Path: fullPath, Body: rawBody, Headers: headers,
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
			return envelope.Data, classifyOKXCode(details[0].Code, details[0].Message)
		}
		return envelope.Data, classifyOKXCode(envelope.Code, envelope.Msg)
	}
	return envelope.Data, nil
}

func signature(secret, timestamp, method, path, body string) string {
	payload := timestamp + strings.ToUpper(method) + path + body
	return base64.StdEncoding.EncodeToString(hmacSha256([]byte(secret), []byte(payload)))
}

func (a *Adapter) instrumentType() string {
	if a.config.MarketType == exchange.MarketTypeSwap {
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
		instrument := exchange.Instrument{
			Exchange: exchange.ExchangeOKX, MarketType: a.config.MarketType,
			Symbol: row.InstID, InstrumentID: row.InstID,
			BaseAsset: row.BaseCcy, QuoteAsset: row.QuoteCcy,
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
		if a.config.MarketType == exchange.MarketTypeSwap {
			if row.CtType != "linear" || row.SettleCcy != "USDT" ||
				row.CtVal == "" || row.CtValCcy == "" {
				continue
			}
			instrument.BaseAsset = row.CtValCcy
			instrument.QuoteAsset = row.SettleCcy
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
	a.instruments = make(map[string]exchange.Instrument, len(out))
	for _, instrument := range out {
		a.instruments[instrument.Symbol] = instrument
	}
	a.mu.Unlock()
	return out, nil
}

func (a *Adapter) instrument(symbol string) (exchange.Instrument, error) {
	a.mu.RLock()
	instrument, ok := a.instruments[symbol]
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
	if a.config.MarketType == exchange.MarketTypeSpot {
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
	if a.config.MarketType == exchange.MarketTypeSpot {
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
	AdjEq   string `json:"adjEq"`
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
	if a.config.MarketType == exchange.MarketTypeSwap {
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
	row := payload[0]
	snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millisString(row.UTime)}
	snapshot.Equity, err = decimalOrZero(row.TotalEq)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	for _, detail := range row.Details {
		totalRaw := detail.Eq
		if totalRaw == "" {
			totalRaw = detail.CashBal
		}
		total, parseErr := decimalOrZero(totalRaw)
		if parseErr != nil {
			return exchange.AccountSnapshot{}, parseErr
		}
		availableRaw := detail.AvailEq
		if availableRaw == "" {
			availableRaw = detail.AvailBal
		}
		available, parseErr := decimalOrZero(availableRaw)
		if parseErr != nil {
			return exchange.AccountSnapshot{}, parseErr
		}
		locked, parseErr := decimalOrZero(detail.FrozenBal)
		if parseErr != nil {
			return exchange.AccountSnapshot{}, parseErr
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Asset: detail.Ccy, Available: available, Locked: locked, Total: total,
		})
		if strings.EqualFold(detail.Ccy, a.config.SettlementAsset) {
			snapshot.AvailableFunds = available
			snapshot.UsedMargin, parseErr = decimalOrZero(detail.Imr)
			if parseErr == nil {
				snapshot.MaintenanceMargin, parseErr = decimalOrZero(detail.Mmr)
			}
			if parseErr == nil {
				snapshot.UnrealizedPnL, parseErr = decimalOrZero(detail.Upl)
			}
			if parseErr != nil {
				return exchange.AccountSnapshot{}, parseErr
			}
		}
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
	if a.config.MarketType == exchange.MarketTypeSpot {
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
			ExchangeAccountID: a.config.ExchangeAccountID,
			Symbol:            row.InstID, PositionSide: exchange.PositionSideNet,
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

func (a *Adapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	data, err := a.request(ctx, http.MethodGet, "/api/v5/trade/orders-pending", url.Values{
		"instType": []string{a.instrumentType()},
	}, nil, true)
	if err != nil {
		return nil, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, rejected("decode open orders", err)
	}
	return a.orders(payload)
}

func (a *Adapter) GetOrder(
	ctx context.Context,
	symbol string,
	clientOrderID string,
) (exchange.Order, error) {
	data, err := a.request(ctx, http.MethodGet, "/api/v5/trade/order", url.Values{
		"instId":  []string{symbol},
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
	symbol string,
	exchangeOrderID string,
) (exchange.Order, error) {
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
	if err := validateRequest(request, a.config.MarketType); err != nil {
		return exchange.Order{}, err
	}
	quantity, err := a.toExchangeQuantity(request.Symbol, request.Quantity)
	if err != nil {
		return exchange.Order{}, err
	}
	body := map[string]string{
		"instId": request.Symbol, "clOrdId": request.ClientOrderID,
		"tdMode": "cash", "side": strings.ToLower(string(request.Side)),
		"ordType": strings.ToLower(string(request.OrderType)),
		"sz":      quantity.String(),
	}
	if a.config.MarketType == exchange.MarketTypeSpot &&
		request.OrderType == exchange.OrderTypeMarket &&
		request.Side == exchange.SideBuy {
		// Domain MARKET quantities are always base-asset quantities. OKX
		// otherwise interprets a SPOT MARKET buy sz as quote currency.
		body["tgtCcy"] = "base_ccy"
	}
	if request.OrderType == exchange.OrderTypeLimit {
		body["px"] = request.LimitPrice.String()
		body["ordType"] = mapOKXLimitType(request.TimeInForce)
	}
	if a.config.MarketType == exchange.MarketTypeSwap {
		body["tdMode"] = "cross"
		body["posSide"] = "net"
		if request.ReduceOnly {
			body["reduceOnly"] = "true"
		}
	}
	data, err := a.request(ctx, http.MethodPost, "/api/v5/trade/order", nil, body, true)
	if err != nil {
		return exchange.Order{}, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return exchange.Order{}, transportUnknown("decode placed order", err)
	}
	if payload[0].SCode != "" && payload[0].SCode != "0" {
		return exchange.Order{}, classifyOKXCode(payload[0].SCode, payload[0].SMsg)
	}
	return exchange.Order{
		ExchangeOrderID: payload[0].OrdID,
		ClientOrderID:   request.ClientOrderID,
		Symbol:          request.Symbol,
		OrderType:       request.OrderType,
		TimeInForce:     request.TimeInForce,
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
	symbol string,
	clientOrderID string,
) (exchange.Order, error) {
	data, err := a.request(ctx, http.MethodPost, "/api/v5/trade/cancel-order", nil, map[string]string{
		"instId": symbol, "clOrdId": clientOrderID,
	}, true)
	if err != nil {
		return exchange.Order{}, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return exchange.Order{}, transportUnknown("decode canceled order", err)
	}
	if payload[0].SCode != "" && payload[0].SCode != "0" {
		return exchange.Order{}, classifyOKXCode(payload[0].SCode, payload[0].SMsg)
	}
	// Fetch the authoritative terminal state; a cancel acknowledgement alone is
	// not sufficient because final fills may have raced with the request.
	return a.GetOrder(ctx, symbol, clientOrderID)
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
}

func (a *Adapter) ListRecentFills(
	ctx context.Context,
	symbol string,
	cursor string,
) ([]exchange.Fill, string, error) {
	if strings.TrimSpace(symbol) == "" {
		return nil, cursor, rejected("symbol is required to list OKX fills", nil)
	}
	const pageSize = 100
	payload, err := a.fillPage(ctx, symbol, "before", cursor, pageSize)
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
		nextPage, pageErr := a.fillPage(ctx, symbol, "after", oldest, pageSize)
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
	// OKX returns newest first. Apply only rows newer than the stored cursor,
	// oldest first, so a large synchronization gap cannot skip intermediate
	// Fills and downstream reducers observe chronological facts.
	out := make([]exchange.Fill, 0, len(rows))
	for index := len(rows) - 1; index >= 0; index-- {
		row := rows[index]
		if cursor != "" && compareBillID(row.BillID, cursor) <= 0 {
			continue
		}
		fill, err := a.fill(row)
		if err != nil {
			return nil, cursor, err
		}
		out = append(out, fill)
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
	symbol string,
	leverage shared.Decimal,
) error {
	if a.config.MarketType != exchange.MarketTypeSwap ||
		strings.TrimSpace(symbol) == "" ||
		leverage.Cmp(shared.Zero()) <= 0 ||
		strings.ContainsAny(leverage.String(), "./") {
		return rejected("positive integer SWAP leverage is required", nil)
	}
	_, err := a.request(ctx, http.MethodPost, "/api/v5/account/set-leverage", nil, map[string]string{
		"instId": symbol, "lever": leverage.String(), "mgnMode": "cross", "posSide": "net",
	}, true)
	return err
}

func (a *Adapter) SetMarginMode(
	_ context.Context,
	_ string,
	mode exchange.MarginMode,
) error {
	if a.config.MarketType != exchange.MarketTypeSwap || mode != exchange.MarginModeCross {
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
	if a.config.MarketType == exchange.MarketTypeSwap {
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
		ExchangeOrderID: row.OrdID, ClientOrderID: row.ClOrdID,
		Symbol: row.InstID, OrderType: orderType, TimeInForce: tif,
		Side: exchange.Side(strings.ToUpper(row.Side)), PositionSide: positionSide,
		Quantity: quantity, LimitPrice: limitPrice,
		FilledQuantity: filled, AveragePrice: average,
		ReduceOnly: row.ReduceOnly == "true",
		Status:     mapStatus(row.State, filled),
		CreatedAt:  millisString(row.CTime), UpdatedAt: millisString(row.UTime),
	}, nil
}

func (a *Adapter) fill(row fillPayload) (exchange.Fill, error) {
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
	if a.config.MarketType == exchange.MarketTypeSwap {
		if row.PosSide != "" && row.PosSide != "net" {
			return exchange.Fill{}, rejected("hedge fill is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	return exchange.Fill{
		ExchangeTradeID: row.TradeID, ExchangeOrderID: row.OrdID,
		ClientOrderID: row.ClOrdID, Symbol: row.InstID,
		Side: exchange.Side(strings.ToUpper(row.Side)), PositionSide: positionSide,
		Quantity: quantity, Price: price, Fee: fee.Abs(), FeeAsset: row.FeeCcy,
		RealizedPnL: pnl, SettlementAsset: a.config.SettlementAsset,
		LiquidityRole: liquidityRole(row.ExecType), TradedAt: millisString(row.Ts),
	}, nil
}

func validateRequest(request exchange.OrderRequest, market exchange.MarketType) error {
	if strings.TrimSpace(request.ClientOrderID) == "" ||
		strings.TrimSpace(request.Symbol) == "" ||
		!request.Side.Valid() ||
		request.Quantity.Cmp(shared.Zero()) <= 0 {
		return rejected("invalid order request", nil)
	}
	switch request.OrderType {
	case exchange.OrderTypeMarket:
		if request.LimitPrice != nil || request.TimeInForce != exchange.TimeInForceUnspecified {
			return rejected("MARKET order cannot carry price or TimeInForce", nil)
		}
	case exchange.OrderTypeLimit:
		if request.LimitPrice == nil || request.LimitPrice.Cmp(shared.Zero()) <= 0 ||
			!request.TimeInForce.ValidForLimit() {
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

func mapOKXLimitType(tif exchange.TimeInForce) string {
	switch tif {
	case exchange.TimeInForceIOC:
		return "ioc"
	case exchange.TimeInForceFOK:
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
	case "50011", "50040":
		kind = exchange.ErrorRateLimited
	case "50014", "50101", "50113":
		kind = exchange.ErrorAuthentication
	case "51008":
		kind = exchange.ErrorInsufficientBalance
	case "51400", "51401", "51603":
		kind = exchange.ErrorOrderNotFound
	case "50000", "50004", "50026":
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
