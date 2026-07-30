package binance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

const (
	productionSpotURL = "https://api.binance.com"
	testnetSpotURL    = "https://testnet.binance.vision"
	productionSwapURL = "https://fapi.binance.com"
	testnetSwapURL    = "https://demo-fapi.binance.com"
	recvWindow        = 5000
)

type Adapter struct {
	config     exchange.AccountConfig
	credential exchange.Credential
	spot       *httpclient.Client
	swap       *httpclient.Client

	mu          sync.RWMutex
	instruments map[string]exchange.Instrument
}

func New(config exchange.AccountConfig, credential exchange.Credential) *Adapter {
	spotURL, swapURL := restEndpoints(config.Environment)
	return &Adapter{
		config:      config,
		credential:  credential,
		spot:        httpclient.New(spotURL),
		swap:        httpclient.New(swapURL),
		instruments: make(map[string]exchange.Instrument),
	}
}

func restEndpoints(
	environment exchange.AccountEnvironment,
) (spot string, swap string) {
	if environment == exchange.AccountEnvironmentTestnet {
		return testnetSpotURL, testnetSwapURL
	}
	return productionSpotURL, productionSwapURL
}

func privateStreamEndpoint(config exchange.AccountConfig) string {
	if config.MarketType == exchange.MarketTypeSpot {
		if config.Environment == exchange.AccountEnvironmentTestnet {
			return "wss://ws-api.testnet.binance.vision/ws-api/v3"
		}
		return "wss://ws-api.binance.com:443/ws-api/v3"
	}
	if config.Environment == exchange.AccountEnvironmentTestnet {
		return "wss://demo-fstream.binance.com/ws/"
	}
	return "wss://fstream.binance.com/ws/"
}

func (a *Adapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }

func (a *Adapter) client() *httpclient.Client {
	if a.config.MarketType == exchange.MarketTypeSwap {
		return a.swap
	}
	return a.spot
}

func (a *Adapter) path(spot, swap string) string {
	if a.config.MarketType == exchange.MarketTypeSwap {
		return swap
	}
	return spot
}

func (a *Adapter) signedQuery(values url.Values) url.Values {
	query := cloneValues(values)
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.Itoa(recvWindow))
	query.Set("signature", hex.EncodeToString(hmacSha256(
		[]byte(a.credential.APISecret),
		[]byte(query.Encode()),
	)))
	return query
}

func (a *Adapter) request(
	ctx context.Context,
	method string,
	path string,
	values url.Values,
) ([]byte, error) {
	return a.client().Do(ctx, &httpclient.Request{
		Method: method,
		Path:   path,
		Query:  a.signedQuery(values),
		Headers: map[string]string{
			"X-MBX-APIKEY": a.credential.APIKey,
		},
	})
}

type exchangeInfo struct {
	Symbols []struct {
		Symbol       string `json:"symbol"`
		Pair         string `json:"pair"`
		ContractType string `json:"contractType"`
		Status       string `json:"status"`
		BaseAsset    string `json:"baseAsset"`
		QuoteAsset   string `json:"quoteAsset"`
		MarginAsset  string `json:"marginAsset"`
		Filters      []struct {
			FilterType  string `json:"filterType"`
			TickSize    string `json:"tickSize"`
			StepSize    string `json:"stepSize"`
			MinQty      string `json:"minQty"`
			Notional    string `json:"notional"`
			MinNotional string `json:"minNotional"`
		} `json:"filters"`
	} `json:"symbols"`
}

