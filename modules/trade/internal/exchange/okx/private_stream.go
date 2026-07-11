package okx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"golang.org/x/net/websocket"
)

func (a *Adapter) SubscribePrivate(ctx context.Context, cred exchange.Credential, market exchange.MarketType, handler exchange.StreamHandler) error {
	if handler == nil {
		return errors.New("okx: private stream handler is required")
	}
	cfg, err := websocket.NewConfig("wss://ws.okx.com:8443/ws/v5/private", "https://moox.local")
	if err != nil {
		return err
	}
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return fmt.Errorf("okx: connect private stream: %w", err)
	}
	defer conn.Close()
	go func() { <-ctx.Done(); _ = conn.Close() }()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	login := map[string]any{"op": "login", "args": []map[string]string{{"apiKey": cred.APIKey, "passphrase": cred.Passphrase, "timestamp": timestamp, "sign": sign(cred.APISecret, timestamp, "GET", "/users/self/verify", "")}}}
	if err := websocket.JSON.Send(conn, login); err != nil {
		return err
	}
	var ack map[string]any
	if err := websocket.JSON.Receive(conn, &ack); err != nil {
		return err
	}
	if ack["event"] != "login" || ack["code"] != "0" {
		return fmt.Errorf("okx: private stream login failed: %v", ack["msg"])
	}
	subscribe := map[string]any{"op": "subscribe", "args": []map[string]string{{"channel": "orders", "instType": instType(market)}}}
	if err := websocket.JSON.Send(conn, subscribe); err != nil {
		return err
	}
	var subscribeAck map[string]any
	if err := websocket.JSON.Receive(conn, &subscribeAck); err != nil {
		return err
	}
	if subscribeAck["event"] != "subscribe" {
		return fmt.Errorf("okx: private stream subscribe failed: %v", subscribeAck["msg"])
	}
	exchange.NotifyPrivateStreamState(ctx, true)
	defer exchange.NotifyPrivateStreamState(ctx, false)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(25 * time.Second))
		var payload []byte
		if err := websocket.Message.Receive(conn, &payload); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if sendErr := websocket.Message.Send(conn, "ping"); sendErr == nil {
					continue
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("okx: receive private stream: %w", err)
		}
		if string(payload) == "pong" {
			continue
		}
		if err := dispatchOKXPrivate(payload, handler); err != nil {
			handler.OnError(err)
		}
	}
}

func dispatchOKXPrivate(payload []byte, handler exchange.StreamHandler) error {
	var message struct {
		Arg struct {
			Channel string `json:"channel"`
		} `json:"arg"`
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
		Data  []struct {
			Symbol    string `json:"instId"`
			Side      string `json:"side"`
			ClientID  string `json:"clOrdId"`
			OrderID   string `json:"ordId"`
			TradeID   string `json:"tradeId"`
			FillQty   string `json:"fillSz"`
			FillPrice string `json:"fillPx"`
			Fee       string `json:"fee"`
			FeeAsset  string `json:"feeCcy"`
			FillTime  string `json:"fillTime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &message); err != nil {
		return err
	}
	if message.Event == "error" {
		return fmt.Errorf("okx private stream %s: %s", message.Code, message.Msg)
	}
	if message.Arg.Channel != "orders" {
		return nil
	}
	for _, row := range message.Data {
		if row.TradeID == "" || row.FillQty == "" || row.FillQty == "0" {
			continue
		}
		fee := row.Fee
		if len(fee) > 0 && fee[0] == '-' {
			fee = fee[1:]
		}
		at, _ := strconv.ParseInt(row.FillTime, 10, 64)
		handler.OnTrade(&exchange.TradeEvent{Trade: exchange.Trade{ExchangeTradeID: row.TradeID, OrderID: row.OrderID, ClientOrderID: row.ClientID, Symbol: row.Symbol, Side: exchange.OrderSide(row.Side), Price: row.FillPrice, Quantity: row.FillQty, Fee: fee, FeeCurrency: row.FeeAsset, TradedAt: at}})
	}
	return nil
}
