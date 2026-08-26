package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

var errListenKeyExpired = errors.New("binance: private stream listen key expired")

type listenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

type spotSubscriptionRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func (a *Adapter) Subscribe(ctx context.Context, handler execution.AccountEventHandler) error {
	if handler == nil {
		return typedRejected("private event handler is required", nil)
	}
	if a.config.MarketType == exchange.MarketTypeSpot {
		return a.subscribeSpotPrivate(ctx, handler)
	}
	return a.subscribeSwapPrivate(ctx, handler)
}

func (a *Adapter) subscribeSpotPrivate(
	ctx context.Context,
	handler execution.AccountEventHandler,
) error {
	connection, _, err := websocket.DefaultDialer.DialContext(
		ctx,
		privateStreamEndpoint(a.config),
		http.Header{"Origin": []string{"https://moox.local"}},
	)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()

	request := a.newSpotSubscriptionRequest(time.Now().UnixMilli())
	if err := connection.WriteJSON(request); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	var acknowledgement struct {
		Status int `json:"status"`
		Result struct {
			SubscriptionID *int `json:"subscriptionId"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := connection.ReadJSON(&acknowledgement); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	if acknowledgement.Status != http.StatusOK ||
		acknowledgement.Result.SubscriptionID == nil {
		return classifySpotSubscriptionError(
			acknowledgement.Status,
			acknowledgement.Error,
		)
	}
	handler.OnSubscribed()
	return a.receivePrivate(ctx, connection, handler)
}

func classifySpotSubscriptionError(status int, raw json.RawMessage) error {
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &payload)
	cause := fmt.Errorf("status %d, code %d: %s", status, payload.Code, payload.Msg)
	switch {
	case status == http.StatusUnauthorized || payload.Code == -2015:
		return &exchange.Error{
			Kind: exchange.ErrorAuthentication, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	case status == http.StatusTooManyRequests ||
		status == http.StatusTeapot ||
		payload.Code == -1003:
		return &exchange.Error{
			Kind: exchange.ErrorRateLimited, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	case status >= http.StatusInternalServerError:
		return &exchange.Error{
			Kind: exchange.ErrorTransportUnknown, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	default:
		return &exchange.Error{
			Kind: exchange.ErrorRejected, HTTPStatus: status,
			Code: fmt.Sprint(payload.Code), Err: cause,
		}
	}
}

func (a *Adapter) newSpotSubscriptionRequest(timestamp int64) spotSubscriptionRequest {
	params := url.Values{
		"apiKey":     []string{a.credential.APIKey},
		"recvWindow": []string{fmt.Sprint(recvWindow)},
		"timestamp":  []string{fmt.Sprint(timestamp)},
	}
	params.Set("signature", fmt.Sprintf(
		"%x",
		hmacSha256([]byte(a.credential.APISecret), []byte(params.Encode())),
	))
	return spotSubscriptionRequest{
		ID: "private-" + fmt.Sprint(timestamp), Method: "userDataStream.subscribe.signature",
		Params: map[string]any{
			"apiKey": a.credential.APIKey, "recvWindow": recvWindow,
			"timestamp": timestamp, "signature": params.Get("signature"),
		},
	}
}

func (a *Adapter) subscribeSwapPrivate(
	ctx context.Context,
	handler execution.AccountEventHandler,
) error {
	path := "/fapi/v1/listenKey"
	raw, err := a.client().Do(ctx, &httpclient.Request{
		Method: http.MethodPost,
		Path:   path,
		Headers: map[string]string{
			"X-MBX-APIKEY": a.credential.APIKey,
		},
	})
	if err != nil {
		return fmt.Errorf("binance: create private stream: %w", err)
	}
	var key listenKeyResponse
	if err := json.Unmarshal(raw, &key); err != nil || key.ListenKey == "" {
		return typedRejected("invalid private stream listen key", err)
	}
	endpoint := privateStreamEndpoint(a.config) + key.ListenKey
	connection, _, err := websocket.DefaultDialer.DialContext(
		ctx,
		endpoint,
		http.Header{"Origin": []string{"https://moox.local"}},
	)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()

	keepaliveDone := make(chan struct{})
	defer close(keepaliveDone)
	go a.keepListenKeyAlive(ctx, path, key.ListenKey, connection, keepaliveDone)

	handler.OnSubscribed()
	return a.receivePrivate(ctx, connection, handler)
}

func (a *Adapter) receivePrivate(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
) error {
	return a.receivePrivateWithHeartbeat(
		ctx,
		connection,
		handler,
		a.privateHeartbeatTimeout(),
	)
}

func (a *Adapter) privateHeartbeatTimeout() time.Duration {
	if a.config.MarketType == exchange.MarketTypeSwap {
		// USD-M sends a server ping roughly every three minutes.
		return 4 * time.Minute
	}
	return 60 * time.Second
}

func (a *Adapter) receivePrivateWithHeartbeat(
	ctx context.Context,
	connection *websocket.Conn,
	handler execution.AccountEventHandler,
	heartbeatTimeout time.Duration,
) error {
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()
	refreshDeadline := func() error {
		return connection.SetReadDeadline(time.Now().Add(heartbeatTimeout))
	}
	if err := refreshDeadline(); err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	connection.SetPingHandler(func(data string) error {
		if err := refreshDeadline(); err != nil {
			return err
		}
		return connection.WriteControl(
			websocket.PongMessage,
			[]byte(data),
			time.Now().Add(5*time.Second),
		)
	})
	for {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
		}
		if err := refreshDeadline(); err != nil {
			return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
		}
		if err := a.dispatchPrivate(ctx, payload, handler); err != nil {
			return err
		}
	}
}

func (a *Adapter) keepListenKeyAlive(
	ctx context.Context,
	path string,
	listenKey string,
	connection *websocket.Conn,
	done <-chan struct{},
) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			_, err := a.client().Do(ctx, &httpclient.Request{
				Method: http.MethodPut,
				Path:   path,
				Query:  url.Values{"listenKey": []string{listenKey}},
				Headers: map[string]string{
					"X-MBX-APIKEY": a.credential.APIKey,
				},
			})
			if err != nil {
				_ = connection.Close()
				return
			}
		}
	}
}

type privateOrderFields struct {
	Execution       string      `json:"x"`
	Symbol          string      `json:"s"`
	Side            string      `json:"S"`
	OrderType       string      `json:"o"`
	TimeInForce     string      `json:"f"`
	ClientID        string      `json:"c"`
	OrderID         json.Number `json:"i"`
	OrderStatus     string      `json:"X"`
	PositionSide    string      `json:"ps"`
	ReduceOnly      bool        `json:"R"`
	OriginalQty     string      `json:"q"`
	FilledQty       string      `json:"z"`
	AveragePrice    string      `json:"ap"`
	TradeID         json.Number `json:"t"`
	LastQty         string      `json:"l"`
	LastPrice       string      `json:"L"`
	Fee             string      `json:"n"`
	FeeAsset        string      `json:"N"`
	RealizedPnL     string      `json:"rp"`
	CumulativeQuote string      `json:"Z"`
	Maker           bool        `json:"m"`
	TransactionAt   int64       `json:"T"`
}

func (a *Adapter) dispatchPrivate(
	ctx context.Context,
	payload []byte,
	handler execution.AccountEventHandler,
) error {
	var wrapper struct {
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		return typedRejected("decode private event wrapper", err)
	}
	if len(wrapper.Event) != 0 && string(wrapper.Event) != "null" {
		payload = wrapper.Event
	}
	var root struct {
		Event   string          `json:"e"`
		Time    int64           `json:"E"`
		Order   json.RawMessage `json:"o"`
		Account json.RawMessage `json:"a"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return typedRejected("decode private event", err)
	}
	if root.Event == "listenKeyExpired" {
		return errListenKeyExpired
	}
	if root.Event == "ACCOUNT_UPDATE" {
		return a.dispatchFuturesAccount(ctx, root.Time, root.Account, handler)
	}
	if root.Event == "outboundAccountPosition" {
		return a.dispatchSpotAccount(ctx, payload, handler)
	}
	var envelope privateOrderFields
	switch root.Event {
	case "ORDER_TRADE_UPDATE":
		if len(root.Order) == 0 || root.Order[0] != '{' {
			return typedRejected("missing futures order event", nil)
		}
		if err := json.Unmarshal(root.Order, &envelope); err != nil {
			return typedRejected("decode futures private order", err)
		}
	case "executionReport":
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return typedRejected("decode spot private order", err)
		}
	default:
		return nil
	}
	order, err := a.orderFromPayload(orderPayload{
		OrderID: envelope.OrderID, ClientOrderID: envelope.ClientID,
		Symbol: envelope.Symbol, Type: envelope.OrderType,
		TimeInForce: envelope.TimeInForce, Side: envelope.Side,
		PositionSide: envelope.PositionSide, OrigQty: envelope.OriginalQty,
		ExecutedQty: envelope.FilledQty, AvgPrice: envelope.AveragePrice,
		CumulativeQuoteQty: envelope.CumulativeQuote,
		ReduceOnly:         envelope.ReduceOnly, Status: envelope.OrderStatus,
		UpdateTime: envelope.TransactionAt,
	})
	if err != nil {
		return err
	}
	if err := handler.OnOrder(ctx, order); err != nil {
		return err
	}
	if envelope.Execution != "TRADE" || envelope.TradeID.String() == "" {
		return nil
	}
	quantity, err := decimalOrZero(envelope.LastQty)
	if err != nil {
		return err
	}
	price, err := decimalOrZero(envelope.LastPrice)
	if err != nil {
		return err
	}
	fee, err := decimalOrZero(envelope.Fee)
	if err != nil {
		return err
	}
	pnl, err := decimalOrZero(envelope.RealizedPnL)
	if err != nil {
		return err
	}
	positionSide := exchange.PositionSideUnspecified
	if a.config.MarketType == exchange.MarketTypeSwap {
		if envelope.PositionSide != "" && envelope.PositionSide != "BOTH" {
			return typedRejected("hedge fill is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	return handler.OnFill(ctx, exchange.Fill{
		ExchangeTradeID: envelope.TradeID.String(),
		ExchangeOrderID: envelope.OrderID.String(),
		ClientOrderID:   envelope.ClientID,
		ExchangeSymbol:  envelope.Symbol,
		Side:            exchange.Side(envelope.Side),
		PositionSide:    positionSide,
		Quantity:        quantity,
		Price:           price,
		Fee:             fee.Abs(),
		FeeAsset:        envelope.FeeAsset,
		RealizedPnL:     pnl,
		SettlementAsset: a.config.SettlementAsset,
		LiquidityRole:   binanceLiquidityRole(envelope.Maker),
		TradedAt:        millis(envelope.TransactionAt),
	})
}

func binanceLiquidityRole(maker bool) string {
	if maker {
		return "MAKER"
	}
	return "TAKER"
}

func (a *Adapter) dispatchFuturesAccount(
	ctx context.Context,
	eventTime int64,
	raw json.RawMessage,
	handler execution.AccountEventHandler,
) error {
	var payload struct {
		Balances []struct {
			Asset  string `json:"a"`
			Wallet string `json:"wb"`
			Cross  string `json:"cw"`
		} `json:"B"`
		Positions []struct {
			Symbol       string `json:"s"`
			Quantity     string `json:"pa"`
			EntryPrice   string `json:"ep"`
			Unrealized   string `json:"up"`
			MarginType   string `json:"mt"`
			PositionSide string `json:"ps"`
		} `json:"P"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return typedRejected("decode futures account event", err)
	}
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: millis(eventTime),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
		RequiresSync:      true,
	}
	for _, row := range payload.Balances {
		total, err := decimalOrZero(row.Wallet)
		if err != nil {
			return err
		}
		available, err := decimalOrZero(row.Cross)
		if err != nil {
			return err
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Asset: row.Asset, Available: available, Locked: total.Sub(available), Total: total,
		})
	}
	if err := handler.OnAccountSnapshot(ctx, snapshot); err != nil {
		return err
	}
	for _, row := range payload.Positions {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return typedRejected("hedge position mode is unsupported", nil)
		}
		if !strings.EqualFold(row.MarginType, "cross") {
			return typedRejected("isolated margin is unsupported", nil)
		}
		quantity, err := decimalOrZero(row.Quantity)
		if err != nil {
			return err
		}
		position := exchange.Position{
			TradingAccountID: a.config.TradingAccountID,
			ExchangeSymbol:   row.Symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: millis(eventTime),
			Present: exchange.PositionPresence{
				SignedQuantity: true,
				EntryPrice:     true,
				MarginMode:     true,
				UnrealizedPnL:  true,
			},
			RequiresSync: true,
		}
		position.EntryPrice, err = decimalOrZero(row.EntryPrice)
		if err == nil {
			position.UnrealizedPnL, err = decimalOrZero(row.Unrealized)
		}
		if err != nil {
			return err
		}
		if err := handler.OnPosition(ctx, position); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) dispatchSpotAccount(
	ctx context.Context,
	raw []byte,
	handler execution.AccountEventHandler,
) error {
	var payload struct {
		Time     int64 `json:"u"`
		Balances []struct {
			Asset  string `json:"a"`
			Free   string `json:"f"`
			Locked string `json:"l"`
		} `json:"B"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return typedRejected("decode spot account event", err)
	}
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: millis(payload.Time),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
	}
	for _, row := range payload.Balances {
		available, err := decimalOrZero(row.Free)
		if err != nil {
			return err
		}
		locked, err := decimalOrZero(row.Locked)
		if err != nil {
			return err
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Asset: row.Asset, Available: available, Locked: locked,
			Total: available.Add(locked),
		})
	}
	return handler.OnAccountSnapshot(ctx, snapshot)
}