func (a *Adapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	path := a.path("/api/v3/exchangeInfo", "/fapi/v1/exchangeInfo")
	raw, err := a.client().Do(ctx, &httpclient.Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return nil, err
	}
	var payload exchangeInfo
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, typedRejected("decode exchange info", err)
	}
	out := make([]exchange.Instrument, 0, len(payload.Symbols))
	for _, item := range payload.Symbols {
		if a.config.MarketType == exchange.MarketTypeSwap {
			if item.ContractType != "PERPETUAL" || item.MarginAsset != "USDT" {
				continue
			}
		}
		instrumentID, idErr := exchange.CanonicalInstrumentID(
			item.BaseAsset,
			item.QuoteAsset,
			a.config.MarketType,
		)
		if idErr != nil {
			return nil, typedRejected("canonical instrument ID", idErr)
		}
		instrument := exchange.Instrument{
			Exchange:           exchange.ExchangeBinance,
			MarketType:         a.config.MarketType,
			Symbol:             item.Symbol,
			InstrumentID:       instrumentID,
			BaseAsset:          item.BaseAsset,
			QuoteAsset:         item.QuoteAsset,
			SettlementAsset:    item.QuoteAsset,
			Linear:             a.config.MarketType == exchange.MarketTypeSwap,
			ContractValue:      shared.MustDecimal("1"),
			ContractValueAsset: item.BaseAsset,
			Status:             item.Status,
			ExchangeUpdatedAt:  time.Now().UTC(),
		}
		if a.config.MarketType == exchange.MarketTypeSwap {
			instrument.SettlementAsset = item.MarginAsset
		}
		for _, filter := range item.Filters {
			switch filter.FilterType {
			case "PRICE_FILTER":
				instrument.PriceTick, err = parseOptionalDecimal(filter.TickSize)
			case "LOT_SIZE", "MARKET_LOT_SIZE":
				// LOT_SIZE wins when both filters are present.
				if instrument.ExchangeQuantityStep.IsZero() || filter.FilterType == "LOT_SIZE" {
					instrument.ExchangeQuantityStep, err = parseOptionalDecimal(filter.StepSize)
					if err == nil {
						instrument.MinExchangeQuantity, err = parseOptionalDecimal(filter.MinQty)
					}
				}
			case "NOTIONAL", "MIN_NOTIONAL":
				rawNotional := filter.Notional
				if rawNotional == "" {
					rawNotional = filter.MinNotional
				}
				instrument.MinNotional, err = parseOptionalDecimal(rawNotional)
			}
			if err != nil {
				return nil, typedRejected("decode instrument decimal", err)
			}
		}
		if instrument.ExchangeQuantityStep.Cmp(shared.Zero()) <= 0 ||
			instrument.MinExchangeQuantity.Cmp(shared.Zero()) <= 0 ||
			instrument.PriceTick.Cmp(shared.Zero()) <= 0 {
			return nil, typedRejected("incomplete instrument filters", nil)
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

func (a *Adapter) GetReferencePrice(
	ctx context.Context,
	symbol string,
) (exchange.ReferencePrice, error) {
	raw, err := a.client().Do(ctx, &httpclient.Request{
		Method: http.MethodGet,
		Path:   a.path("/api/v3/ticker/price", "/fapi/v1/ticker/price"),
		Query:  url.Values{"symbol": []string{symbol}},
	})
	if err != nil {
		return exchange.ReferencePrice{}, err
	}
	var payload struct {
		Price string `json:"price"`
		Time  int64  `json:"time"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.ReferencePrice{}, typedRejected("decode reference price", err)
	}
	price, err := shared.ParseDecimal(payload.Price)
	if err != nil || price.Cmp(shared.Zero()) <= 0 {
		return exchange.ReferencePrice{}, typedRejected("invalid reference price", err)
	}
	updatedAt := time.Now().UTC()
	if payload.Time > 0 {
		updatedAt = time.UnixMilli(payload.Time)
	}
	return exchange.ReferencePrice{Price: price, UpdatedAt: updatedAt}, nil
}

type spotAccount struct {
	UpdateTime int64 `json:"updateTime"`
	Balances   []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	} `json:"balances"`
}

type swapAccount struct {
	TotalWalletBalance    string `json:"totalWalletBalance"`
	TotalMarginBalance    string `json:"totalMarginBalance"`
	AvailableBalance      string `json:"availableBalance"`
	TotalInitialMargin    string `json:"totalInitialMargin"`
	TotalMaintMargin      string `json:"totalMaintMargin"`
	TotalUnrealizedProfit string `json:"totalUnrealizedProfit"`
	Assets                []struct {
		Asset            string `json:"asset"`
		WalletBalance    string `json:"walletBalance"`
		AvailableBalance string `json:"availableBalance"`
		InitialMargin    string `json:"initialMargin"`
		UnrealizedProfit string `json:"unrealizedProfit"`
		UpdateTime       int64  `json:"updateTime"`
	} `json:"assets"`
}

func (a *Adapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	if a.config.MarketType == exchange.MarketTypeSwap {
		if err := a.ensureOneWayMode(ctx); err != nil {
			return exchange.AccountSnapshot{}, err
		}
		if err := a.ensureSingleAssetMode(ctx); err != nil {
			return exchange.AccountSnapshot{}, err
		}
	}
	path := a.path("/api/v3/account", "/fapi/v3/account")
	raw, err := a.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	if a.config.MarketType == exchange.MarketTypeSpot {
		var payload spotAccount
		if err := json.Unmarshal(raw, &payload); err != nil {
			return exchange.AccountSnapshot{}, typedRejected("decode account", err)
		}
		snapshotTime := payload.UpdateTime
		if snapshotTime <= 0 {
			snapshotTime = time.Now().UTC().UnixMilli()
		}
		snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millis(snapshotTime)}
		for _, balance := range payload.Balances {
			available, err := decimalOrZero(balance.Free)
			if err != nil {
				return exchange.AccountSnapshot{}, err
			}
			locked, err := decimalOrZero(balance.Locked)
			if err != nil {
				return exchange.AccountSnapshot{}, err
			}
			snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
				Asset: balance.Asset, Available: available, Locked: locked, Total: available.Add(locked),
			})
		}
		return snapshot, nil
	}
	var payload swapAccount
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.AccountSnapshot{}, typedRejected("decode futures account", err)
	}
	snapshotTime := int64(0)
	for _, asset := range payload.Assets {
		if asset.UpdateTime > snapshotTime {
			snapshotTime = asset.UpdateTime
		}
	}
	if snapshotTime == 0 {
		// `/fapi/v3/account` has no top-level updateTime and may return no
		// timestamp for an empty account. In that case the successful fetch
		// completion is the authoritative observation boundary.
		snapshotTime = time.Now().UTC().UnixMilli()
	}
	snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millis(snapshotTime)}
	snapshot.Equity, err = decimalOrZero(payload.TotalMarginBalance)
	if err == nil {
		snapshot.AvailableFunds, err = decimalOrZero(payload.AvailableBalance)
	}
	if err == nil {
		snapshot.UsedMargin, err = decimalOrZero(payload.TotalInitialMargin)
	}
	if err == nil {
		snapshot.MaintenanceMargin, err = decimalOrZero(payload.TotalMaintMargin)
	}
	if err == nil {
		snapshot.UnrealizedPnL, err = decimalOrZero(payload.TotalUnrealizedProfit)
	}
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	for _, balance := range payload.Assets {
		total, parseErr := decimalOrZero(balance.WalletBalance)
		if parseErr != nil {
			return exchange.AccountSnapshot{}, parseErr
		}
		available, parseErr := decimalOrZero(balance.AvailableBalance)
		if parseErr != nil {
			return exchange.AccountSnapshot{}, parseErr
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Asset: balance.Asset, Available: available, Locked: total.Sub(available), Total: total,
		})
	}
	return snapshot, nil
}

func (a *Adapter) ensureOneWayMode(ctx context.Context) error {
	raw, err := a.request(ctx, http.MethodGet, "/fapi/v1/positionSide/dual", nil)
	if err != nil {
		return err
	}
	var payload struct {
		DualSidePosition bool `json:"dualSidePosition"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return typedRejected("decode position mode", err)
	}
	if payload.DualSidePosition {
		return typedRejected("hedge position mode is unsupported", nil)
	}
	return nil
}

func (a *Adapter) ensureSingleAssetMode(ctx context.Context) error {
	raw, err := a.request(ctx, http.MethodGet, "/fapi/v1/multiAssetsMargin", nil)
	if err != nil {
		return err
	}
	var payload struct {
		MultiAssetsMargin bool `json:"multiAssetsMargin"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return typedRejected("decode multi-assets margin mode", err)
	}
	if payload.MultiAssetsMargin {
		return typedRejected("multi-assets margin is unsupported", nil)
	}
	return nil
}

type positionRisk struct {
	Symbol           string `json:"symbol"`
	PositionSide     string `json:"positionSide"`
	PositionAmt      string `json:"positionAmt"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice"`
	Leverage         string `json:"leverage"`
	MarginType       string `json:"marginType"`
	IsolatedMargin   string `json:"isolatedMargin"`
	LiquidationPrice string `json:"liquidationPrice"`
	UnRealizedProfit string `json:"unRealizedProfit"`
	UpdateTime       int64  `json:"updateTime"`
}

func (a *Adapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	if a.config.MarketType == exchange.MarketTypeSpot {
		return nil, nil
	}
	raw, err := a.request(ctx, http.MethodGet, "/fapi/v3/positionRisk", nil)
	if err != nil {
		return nil, err
	}
	var payload []positionRisk
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, typedRejected("decode positions", err)
	}
	out := make([]exchange.Position, 0, len(payload))
	for _, row := range payload {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return nil, typedRejected("hedge position mode is unsupported", nil)
		}
		quantity, err := decimalOrZero(row.PositionAmt)
		if err != nil || quantity.IsZero() {
			continue
		}
		position := exchange.Position{
			ExchangeAccountID: a.config.ExchangeAccountID,
			Symbol:            row.Symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: millis(row.UpdateTime),
		}
		position.EntryPrice, err = decimalOrZero(row.EntryPrice)
		if err == nil {
			position.MarkPrice, err = decimalOrZero(row.MarkPrice)
		}
		if err == nil {
			position.Leverage, err = decimalOrZero(row.Leverage)
		}
		if err == nil {
			position.LiquidationPrice, err = decimalOrZero(row.LiquidationPrice)
		}
		if err == nil {
			position.UnrealizedPnL, err = decimalOrZero(row.UnRealizedProfit)
		}
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(row.MarginType, "cross") {
			return nil, typedRejected("isolated margin is unsupported", nil)
		}
		out = append(out, position)
	}
	return out, nil
}

type orderPayload struct {
	OrderID            json.Number `json:"orderId"`
	ClientOrderID      string      `json:"clientOrderId"`
	Symbol             string      `json:"symbol"`
	Type               string      `json:"type"`
	TimeInForce        string      `json:"timeInForce"`
	Side               string      `json:"side"`
	PositionSide       string      `json:"positionSide"`
	OrigQty            string      `json:"origQty"`
	ExecutedQty        string      `json:"executedQty"`
	AvgPrice           string      `json:"avgPrice"`
	CumulativeQuoteQty string      `json:"cummulativeQuoteQty"`
	Price              string      `json:"price"`
	ReduceOnly         bool        `json:"reduceOnly"`
	Status             string      `json:"status"`
	Time               int64       `json:"time"`
	UpdateTime         int64       `json:"updateTime"`
}

func (a *Adapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	raw, err := a.request(
		ctx,
		http.MethodGet,
		a.path("/api/v3/openOrders", "/fapi/v1/openOrders"),
		nil,
	)
	if err != nil {
		return nil, err
	}
	var payload []orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, typedRejected("decode open orders", err)
	}
	return a.ordersFromPayload(payload)
}

