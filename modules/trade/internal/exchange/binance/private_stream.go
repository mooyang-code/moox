package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"golang.org/x/net/websocket"
)

var errListenKeyExpired = errors.New("binance: private stream listen key expired")

type listenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

func (a *Adapter) SubscribePrivate(ctx context.Context, handler exchange.EventHandler) error {
	if handler == nil {
		return typedRejected("private event handler is required", nil)
	}
	path := a.path("/api/v3/userDataStream", "/fapi/v1/listenKey")
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
	endpoint := "wss://stream.binance.com:9443/ws/" + key.ListenKey
	if a.config.MarketType == exchange.MarketTypeSwap {
		endpoint = "wss://fstream.binance.com/ws/" + key.ListenKey
	}
	config, err := websocket.NewConfig(endpoint, "https://moox.local")
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()

	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()
	keepaliveDone := make(chan struct{})
	defer close(keepaliveDone)
	go a.keepListenKeyAlive(ctx, path, key.ListenKey, connection, keepaliveDone)

	for {
		var payload []byte
		if err := websocket.Message.Receive(connection, &payload); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
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
	Execution     string      `json:"x"`
	Symbol        string      `json:"s"`
	Side          string      `json:"S"`
	OrderType     string      `json:"o"`
	TimeInForce   string      `json:"f"`
	ClientID      string      `json:"c"`
	OrderID       json.Number `json:"i"`
	OrderStatus   string      `json:"X"`
	PositionSide  string      `json:"ps"`
	ReduceOnly    bool        `json:"R"`
	OriginalQty   string      `json:"q"`
	FilledQty     string      `json:"z"`
	AveragePrice  string      `json:"ap"`
	TradeID       json.Number `json:"t"`
	LastQty       string      `json:"l"`
	LastPrice     string      `json:"L"`
	Fee           string      `json:"n"`
	FeeAsset      string      `json:"N"`
	RealizedPnL   string      `json:"rp"`
	Maker         bool        `json:"m"`
	TransactionAt int64       `json:"T"`
}

func (a *Adapter) dispatchPrivate(
	ctx context.Context,
	payload []byte,
	handler exchange.EventHandler,
) error {
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
		ReduceOnly: envelope.ReduceOnly, Status: envelope.OrderStatus,
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
		Symbol:          envelope.Symbol,
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
	handler exchange.EventHandler,
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
	snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millis(eventTime)}
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
		quantity, err := decimalOrZero(row.Quantity)
		if err != nil {
			return err
		}
		position := exchange.Position{
			ExchangeAccountID: a.config.ExchangeAccountID,
			Symbol:            row.Symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: millis(eventTime),
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
	handler exchange.EventHandler,
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
	snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millis(payload.Time)}
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
