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

type listenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

var errListenKeyExpired = errors.New("binance: private stream listen key expired")

func (a *Adapter) SubscribePrivate(ctx context.Context, cred exchange.Credential, market exchange.MarketType, handler exchange.StreamHandler) error {
	if handler == nil {
		return errors.New("binance: private stream handler is required")
	}
	path := marketPath(market, "/api/v3/userDataStream", "/fapi/v1/listenKey")
	raw, err := client(market).Do(ctx, &httpclient.Request{Method: http.MethodPost, Path: path, Headers: apiHeader(cred)})
	if err != nil {
		return fmt.Errorf("binance: create private stream: %w", err)
	}
	var key listenKeyResponse
	if err := json.Unmarshal(raw, &key); err != nil || key.ListenKey == "" {
		return errors.New("binance: invalid private stream listen key")
	}
	endpoint := "wss://stream.binance.com:9443/ws/" + key.ListenKey
	if market == exchange.MarketSwap || market == exchange.MarketFutures {
		endpoint = "wss://fstream.binance.com/ws/" + key.ListenKey
	}
	cfg, err := websocket.NewConfig(endpoint, "https://moox.local")
	if err != nil {
		return err
	}
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return fmt.Errorf("binance: connect private stream: %w", err)
	}
	defer conn.Close()
	exchange.NotifyPrivateStreamState(ctx, true)
	defer exchange.NotifyPrivateStreamState(ctx, false)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	keepaliveDone := make(chan struct{})
	defer close(keepaliveDone)
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-keepaliveDone:
				return
			case <-ticker.C:
				if _, keepaliveErr := client(market).Do(ctx, &httpclient.Request{Method: http.MethodPut, Path: path, Query: url.Values{"listenKey": []string{key.ListenKey}}, Headers: apiHeader(cred)}); keepaliveErr != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	for {
		var payload []byte
		if err := websocket.Message.Receive(conn, &payload); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("binance: receive private stream: %w", err)
		}
		if err := dispatchBinancePrivate(payload, market, handler); err != nil {
			if errors.Is(err, errListenKeyExpired) {
				return err
			}
			handler.OnError(err)
		}
	}
}

func dispatchBinancePrivate(payload []byte, market exchange.MarketType, handler exchange.StreamHandler) error {
	var envelope struct {
		Event     string      `json:"e"`
		Execution string      `json:"x"`
		Symbol    string      `json:"s"`
		Side      string      `json:"S"`
		ClientID  string      `json:"c"`
		OrderID   json.Number `json:"i"`
		TradeID   json.Number `json:"t"`
		LastQty   string      `json:"l"`
		LastPrice string      `json:"L"`
		Fee       string      `json:"n"`
		FeeAsset  string      `json:"N"`
		Order     *struct {
			Execution string      `json:"x"`
			Symbol    string      `json:"s"`
			Side      string      `json:"S"`
			ClientID  string      `json:"c"`
			OrderID   json.Number `json:"i"`
			TradeID   json.Number `json:"t"`
			LastQty   string      `json:"l"`
			LastPrice string      `json:"L"`
			Fee       string      `json:"n"`
			FeeAsset  string      `json:"N"`
		} `json:"o"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if envelope.Event == "listenKeyExpired" {
		return errListenKeyExpired
	}
	if envelope.Event == "ORDER_TRADE_UPDATE" && envelope.Order != nil {
		envelope.Execution, envelope.Symbol, envelope.Side, envelope.ClientID = envelope.Order.Execution, envelope.Order.Symbol, envelope.Order.Side, envelope.Order.ClientID
		envelope.OrderID, envelope.TradeID, envelope.LastQty, envelope.LastPrice = envelope.Order.OrderID, envelope.Order.TradeID, envelope.Order.LastQty, envelope.Order.LastPrice
		envelope.Fee, envelope.FeeAsset = envelope.Order.Fee, envelope.Order.FeeAsset
	}
	if envelope.Execution != "TRADE" || envelope.TradeID.String() == "" {
		return nil
	}
	handler.OnTrade(&exchange.TradeEvent{Trade: exchange.Trade{ExchangeTradeID: envelope.TradeID.String(), OrderID: envelope.OrderID.String(), ClientOrderID: envelope.ClientID, Symbol: envelope.Symbol, Side: exchange.OrderSide(lower(envelope.Side)), Price: envelope.LastPrice, Quantity: envelope.LastQty, Fee: envelope.Fee, FeeCurrency: envelope.FeeAsset, TradedAt: time.Now().UnixMilli()}})
	_ = market
	return nil
}

func lower(v string) string {
	if v == "BUY" {
		return "buy"
	}
	if v == "SELL" {
		return "sell"
	}
	return v
}