func (a *Adapter) GetOrder(
	ctx context.Context,
	symbol string,
	clientOrderID string,
) (exchange.Order, error) {
	if strings.TrimSpace(symbol) == "" || strings.TrimSpace(clientOrderID) == "" {
		return exchange.Order{}, typedRejected("symbol and client order id are required", nil)
	}
	raw, err := a.request(ctx, http.MethodGet, a.path("/api/v3/order", "/fapi/v1/order"), url.Values{
		"symbol":            []string{symbol},
		"origClientOrderId": []string{clientOrderID},
	})
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, typedRejected("decode order", err)
	}
	return a.orderFromPayload(payload)
}

func (a *Adapter) GetOrderByExchangeID(
	ctx context.Context,
	symbol string,
	exchangeOrderID string,
) (exchange.Order, error) {
	if strings.TrimSpace(symbol) == "" || strings.TrimSpace(exchangeOrderID) == "" {
		return exchange.Order{}, typedRejected("symbol and Exchange order id are required", nil)
	}
	raw, err := a.request(
		ctx,
		http.MethodGet,
		a.path("/api/v3/order", "/fapi/v1/order"),
		url.Values{
			"symbol":  []string{symbol},
			"orderId": []string{exchangeOrderID},
		},
	)
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, typedRejected("decode order", err)
	}
	return a.orderFromPayload(payload)
}

func (a *Adapter) PlaceOrder(
	ctx context.Context,
	request exchange.OrderRequest,
) (exchange.Order, error) {
	if err := validateOrderRequest(request, a.config.MarketType); err != nil {
		return exchange.Order{}, err
	}
	values := url.Values{
		"symbol":           []string{request.Symbol},
		"newClientOrderId": []string{request.ClientOrderID},
		"side":             []string{string(request.Side)},
		"type":             []string{string(request.OrderType)},
		"quantity":         []string{request.Quantity.String()},
		"newOrderRespType": []string{"RESULT"},
	}
	if request.OrderType == exchange.OrderTypeLimit {
		values.Set("timeInForce", string(request.EffectiveFillPolicy()))
		values.Set("price", request.LimitPrice.String())
	}
	if a.config.MarketType == exchange.MarketTypeSwap {
		values.Set("positionSide", "BOTH")
		if request.ReduceOnly {
			values.Set("reduceOnly", "true")
		}
	}
	raw, err := a.request(
		ctx,
		http.MethodPost,
		a.path("/api/v3/order", "/fapi/v1/order"),
		values,
	)
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, transportUnknown("decode placed order", err)
	}
	order, err := a.orderFromPayload(payload)
	if err != nil {
		return exchange.Order{}, err
	}
	// Some Binance acknowledgements omit these echoed fields.
	order.ClientOrderID = request.ClientOrderID
	order.Symbol = request.Symbol
	order.OrderType = request.OrderType
	order.TimeInForce = request.NativeTimeInForce()
	order.Side = request.Side
	order.PositionSide = request.PositionSide
	order.Quantity = request.Quantity
	order.ReduceOnly = request.ReduceOnly
	return order, nil
}

func (a *Adapter) CancelOrder(
	ctx context.Context,
	symbol string,
	clientOrderID string,
) (exchange.Order, error) {
	raw, err := a.request(ctx, http.MethodDelete, a.path("/api/v3/order", "/fapi/v1/order"), url.Values{
		"symbol":            []string{symbol},
		"origClientOrderId": []string{clientOrderID},
	})
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, transportUnknown("decode canceled order", err)
	}
	return a.orderFromPayload(payload)
}

type tradePayload struct {
	ID              json.Number `json:"id"`
	OrderID         json.Number `json:"orderId"`
	Symbol          string      `json:"symbol"`
	Price           string      `json:"price"`
	Qty             string      `json:"qty"`
	Commission      string      `json:"commission"`
	CommissionAsset string      `json:"commissionAsset"`
	RealizedPnl     string      `json:"realizedPnl"`
	Time            int64       `json:"time"`
	IsBuyer         bool        `json:"isBuyer"`
	IsMaker         bool        `json:"isMaker"`
	PositionSide    string      `json:"positionSide"`
}

func (a *Adapter) ListRecentFills(
	ctx context.Context,
	symbol string,
	cursor string,
) ([]exchange.Fill, string, error) {
	if strings.TrimSpace(symbol) == "" {
		return nil, cursor, typedRejected("symbol is required to list Binance fills", nil)
	}
	const pageSize = 1000
	values := url.Values{
		"symbol": []string{symbol},
		"limit":  []string{strconv.Itoa(pageSize)},
	}
	catchUp := true
	if cursor != "" {
		tradeID, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil || tradeID == ^uint64(0) {
			return nil, cursor, typedRejected("invalid Binance fill cursor", err)
		}
		// Binance fromId is inclusive. Advance past the last applied trade so
		// an idle account does not return the same Fill on every sync.
		values.Set("fromId", strconv.FormatUint(tradeID+1, 10))
	} else {
		// A configured symbol is an explicit request to recover every Fill the
		// Exchange still exposes, including terminal manual orders from before
		// MooX first started.
		values.Set("fromId", "0")
	}
	out := make([]exchange.Fill, 0)
	next := cursor
	clientOrderIDs := make(map[string]string)
	for {
		raw, err := a.request(
			ctx,
			http.MethodGet,
			a.path("/api/v3/myTrades", "/fapi/v1/userTrades"),
			values,
		)
		if err != nil {
			return nil, cursor, err
		}
		var payload []tradePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, cursor, typedRejected("decode fills", err)
		}
		for _, row := range payload {
			fill, err := a.fillFromPayload(row)
			if err != nil {
				return nil, cursor, err
			}
			orderID := row.OrderID.String()
			clientOrderID, found := clientOrderIDs[orderID]
			if !found {
				clientOrderID, err = a.clientOrderIDByExchangeOrderID(ctx, symbol, orderID)
				if exchange.IsKind(err, exchange.ErrorOrderNotFound) {
					clientOrderID = ""
				} else if err != nil {
					return nil, cursor, err
				}
				clientOrderIDs[orderID] = clientOrderID
			}
			fill.ClientOrderID = clientOrderID
			out = append(out, fill)
			if row.ID.String() != "" {
				next = row.ID.String()
			}
		}
		if !catchUp || len(payload) < pageSize {
			break
		}
		lastID, err := strconv.ParseUint(next, 10, 64)
		if err != nil || lastID == ^uint64(0) {
			return nil, cursor, typedRejected("invalid Binance fill page cursor", err)
		}
		fromID := strconv.FormatUint(lastID+1, 10)
		if values.Get("fromId") == fromID {
			return nil, cursor, typedRejected("Binance fill page did not advance", nil)
		}
		values.Set("fromId", fromID)
	}
	return out, next, nil
}

func (a *Adapter) clientOrderIDByExchangeOrderID(
	ctx context.Context,
	symbol string,
	exchangeOrderID string,
) (string, error) {
	if exchangeOrderID == "" {
		return "", typedRejected("Binance Fill is missing order id", nil)
	}
	raw, err := a.request(
		ctx,
		http.MethodGet,
		a.path("/api/v3/order", "/fapi/v1/order"),
		url.Values{"symbol": []string{symbol}, "orderId": []string{exchangeOrderID}},
	)
	if err != nil {
		return "", classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", typedRejected("decode Fill order", err)
	}
	if strings.TrimSpace(payload.ClientOrderID) == "" {
		return "", typedRejected("Binance Fill order is missing client order id", nil)
	}
	return payload.ClientOrderID, nil
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
		return typedRejected("positive integer SWAP leverage is required", nil)
	}
	raw, err := a.request(ctx, http.MethodPost, "/fapi/v1/leverage", url.Values{
		"symbol":   []string{symbol},
		"leverage": []string{leverage.String()},
	})
	return classifyAPIError(err, raw)
}

func (a *Adapter) SetMarginMode(
	ctx context.Context,
	symbol string,
	mode exchange.MarginMode,
) error {
	if a.config.MarketType != exchange.MarketTypeSwap ||
		mode != exchange.MarginModeCross ||
		strings.TrimSpace(symbol) == "" {
		return typedRejected("only CROSS SWAP margin mode is supported", nil)
	}
	raw, err := a.request(ctx, http.MethodPost, "/fapi/v1/marginType", url.Values{
		"symbol":     []string{symbol},
		"marginType": []string{"CROSSED"},
	})
	if err != nil && apiCode(raw) == "-4046" {
		return nil
	}
	return classifyAPIError(err, raw)
}

func (a *Adapter) ordersFromPayload(payload []orderPayload) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(payload))
	for _, row := range payload {
		order, err := a.orderFromPayload(row)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, nil
}

func (a *Adapter) orderFromPayload(row orderPayload) (exchange.Order, error) {
	quantity, err := decimalOrZero(row.OrigQty)
	if err != nil {
		return exchange.Order{}, err
	}
	filled, err := decimalOrZero(row.ExecutedQty)
	if err != nil {
		return exchange.Order{}, err
	}
	average, err := decimalOrZero(row.AvgPrice)
	if err != nil {
		return exchange.Order{}, err
	}
	if average.IsZero() && !filled.IsZero() {
		cumulativeQuote, parseErr := decimalOrZero(row.CumulativeQuoteQty)
		if parseErr != nil {
			return exchange.Order{}, parseErr
		}
		if cumulativeQuote.Cmp(shared.Zero()) > 0 {
			average = cumulativeQuote.Div(filled)
		}
	}
	price, err := decimalOrZero(row.Price)
	if err != nil {
		return exchange.Order{}, err
	}
	var limitPrice *shared.Decimal
	if exchange.OrderType(row.Type) == exchange.OrderTypeLimit &&
		price.Cmp(shared.Zero()) > 0 {
		limitPrice = &price
	}
	positionSide := exchange.PositionSideUnspecified
	if a.config.MarketType == exchange.MarketTypeSwap {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return exchange.Order{}, typedRejected("hedge order is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	return exchange.Order{
		ExchangeOrderID: row.OrderID.String(),
		ClientOrderID:   row.ClientOrderID,
		Symbol:          row.Symbol,
		OrderType:       exchange.OrderType(row.Type),
		TimeInForce:     exchange.TimeInForce(row.TimeInForce),
		Side:            exchange.Side(row.Side),
		PositionSide:    positionSide,
		Quantity:        quantity,
		LimitPrice:      limitPrice,
		FilledQuantity:  filled,
		AveragePrice:    average,
		ReduceOnly:      row.ReduceOnly,
		Status:          mapOrderStatus(row.Status, filled),
		CreatedAt:       millis(row.Time),
		UpdatedAt:       millis(row.UpdateTime),
	}, nil
}

func (a *Adapter) fillFromPayload(row tradePayload) (exchange.Fill, error) {
	quantity, err := decimalOrZero(row.Qty)
	if err != nil {
		return exchange.Fill{}, err
	}
	price, err := decimalOrZero(row.Price)
	if err != nil {
		return exchange.Fill{}, err
	}
	fee, err := decimalOrZero(row.Commission)
	if err != nil {
		return exchange.Fill{}, err
	}
	pnl, err := decimalOrZero(row.RealizedPnl)
	if err != nil {
		return exchange.Fill{}, err
	}
	side := exchange.SideSell
	if row.IsBuyer {
		side = exchange.SideBuy
	}
	positionSide := exchange.PositionSideUnspecified
	if a.config.MarketType == exchange.MarketTypeSwap {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return exchange.Fill{}, typedRejected("hedge fill is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	role := "TAKER"
	if row.IsMaker {
		role = "MAKER"
	}
	return exchange.Fill{
		ExchangeTradeID: row.ID.String(),
		ExchangeOrderID: row.OrderID.String(),
		Symbol:          row.Symbol,
		Side:            side,
		PositionSide:    positionSide,
		Quantity:        quantity,
		Price:           price,
		Fee:             fee.Abs(),
		FeeAsset:        row.CommissionAsset,
		RealizedPnL:     pnl,
		SettlementAsset: a.config.SettlementAsset,
		LiquidityRole:   role,
		TradedAt:        millis(row.Time),
	}, nil
}

func validateOrderRequest(request exchange.OrderRequest, market exchange.MarketType) error {
	if strings.TrimSpace(request.ClientOrderID) == "" ||
		strings.TrimSpace(request.Symbol) == "" ||
		!request.Side.Valid() ||
		request.Quantity.Cmp(shared.Zero()) <= 0 {
		return typedRejected("invalid order request", nil)
	}
	switch request.OrderType {
	case exchange.OrderTypeMarket:
		if request.LimitPrice != nil ||
			request.EffectiveFillPolicy() != exchange.FillPolicyUnspecified {
			return typedRejected("MARKET order cannot carry price or TimeInForce", nil)
		}
	case exchange.OrderTypeLimit:
		if request.LimitPrice == nil ||
			request.LimitPrice.Cmp(shared.Zero()) <= 0 ||
			!request.EffectiveFillPolicy().ValidForLimit() {
			return typedRejected("invalid LIMIT order", nil)
		}
	default:
		return typedRejected("unsupported order type", nil)
	}
	if market == exchange.MarketTypeSpot {
		if request.PositionSide != exchange.PositionSideUnspecified || request.ReduceOnly {
			return typedRejected("SPOT order cannot set position side or reduce-only", nil)
		}
	} else if request.PositionSide != exchange.PositionSideNet {
		return typedRejected("SWAP order requires NET position side", nil)
	}
	return nil
}

func classifyAPIError(err error, raw []byte) error {
	if err == nil {
		return nil
	}
	var typed *exchange.Error
	if !errors.As(err, &typed) {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	code := apiCode(raw)
	typed.Code = code
	switch code {
	case "-2010", "-2019":
		typed.Kind = exchange.ErrorInsufficientBalance
	case "-2011", "-2013":
		typed.Kind = exchange.ErrorOrderNotFound
	case "-2015":
		typed.Kind = exchange.ErrorAuthentication
	case "-1003":
		typed.Kind = exchange.ErrorRateLimited
	}
	return typed
}

func apiCode(raw []byte) string {
	var payload struct {
		Code json.Number `json:"code"`
	}
	_ = json.Unmarshal(raw, &payload)
	return payload.Code.String()
}

func mapOrderStatus(status string, filled shared.Decimal) exchange.OrderStatus {
	switch status {
	case "NEW":
		return exchange.OrderStatusOpen
	case "PARTIALLY_FILLED":
		return exchange.OrderStatusPartiallyFilled
	case "FILLED":
		return exchange.OrderStatusFilled
	case "CANCELED":
		if filled.Cmp(shared.Zero()) > 0 {
			return exchange.OrderStatusPartiallyCanceled
		}
		return exchange.OrderStatusCanceled
	case "EXPIRED":
		return exchange.OrderStatusExpired
	case "REJECTED":
		return exchange.OrderStatusRejected
	default:
		return exchange.OrderStatusSubmitUnknown
	}
}

func typedRejected(message string, err error) error {
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

func decimalOrZero(raw string) (shared.Decimal, error) {
	if raw == "" {
		return shared.Zero(), nil
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Decimal{}, typedRejected("invalid Exchange decimal", err)
	}
	return value, nil
}

func parseOptionalDecimal(raw string) (shared.Decimal, error) {
	return decimalOrZero(raw)
}

func cloneValues(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

func millis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
